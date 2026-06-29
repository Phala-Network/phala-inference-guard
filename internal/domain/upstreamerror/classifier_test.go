package upstreamerror

import (
	"encoding/json"
	"net/http"
	"testing"
)

func TestClassifyAndRewriteKnownClientInputErrors(t *testing.T) {
	tests := []struct {
		name       string
		message    string
		wantStatus int
		wantType   string
		wantReason string
	}{
		{
			name:       "context length validation",
			message:    "vllm.exceptions.VLLMValidationError: This model's maximum context length is 262144 tokens",
			wantStatus: http.StatusBadRequest,
			wantType:   "BadRequestError",
			wantReason: "validation",
		},
		{
			name:       "context length exceeded",
			message:    "This model's maximum context length is 262144 tokens. However, you requested too many tokens.",
			wantStatus: http.StatusBadRequest,
			wantType:   "BadRequestError",
			wantReason: "context_length",
		},
		{
			name:       "tool argument json decode",
			message:    "Expecting value: line 1 column 1 (char 0)",
			wantStatus: http.StatusBadRequest,
			wantType:   "BadRequestError",
			wantReason: "json_decode",
		},
		{
			name:       "image url forbidden",
			message:    "403, message='Forbidden', url='https://example.invalid/image.jpg'",
			wantStatus: http.StatusUnprocessableEntity,
			wantType:   "UnprocessableEntityError",
			wantReason: "image_input",
		},
		{
			name:       "image dns failure",
			message:    "Cannot connect to host files.teleclaw.io:443 ssl:default [Name or service not known]",
			wantStatus: http.StatusUnprocessableEntity,
			wantType:   "UnprocessableEntityError",
			wantReason: "image_input",
		},
		{
			name:       "unidentified image",
			message:    "Failed to load image: cannot identify image file <_io.BytesIO object>",
			wantStatus: http.StatusUnprocessableEntity,
			wantType:   "UnprocessableEntityError",
			wantReason: "image_input",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := openAIErrorBody(tt.message)
			classification, rewritten := ClassifyAndRewrite(http.StatusInternalServerError, "application/json", body)
			if !classification.Matched {
				t.Fatalf("classification did not match")
			}
			if classification.Status != tt.wantStatus || classification.Type != tt.wantType || classification.Reason != tt.wantReason {
				t.Fatalf("classification = %#v, want status=%d type=%s reason=%s", classification, tt.wantStatus, tt.wantType, tt.wantReason)
			}
			var payload map[string]map[string]any
			if err := json.Unmarshal(rewritten, &payload); err != nil {
				t.Fatalf("rewritten body is not json: %v", err)
			}
			if payload["error"]["message"] != tt.message {
				t.Fatalf("message changed: %v", payload["error"]["message"])
			}
			if payload["error"]["type"] != tt.wantType {
				t.Fatalf("type=%v want %s", payload["error"]["type"], tt.wantType)
			}
			if int(payload["error"]["code"].(float64)) != tt.wantStatus {
				t.Fatalf("code=%v want %d", payload["error"]["code"], tt.wantStatus)
			}
		})
	}
}

func TestClassifyLeavesTrueBackendFailuresAs500(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		contentType string
		body        []byte
	}{
		{
			name:        "scheduler failure",
			status:      http.StatusInternalServerError,
			contentType: "application/json",
			body:        openAIErrorBody("Scheduler hit an exception while running the model"),
		},
		{
			name:        "non json",
			status:      http.StatusInternalServerError,
			contentType: "text/plain",
			body:        []byte("Expecting value: line 1 column 1 (char 0)"),
		},
		{
			name:        "not upstream 500",
			status:      http.StatusBadGateway,
			contentType: "application/json",
			body:        openAIErrorBody("Cannot connect to host files.teleclaw.io:443 ssl:default [Name or service not known]"),
		},
		{
			name:        "already client typed",
			status:      http.StatusInternalServerError,
			contentType: "application/json",
			body:        []byte(`{"error":{"message":"Expecting value: line 1 column 1 (char 0)","type":"BadRequestError","param":null,"code":400}}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			classification, rewritten := ClassifyAndRewrite(tt.status, tt.contentType, tt.body)
			if classification.Matched {
				t.Fatalf("classification matched unexpectedly: %#v", classification)
			}
			if string(rewritten) != string(tt.body) {
				t.Fatalf("body changed unexpectedly: %s", rewritten)
			}
		})
	}
}

func openAIErrorBody(message string) []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "InternalServerError",
			"param":   nil,
			"code":    http.StatusInternalServerError,
		},
	})
	if err != nil {
		panic(err)
	}
	return body
}
