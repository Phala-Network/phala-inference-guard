package request

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestRewriteJSONPriorityAddsNegativePriority(t *testing.T) {
	rewritten, err := RewriteJSONPriority([]byte(`{"model":"m","messages":[]}`), "priority", -100)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	if payload["model"].(string) != "m" {
		t.Fatalf("model = %v, want m", payload["model"])
	}
}

func TestRewriteJSONPriorityOverwritesExistingPriority(t *testing.T) {
	rewritten, err := RewriteJSONPriority([]byte(`{"priority":-100,"model":"m"}`), "priority", 0)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != 0 {
		t.Fatalf("priority = %v, want 0", payload["priority"])
	}
}

func TestRewriteJSONPriorityOverwritesEscapedPriorityKey(t *testing.T) {
	rewritten, err := RewriteJSONPriority([]byte(`{"prior\u0069ty":-100,"model":"m"}`), "priority", 0)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	if strings.Contains(string(rewritten), `prior\u0069ty`) {
		t.Fatalf("rewritten body kept escaped priority key: %s", rewritten)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != 0 {
		t.Fatalf("priority = %v, want 0", payload["priority"])
	}
}

func TestRewriteJSONPriorityOverwritesDuplicatePriorityValues(t *testing.T) {
	rewritten, err := RewriteJSONPriority([]byte(`{"priority":-100,"model":"m","priority":-50}`), "priority", 0)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	if got, want := string(rewritten), `{"priority":0,"model":"m"}`; got != want {
		t.Fatalf("rewritten = %s, want %s", got, want)
	}
}

func TestRewriteJSONPriorityOverwritesExtraBodyPriority(t *testing.T) {
	rewritten, err := RewriteJSONPriority([]byte(`{"model":"m","extra_body":{"priority":100,"foo":1}}`), "priority", -100)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("top-level priority = %v, want -100", payload["priority"])
	}
	extraBody := payload["extra_body"].(map[string]any)
	if extraBody["priority"].(float64) != -100 {
		t.Fatalf("extra_body.priority = %v, want -100", extraBody["priority"])
	}
	if extraBody["foo"].(float64) != 1 {
		t.Fatalf("extra_body.foo = %v, want 1", extraBody["foo"])
	}
}

func TestRewriteJSONPriorityHandlesSGLangGenerateShape(t *testing.T) {
	body := []byte(`{"text":"请总结这篇文章的内容。","sampling_params":{"max_new_tokens":128},"priority":5}`)
	rewritten, err := RewriteJSONPriority(body, "priority", -100)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	if payload["text"].(string) != "请总结这篇文章的内容。" {
		t.Fatalf("text was not preserved")
	}
	samplingParams := payload["sampling_params"].(map[string]any)
	if samplingParams["max_new_tokens"].(float64) != 128 {
		t.Fatalf("sampling_params.max_new_tokens = %v, want 128", samplingParams["max_new_tokens"])
	}
}

func TestRewriteJSONPriorityIgnoresNestedPriority(t *testing.T) {
	rewritten, err := RewriteJSONPriority([]byte(`{"messages":[{"role":"user","priority":-100,"content":"x"}],"model":"m"}`), "priority", 0)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != 0 {
		t.Fatalf("top-level priority = %v, want 0", payload["priority"])
	}
	messages := payload["messages"].([]any)
	nested := messages[0].(map[string]any)
	if nested["priority"].(float64) != -100 {
		t.Fatalf("nested priority = %v, want -100", nested["priority"])
	}
}

