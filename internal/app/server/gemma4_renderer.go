package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	ContentBytes int
	ContentText  string
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
		increment := message.ContentBytes + perMessageOverhead
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
	var root map[string]json.RawMessage
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, fmt.Errorf("decode predictive request: %w", err)
	}
	if root == nil {
		return nil, fmt.Errorf("predictive request must be a JSON object")
	}
	if err := validateUniqueJSONKeys(body); err != nil {
		return nil, err
	}
	return root, nil
}

func validateUniqueJSONKeys(body []byte) error {
	scanner := uniqueJSONKeyScanner{body: body}
	if err := scanner.skipValue(); err != nil {
		return err
	}
	scanner.skipWhitespace()
	if scanner.offset != len(body) {
		return fmt.Errorf("predictive request contains trailing JSON data")
	}
	return nil
}

type uniqueJSONKeyScanner struct {
	body   []byte
	offset int
}

func (s *uniqueJSONKeyScanner) skipValue() error {
	s.skipWhitespace()
	if s.offset >= len(s.body) {
		return fmt.Errorf("predictive request JSON value is truncated")
	}
	switch s.body[s.offset] {
	case '{':
		return s.skipObject()
	case '[':
		return s.skipArray()
	case '"':
		_, err := s.skipString()
		return err
	default:
		return s.skipScalar()
	}
}

func (s *uniqueJSONKeyScanner) skipObject() error {
	s.offset++
	s.skipWhitespace()
	if s.consume('}') {
		return nil
	}
	seen := make(map[string]struct{})
	for {
		s.skipWhitespace()
		start, err := s.skipString()
		if err != nil {
			return err
		}
		var key string
		if err := json.Unmarshal(s.body[start:s.offset], &key); err != nil {
			return fmt.Errorf("decode predictive request object key: %w", err)
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("predictive request contains duplicate key %q", key)
		}
		seen[key] = struct{}{}
		s.skipWhitespace()
		if !s.consume(':') {
			return fmt.Errorf("predictive request object key has no value")
		}
		if err := s.skipValue(); err != nil {
			return err
		}
		s.skipWhitespace()
		if s.consume('}') {
			return nil
		}
		if !s.consume(',') {
			return fmt.Errorf("predictive request object separator is invalid")
		}
	}
}

func (s *uniqueJSONKeyScanner) skipArray() error {
	s.offset++
	s.skipWhitespace()
	if s.consume(']') {
		return nil
	}
	for {
		if err := s.skipValue(); err != nil {
			return err
		}
		s.skipWhitespace()
		if s.consume(']') {
			return nil
		}
		if !s.consume(',') {
			return fmt.Errorf("predictive request array separator is invalid")
		}
	}
}

func (s *uniqueJSONKeyScanner) skipString() (int, error) {
	if s.offset >= len(s.body) || s.body[s.offset] != '"' {
		return 0, fmt.Errorf("predictive request object key is not a string")
	}
	start := s.offset
	s.offset++
	for s.offset < len(s.body) {
		value := s.body[s.offset]
		s.offset++
		switch value {
		case '"':
			return start, nil
		case '\\':
			if s.offset >= len(s.body) {
				return 0, fmt.Errorf("predictive request JSON string is truncated")
			}
			s.offset++
		}
	}
	return 0, fmt.Errorf("predictive request JSON string is unterminated")
}

func (s *uniqueJSONKeyScanner) skipScalar() error {
	start := s.offset
	for s.offset < len(s.body) {
		value := s.body[s.offset]
		if value == ',' || value == ']' || value == '}' || isJSONWhitespace(value) {
			break
		}
		s.offset++
	}
	if s.offset == start {
		return fmt.Errorf("predictive request scalar value is invalid")
	}
	return nil
}

func (s *uniqueJSONKeyScanner) skipWhitespace() {
	for s.offset < len(s.body) && isJSONWhitespace(s.body[s.offset]) {
		s.offset++
	}
}

func (s *uniqueJSONKeyScanner) consume(value byte) bool {
	if s.offset >= len(s.body) || s.body[s.offset] != value {
		return false
	}
	s.offset++
	return true
}

func isJSONWhitespace(value byte) bool {
	return value == ' ' || value == '\n' || value == '\r' || value == '\t'
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
		message := gemma4TextMessage{Role: role, ContentBytes: len(content)}
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
			if err := json.Unmarshal(content, &message.ContentText); err != nil {
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
	text := message.ContentText
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
