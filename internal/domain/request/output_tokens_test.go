package request

import "testing"

func TestParseJSONFieldsExtractsOutputTokensAndTopLevelStream(t *testing.T) {
	result, ok := ParseJSONFields([]byte(`{"messages":[{"stream":false}],"max_tokens":512,"stream":true}`), []string{"max_tokens"})
	if !ok {
		t.Fatal("ParseJSONFields rejected valid JSON")
	}
	if !result.HasOutputTokens || result.OutputTokens != 512 {
		t.Fatalf("output tokens = %d/%t, want 512/true", result.OutputTokens, result.HasOutputTokens)
	}
	if !result.HasStream || !result.Stream {
		t.Fatalf("stream = %t/%t, want true/true", result.Stream, result.HasStream)
	}
}

func TestParseJSONFieldsUsesLastValidTopLevelStreamValue(t *testing.T) {
	result, ok := ParseJSONFields([]byte(`{"stream":false,"stream":true}`), nil)
	if !ok || !result.HasStream || !result.Stream {
		t.Fatalf("result = %#v ok=%t, want last stream=true", result, ok)
	}
}

func TestParseJSONFieldsRejectsMalformedTrailingData(t *testing.T) {
	if _, ok := ParseJSONFields([]byte(`{"stream":true} trailing`), nil); ok {
		t.Fatal("ParseJSONFields accepted trailing data")
	}
}

func TestParseOutputTokensCompatibilityWrapper(t *testing.T) {
	tokens, ok := ParseOutputTokens([]byte(`{"max_completion_tokens":"256","stream":true}`), []string{"max_completion_tokens"})
	if !ok || tokens != 256 {
		t.Fatalf("ParseOutputTokens = %d/%t, want 256/true", tokens, ok)
	}
}
