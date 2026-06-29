package upstreamerror

import (
	"encoding/json"
	"mime"
	"net/http"
	"strconv"
	"strings"
)

type Classification struct {
	Matched bool
	Status  int
	Type    string
	Reason  string
}

func ClassifyAndRewrite(status int, contentType string, body []byte) (Classification, []byte) {
	classification, payload, errorPayload := classifyPayload(status, contentType, body)
	if !classification.Matched {
		return classification, body
	}
	errorPayload["code"] = classification.Status
	errorPayload["type"] = classification.Type
	rewritten, err := json.Marshal(payload)
	if err != nil {
		return Classification{}, body
	}
	return classification, rewritten
}

func Classify(status int, contentType string, body []byte) Classification {
	classification, _, _ := classifyPayload(status, contentType, body)
	return classification
}

func Eligible(status int, contentType string) bool {
	return status == http.StatusInternalServerError && jsonContentType(contentType)
}

func classifyPayload(status int, contentType string, body []byte) (Classification, map[string]any, map[string]any) {
	if !Eligible(status, contentType) {
		return Classification{}, nil, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return Classification{}, nil, nil
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		return Classification{}, nil, nil
	}
	if !looksLikeVLLMInternalError(errorPayload) {
		return Classification{}, nil, nil
	}
	message, _ := errorPayload["message"].(string)
	return classifyMessage(message), payload, errorPayload
}

func jsonContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.TrimSpace(strings.ToLower(contentType))
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}

func looksLikeVLLMInternalError(errorPayload map[string]any) bool {
	errorType, _ := errorPayload["type"].(string)
	if errorType != "" && errorType != "InternalServerError" {
		return false
	}
	code, ok := errorPayload["code"]
	if !ok || code == nil {
		return errorType == "InternalServerError"
	}
	return numericCode(code) == http.StatusInternalServerError
}

func numericCode(code any) int {
	switch value := code.(type) {
	case float64:
		return int(value)
	case int:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return int(parsed)
	case string:
		parsed, _ := strconv.Atoi(value)
		return parsed
	default:
		return 0
	}
}

func classifyMessage(message string) Classification {
	lower := strings.ToLower(message)
	if lower == "" {
		return Classification{}
	}
	if strings.Contains(lower, "vllmvalidationerror") {
		return Classification{Matched: true, Status: http.StatusBadRequest, Type: "BadRequestError", Reason: "validation"}
	}
	if isContextLengthInputError(lower) {
		return Classification{Matched: true, Status: http.StatusBadRequest, Type: "BadRequestError", Reason: "context_length"}
	}
	if isJSONDecodeInputError(lower) {
		return Classification{Matched: true, Status: http.StatusBadRequest, Type: "BadRequestError", Reason: "json_decode"}
	}
	if isImageInputError(lower) {
		return Classification{Matched: true, Status: http.StatusUnprocessableEntity, Type: "UnprocessableEntityError", Reason: "image_input"}
	}
	return Classification{}
}

func isContextLengthInputError(message string) bool {
	if strings.Contains(message, "maximum context length") {
		return true
	}
	return strings.Contains(message, "context length") && strings.Contains(message, "exceed")
}

func isJSONDecodeInputError(message string) bool {
	if strings.Contains(message, "jsondecodeerror") {
		return true
	}
	patterns := []string{
		"expecting value:",
		"extra data:",
		"unterminated string",
		"invalid control character",
		"property name enclosed in double quotes",
	}
	for _, pattern := range patterns {
		if strings.Contains(message, pattern) {
			return true
		}
	}
	return false
}

func isImageInputError(message string) bool {
	if strings.Contains(message, "failed to load image") {
		return true
	}
	if strings.Contains(message, "cannot identify image file") || strings.Contains(message, "unidentifiedimageerror") {
		return true
	}
	if strings.Contains(message, "url=") && (strings.Contains(message, "message='forbidden'") || strings.Contains(message, "403")) {
		return true
	}
	if strings.Contains(message, "clientconnectordnserror") {
		return true
	}
	if strings.Contains(message, "cannot connect to host") &&
		(strings.Contains(message, "name or service not known") ||
			strings.Contains(message, "temporary failure in name resolution") ||
			strings.Contains(message, "ssl:")) {
		return true
	}
	return false
}