func TestRewriteJSONBodyStripsEmptyToolCallsAndInjectsPriority(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"assistant","content":"ok","tool_calls":[]}],"priority":100}`)
	rewritten, err := RewriteJSONBodySize(body, JSONRewriteOptions{
		InjectPriority:      true,
		PriorityStrategy:    PriorityRewriteStrategyFieldScan,
		PriorityField:       "priority",
		PriorityValue:       -100,
		StripEmptyToolCalls: true,
	}, priorityStreamBufferSize)
	if err != nil {
		t.Fatalf("RewriteJSONBodySize returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	message := payload["messages"].([]any)[0].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("empty tool_calls was not removed: %s", rewritten)
	}
}

func TestRewriteJSONBodyPreservesNonEmptyToolCalls(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[{"id":"1"}]}]}`)
	rewritten, err := RewriteJSONBodySize(body, JSONRewriteOptions{
		StripEmptyToolCalls: true,
	}, priorityStreamBufferSize)
	if err != nil {
		t.Fatalf("RewriteJSONBodySize returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	message := payload["messages"].([]any)[0].(map[string]any)
	toolCalls := message["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("tool_calls len=%d want 1; body=%s", len(toolCalls), rewritten)
	}
}

func TestRewriteJSONBodyStripsEscapedToolCallsKey(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","tool\u005fcalls":[]}],"model":"m"}`)
	rewritten, err := RewriteJSONBodySize(body, JSONRewriteOptions{
		StripEmptyToolCalls: true,
	}, priorityStreamBufferSize)
	if err != nil {
		t.Fatalf("RewriteJSONBodySize returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	message := payload["messages"].([]any)[0].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("escaped empty tool_calls was not removed: %s", rewritten)
	}
}

func TestRewriteJSONBodyAppendLastCanStripEmptyToolCalls(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","tool_calls":[]}],"model":"m"}`)
	rewritten, err := RewriteJSONBodySize(body, JSONRewriteOptions{
		InjectPriority:      true,
		PriorityStrategy:    PriorityRewriteStrategyAppendLast,
		PriorityField:       "priority",
		PriorityValue:       -100,
		StripEmptyToolCalls: true,
	}, priorityStreamBufferSize)
	if err != nil {
		t.Fatalf("RewriteJSONBodySize returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	message := payload["messages"].([]any)[0].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("empty tool_calls was not removed with append_last: %s", rewritten)
	}
}

func TestRewriteJSONBodyStripsOnlyDirectEmptyMessageToolCalls(t *testing.T) {
	body := []byte(`{"tool_calls":[],"messages":[{"role":"assistant","tool_calls":[],"metadata":{"tool_calls":[]},"content":"ok"},{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{}"}}]},null,"literal",42,["nested"],{"role":"user","content":[{"type":"text","text":"hello"}]}],"priority":100}`)
	rewritten, err := RewriteJSONBodySize(body, JSONRewriteOptions{
		InjectPriority:      true,
		PriorityStrategy:    PriorityRewriteStrategyFieldScan,
		PriorityField:       "priority",
		PriorityValue:       -100,
		StripEmptyToolCalls: true,
	}, priorityStreamBufferSize)
	if err != nil {
		t.Fatalf("RewriteJSONBodySize returned error: %v", err)
	}
	if !json.Valid(rewritten) {
		t.Fatalf("rewritten body is invalid JSON: %s", rewritten)
	}

	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	topLevelToolCalls := payload["tool_calls"].([]any)
	if len(topLevelToolCalls) != 0 {
		t.Fatalf("top-level tool_calls len=%d want 0", len(topLevelToolCalls))
	}

	messages := payload["messages"].([]any)
	if len(messages) != 7 {
		t.Fatalf("messages len=%d want 7", len(messages))
	}
	firstMessage := messages[0].(map[string]any)
	if _, ok := firstMessage["tool_calls"]; ok {
		t.Fatalf("direct empty message tool_calls was not removed: %s", rewritten)
	}
	metadata := firstMessage["metadata"].(map[string]any)
	if nested := metadata["tool_calls"].([]any); len(nested) != 0 {
		t.Fatalf("nested metadata.tool_calls len=%d want 0", len(nested))
	}

	secondMessage := messages[1].(map[string]any)
	toolCalls := secondMessage["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("non-empty message tool_calls len=%d want 1", len(toolCalls))
	}
	if messages[2] != nil {
		t.Fatalf("messages[2] = %v, want nil", messages[2])
	}
	if messages[3].(string) != "literal" {
		t.Fatalf("messages[3] = %v, want literal", messages[3])
	}
	if messages[4].(float64) != 42 {
		t.Fatalf("messages[4] = %v, want 42", messages[4])
	}
	if len(messages[5].([]any)) != 1 {
		t.Fatalf("messages[5] was not preserved: %v", messages[5])
	}
}

func TestRewriteJSONBodyStripsDuplicateEmptyMessageToolCalls(t *testing.T) {
	body := []byte(`{"messages":[{"tool_calls":[],"role":"assistant","tool_calls":[{"id":"call_1"}]},{"tool_calls":[{"id":"call_2"}],"tool_calls":[],"content":"ok"},{"tool_calls":[],"tool_calls":[]}]} `)
	rewritten, err := RewriteJSONBodySize(body, JSONRewriteOptions{
		StripEmptyToolCalls: true,
	}, priorityStreamBufferSize)
	if err != nil {
		t.Fatalf("RewriteJSONBodySize returned error: %v", err)
	}
	if !json.Valid(rewritten) {
		t.Fatalf("rewritten body is invalid JSON: %s", rewritten)
	}

	payload := decodePriorityPayload(t, rewritten)
	messages := payload["messages"].([]any)
	firstMessage := messages[0].(map[string]any)
	if toolCalls := firstMessage["tool_calls"].([]any); len(toolCalls) != 1 {
		t.Fatalf("first message non-empty tool_calls len=%d want 1; body=%s", len(toolCalls), rewritten)
	}
	secondMessage := messages[1].(map[string]any)
	if toolCalls := secondMessage["tool_calls"].([]any); len(toolCalls) != 1 {
		t.Fatalf("second message non-empty tool_calls len=%d want 1; body=%s", len(toolCalls), rewritten)
	}
	thirdMessage := messages[2].(map[string]any)
	if _, ok := thirdMessage["tool_calls"]; ok {
		t.Fatalf("all-empty duplicate tool_calls should be removed: %s", rewritten)
	}
}

func TestRewriteJSONBodyHandlesEmptyAndScalarMessages(t *testing.T) {
	body := []byte(`{"messages":[],"metadata":{"messages":[{"tool_calls":[]}]}}`)
	rewritten, err := RewriteJSONBodySize(body, JSONRewriteOptions{
		StripEmptyToolCalls: true,
	}, priorityStreamBufferSize)
	if err != nil {
		t.Fatalf("RewriteJSONBodySize returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	messages := payload["messages"].([]any)
	if len(messages) != 0 {
		t.Fatalf("messages len=%d want 0", len(messages))
	}
	nestedMessages := payload["metadata"].(map[string]any)["messages"].([]any)
	nestedMessage := nestedMessages[0].(map[string]any)
	if nested := nestedMessage["tool_calls"].([]any); len(nested) != 0 {
		t.Fatalf("nested metadata.messages[0].tool_calls len=%d want 0", len(nested))
	}
}

func TestRewriteJSONPriorityHandlesStringsWithDelimiters(t *testing.T) {
	rewritten, err := RewriteJSONPriority([]byte(`{"model":"m","messages":[{"role":"user","content":"quoted \" } ] , text"}]}`), "priority", -100)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
}

func TestRewriteJSONPriorityStreamsLargeMessagesContent(t *testing.T) {
	contentLiteral := strings.Repeat("x", priorityStreamBufferSize*2) + ` quoted \" } ] , tail`
	rewritten, err := RewriteJSONPriority([]byte(`{"model":"m","messages":[{"role":"user","content":"`+contentLiteral+`"}],"priority":5}`), "priority", -100)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	messages := payload["messages"].([]any)
	message := messages[0].(map[string]any)
	if got, want := message["content"].(string), strings.ReplaceAll(contentLiteral, `\"`, `"`); got != want {
		t.Fatalf("content was not preserved: len got %d want %d", len(got), len(want))
	}
}

func TestRewriteJSONPriorityHandlesTopLevelPromptString(t *testing.T) {
	prompt := strings.Repeat("x", 256*1024) + ` quoted \" } ] , tail`
	rewritten, err := RewriteJSONPriority([]byte(`{"model":"m","prompt":"`+prompt+`"}`), "priority", -100)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	if payload["prompt"].(string) != strings.ReplaceAll(prompt, `\"`, `"`) {
		t.Fatalf("prompt was not preserved")
	}
}

func TestAppendLastJSONPriorityLeavesDuplicateLastWins(t *testing.T) {
	rewritten := appendLastPriorityBody(t, `{"priority":100,"model":"m"}`, "priority", -100)
	if got, want := string(rewritten), `{"priority":100,"model":"m","priority":-100}`; got != want {
		t.Fatalf("rewritten = %s, want %s", got, want)
	}
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("last priority = %v, want -100", payload["priority"])
	}
}

func TestAppendLastJSONPriorityHandlesEmptyObject(t *testing.T) {
	rewritten := appendLastPriorityBody(t, `{}`, "priority", -100)
	if got, want := string(rewritten), `{"priority":-100}`; got != want {
		t.Fatalf("rewritten = %s, want %s", got, want)
	}
}

func TestAppendLastJSONPriorityIgnoresNestedDelimiters(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"quoted \" } ] , text"}],"priority":100}`
	rewritten := appendLastPriorityBody(t, body, "priority", -100)
	payload := decodePriorityPayload(t, rewritten)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("last priority = %v, want -100", payload["priority"])
	}
}

func TestRewriteJSONPriorityAddsToEmptyObject(t *testing.T) {
	rewritten, err := RewriteJSONPriority([]byte(`{}`), "priority", -100)
	if err != nil {
		t.Fatalf("RewriteJSONPriority returned error: %v", err)
	}
	if got, want := string(rewritten), `{"priority":-100}`; got != want {
		t.Fatalf("rewritten = %s, want %s", got, want)
	}
}

func appendLastPriorityBody(t *testing.T, body, field string, value int) []byte {
	t.Helper()
	rewritten, err := NewAppendLastJSONPriorityRewrite(io.NopCloser(strings.NewReader(body)), field, value)
	if err != nil {
		t.Fatalf("NewAppendLastJSONPriorityRewrite returned error: %v", err)
	}
	defer rewritten.Close()
	out, err := io.ReadAll(rewritten)
	if err != nil {
		t.Fatalf("ReadAll append_last returned error: %v", err)
	}
	return out
}

func TestRewriteJSONPriorityRejectsNonObject(t *testing.T) {
	if _, err := RewriteJSONPriority([]byte(`[]`), "priority", 0); err == nil {
		t.Fatalf("RewriteJSONPriority accepted array body")
	}
	if _, err := RewriteJSONPriority([]byte(`null`), "priority", 0); !errors.Is(err, ErrPriorityBodyNotObject) {
		t.Fatalf("RewriteJSONPriority(null) error = %v, want ErrPriorityBodyNotObject", err)
	}
}

func TestShouldInjectBackendPriorityModes(t *testing.T) {
	if !ShouldInjectBackendPriority(PriorityModePremiumOnly, Premium) {
		t.Fatalf("premium_only should inject premium")
	}
	if ShouldInjectBackendPriority(PriorityModePremiumOnly, Basic) {
		t.Fatalf("premium_only should skip basic")
	}
	if !ShouldInjectBackendPriority(PriorityModeAll, Basic) {
		t.Fatalf("all should inject basic")
	}
	if !ShouldInjectBackendPriority("", Basic) {
		t.Fatalf("unknown mode should default to all")
	}
}

func decodePriorityPayload(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	return payload
}
