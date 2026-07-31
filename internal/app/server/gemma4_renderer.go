package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type gemma4TextRendererConfig struct {
	BOSToken             string
	DefaultDecodeHorizon int64
	MaximumDecodeHorizon int64
}

type gemma4TextRenderer struct {
	config gemma4TextRendererConfig
}

type gemma4TextMessage struct {
	Role         string
	Content      json.RawMessage
	ContentParts []gemma4TextPart
}

type gemma4TextPart struct {
	Type string
	Text string
}

func newGemma4TextRenderer(config gemma4TextRendererConfig) (*gemma4TextRenderer, error) {
	if config.BOSToken == "" {
		return nil, fmt.Errorf("Gemma4 renderer BOS token is required")
	}
	if config.DefaultDecodeHorizon <= 0 || config.MaximumDecodeHorizon < config.DefaultDecodeHorizon {
		return nil, fmt.Errorf("Gemma4 renderer decode horizon is invalid")
	}
	return &gemma4TextRenderer{config: config}, nil
}

func (r *gemma4TextRenderer) Render(ctx context.Context, input predictiveShadowInput) (predictiveRenderedRequest, error) {
	if r == nil {
		return predictiveRenderedRequest{}, fmt.Errorf("Gemma4 renderer is nil")
	}
	if err := ctx.Err(); err != nil {
		return predictiveRenderedRequest{}, err
	}
	root, err := parseStrictJSONObject(input.Body)
	if err != nil {
		return predictiveRenderedRequest{}, err
	}
	if err := rejectGemma4UnsupportedRoot(root, input.Path); err != nil {
		return predictiveRenderedRequest{}, err
	}
	decodeHorizon, err := r.decodeHorizon(root)
	if err != nil {
		return predictiveRenderedRequest{}, err
	}
	var result predictiveRenderedRequest
	switch input.Path {
	case "/v1/completions":
		prompt, err := requiredJSONString(root, "prompt")
		if err != nil {
			return predictiveRenderedRequest{}, fmt.Errorf("Gemma4 completion prompt: %w", err)
		}
		result = predictiveRenderedRequest{
			Class:              runtimepredictive.RequestClassCompletion,
			Rendered:           []byte(prompt),
			DecodeHorizonUpper: decodeHorizon,
			Confidence:         1,
		}
	case "/v1/chat/completions":
		messages, err := parseGemma4TextMessages(root)
		if err != nil {
			return predictiveRenderedRequest{}, err
		}
		rendered, err := r.renderChat(messages)
		if err != nil {
			return predictiveRenderedRequest{}, err
		}
		result = predictiveRenderedRequest{
			Class:              runtimepredictive.RequestClassChat,
			Rendered:           rendered,
			DecodeHorizonUpper: decodeHorizon,
			Confidence:         1,
		}
	default:
		return predictiveRenderedRequest{}, fmt.Errorf("Gemma4 renderer path %q is unsupported", input.Path)
	}
	if err := ctx.Err(); err != nil {
		clear(result.Rendered)
		return predictiveRenderedRequest{}, err
	}
	return result, nil
}

func (r *gemma4TextRenderer) decodeHorizon(root map[string]json.RawMessage) (int64, error) {
	maxTokens, hasMaxTokens := nonNullJSONField(root, "max_tokens")
	maxCompletion, hasMaxCompletion := nonNullJSONField(root, "max_completion_tokens")
	if hasMaxTokens && hasMaxCompletion {
		return 0, fmt.Errorf("Gemma4 renderer output token fields conflict")
	}
	if !hasMaxTokens && !hasMaxCompletion {
		return r.config.DefaultDecodeHorizon, nil
	}
	raw := maxTokens
	if hasMaxCompletion {
		raw = maxCompletion
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil || value <= 0 {
		return 0, fmt.Errorf("Gemma4 renderer decode horizon must be a positive integer")
	}
	if value > r.config.MaximumDecodeHorizon {
		return 0, fmt.Errorf("Gemma4 renderer decode horizon exceeds the profile maximum")
	}
	return value, nil
}

func (r *gemma4TextRenderer) renderChat(messages []gemma4TextMessage) ([]byte, error) {
	var rendered bytes.Buffer
	if capacity := estimatedGemma4RenderedCapacity(r.config.BOSToken, messages); capacity > 0 {
		rendered.Grow(capacity)
	}
	rendered.WriteString(r.config.BOSToken)
	if len(messages) > 0 && (messages[0].Role == "system" || messages[0].Role == "developer") {
		rendered.WriteString("<|turn>system\n")
		content, err := renderGemma4Content(messages[0], true, false)
		if err != nil {
			return nil, err
		}
		rendered.WriteString(content)
		rendered.WriteString("<turn|>\n")
		messages = messages[1:]
	}
	previousRole := ""
	for _, message := range messages {
		if message.Role != "user" && message.Role != "assistant" {
			return nil, fmt.Errorf("Gemma4 text profile role %q is unsupported outside the first message", message.Role)
		}
		if previousRole == "assistant" && message.Role == "assistant" {
			return nil, fmt.Errorf("Gemma4 text profile consecutive assistant messages are unsupported")
		}
		role := message.Role
		if role == "assistant" {
			role = "model"
		}
		rendered.WriteString("<|turn>")
		rendered.WriteString(role)
		rendered.WriteByte('\n')
		if role == "model" {
			rendered.WriteString("<|channel>thought\n<channel|>")
		}
		content, err := renderGemma4Content(message, false, role == "model")
		if err != nil {
			return nil, err
		}
		rendered.WriteString(content)
		rendered.WriteString("<turn|>\n")
		previousRole = message.Role
	}
	rendered.WriteString("<|turn>model\n<|channel>thought\n<channel|>")
	return rendered.Bytes(), nil
}

func estimatedGemma4RenderedCapacity(bosToken string, messages []gemma4TextMessage) int {
	const perMessageOverhead = 64
	maximum := int(^uint(0) >> 1)
	capacity := len(bosToken) + perMessageOverhead
	for _, message := range messages {
		increment := len(message.Content) + perMessageOverhead
		if increment < 0 || capacity > maximum-increment {
			return 0
		}
		capacity += increment
	}
	return capacity
}

func parseStrictJSONObject(body []byte) (map[string]json.RawMessage, error) {
	if len(body) == 0 {
		return nil, fmt.Errorf("predictive request body is empty")
	}
	if !utf8.Valid(body) {
		return nil, fmt.Errorf("predictive request body is not valid UTF-8")
	}
	if err := validateUniqueJSONKeys(body); err != nil {
		return nil, err
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("decode predictive request: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("predictive request must be a JSON object")
	}
	return root, nil
}

func validateUniqueJSONKeys(body []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("decode predictive request: %w", err)
	}
	if err := walkUniqueJSONValue(decoder, first); err != nil {
		return err
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("predictive request contains a trailing JSON value")
		}
		return fmt.Errorf("decode predictive request trailing data: %w", err)
	}
	return nil
}

func walkUniqueJSONValue(decoder *json.Decoder, token json.Token) error {
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode predictive request object key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("predictive request object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("predictive request contains duplicate key %q", key)
			}
			seen[key] = struct{}{}
			value, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode predictive request object value: %w", err)
			}
			if err := walkUniqueJSONValue(decoder, value); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode predictive request object terminator: %w", err)
		}
		if closing != json.Delim('}') {
			return fmt.Errorf("predictive request object terminator is invalid")
		}
	case '[':
		for decoder.More() {
			value, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("decode predictive request array value: %w", err)
			}
			if err := walkUniqueJSONValue(decoder, value); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("decode predictive request array terminator: %w", err)
		}
		if closing != json.Delim(']') {
			return fmt.Errorf("predictive request array terminator is invalid")
		}
	default:
		return fmt.Errorf("predictive request contains an invalid JSON delimiter")
	}
	return nil
}

func rejectGemma4UnsupportedRoot(root map[string]json.RawMessage, path string) error {
	unsupported := []string{
		"tools",
		"tool_choice",
		"response_format",
		"reasoning",
		"reasoning_effort",
		"enable_thinking",
		"preserve_thinking",
		"prompt_adapter",
		"cache_salt",
		"mm_processor_kwargs",
		"max_output_tokens",
	}
	if path == "/v1/completions" {
		unsupported = append(unsupported, "suffix")
	}
	for _, field := range unsupported {
		if _, present := nonNullJSONField(root, field); present {
			return fmt.Errorf("Gemma4 text profile field %q is unsupported", field)
		}
	}
	return nil
}

func parseGemma4TextMessages(root map[string]json.RawMessage) ([]gemma4TextMessage, error) {
	raw, exists := root["messages"]
	if !exists {
		return nil, fmt.Errorf("Gemma4 chat messages are required")
	}
	var encoded []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &encoded); err != nil {
		return nil, fmt.Errorf("Gemma4 chat messages must be an array")
	}
	messages := make([]gemma4TextMessage, 0, len(encoded))
	for index, fields := range encoded {
		if fields == nil {
			return nil, fmt.Errorf("Gemma4 chat message %d must be an object", index)
		}
		for _, unsupported := range []string{"tool_calls", "tool_responses", "reasoning", "reasoning_content"} {
			if _, present := nonNullJSONField(fields, unsupported); present {
				return nil, fmt.Errorf("Gemma4 chat message %d field %q is unsupported", index, unsupported)
			}
		}
		role, err := requiredJSONString(fields, "role")
		if err != nil {
			return nil, fmt.Errorf("Gemma4 chat message %d role: %w", index, err)
		}
		if role != "system" && role != "developer" && role != "user" && role != "assistant" {
			return nil, fmt.Errorf("Gemma4 chat message %d role %q is unsupported", index, role)
		}
		content, exists := fields["content"]
		if !exists || string(content) == "null" {
			return nil, fmt.Errorf("Gemma4 chat message %d content is required", index)
		}
		message := gemma4TextMessage{Role: role, Content: content}
		trimmedContent := bytes.TrimSpace(content)
		if len(trimmedContent) > 0 && trimmedContent[0] == '[' {
			var parts []map[string]json.RawMessage
			if err := json.Unmarshal(content, &parts); err != nil {
				return nil, fmt.Errorf("Gemma4 chat message %d content parts are invalid", index)
			}
			message.ContentParts = make([]gemma4TextPart, 0, len(parts))
			for partIndex, partFields := range parts {
				partType, err := requiredJSONString(partFields, "type")
				if err != nil || partType != "text" {
					return nil, fmt.Errorf("Gemma4 chat message %d content part %d is not verified text", index, partIndex)
				}
				text, err := requiredJSONString(partFields, "text")
				if err != nil {
					return nil, fmt.Errorf("Gemma4 chat message %d content part %d text: %w", index, partIndex, err)
				}
				message.ContentParts = append(message.ContentParts, gemma4TextPart{Type: partType, Text: text})
			}
		} else {
			var text string
			if err := json.Unmarshal(content, &text); err != nil {
				return nil, fmt.Errorf("Gemma4 chat message %d content must be text or text parts", index)
			}
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func renderGemma4Content(message gemma4TextMessage, firstSystem bool, model bool) (string, error) {
	if message.ContentParts != nil {
		var rendered strings.Builder
		for _, part := range message.ContentParts {
			text := strings.TrimSpace(part.Text)
			if model {
				text = stripGemma4Thinking(text)
			}
			rendered.WriteString(text)
			if firstSystem {
				rendered.WriteByte(' ')
			}
		}
		return rendered.String(), nil
	}
	var text string
	if err := json.Unmarshal(message.Content, &text); err != nil {
		return "", fmt.Errorf("Gemma4 message content is not text")
	}
	text = strings.TrimSpace(text)
	if model {
		text = stripGemma4Thinking(text)
	}
	return text, nil
}

func stripGemma4Thinking(text string) string {
	if !strings.Contains(text, "<|channel>") && !strings.Contains(text, "<channel|>") {
		return strings.TrimSpace(text)
	}
	var stripped strings.Builder
	for _, part := range strings.Split(text, "<channel|>") {
		if marker := strings.Index(part, "<|channel>"); marker >= 0 {
			stripped.WriteString(part[:marker])
		} else {
			stripped.WriteString(part)
		}
	}
	return strings.TrimSpace(stripped.String())
}

func requiredJSONString(fields map[string]json.RawMessage, name string) (string, error) {
	raw, exists := fields[name]
	if !exists {
		return "", fmt.Errorf("field %q is required", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("field %q must be a string", name)
	}
	return value, nil
}

func nonNullJSONField(fields map[string]json.RawMessage, name string) (json.RawMessage, bool) {
	raw, exists := fields[name]
	if !exists || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, false
	}
	return raw, true
}
