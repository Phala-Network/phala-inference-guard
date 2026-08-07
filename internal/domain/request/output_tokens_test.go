package request

import (
	"bytes"
	"testing"
)

var (
	benchmarkJSONFields   JSONFields
	benchmarkJSONFieldsOK bool
)

func TestParseJSONFieldsExtractsOutputTokens(t *testing.T) {
	result, ok := ParseJSONFields([]byte(`{"messages":[{"stream":false}],"max_tokens":512,"stream":true}`), []string{"max_tokens"})
	if !ok {
		t.Fatal("ParseJSONFields rejected valid JSON")
	}
	if !result.HasOutputTokens || result.OutputTokens != 512 {
		t.Fatalf("output tokens = %d/%t, want 512/true", result.OutputTokens, result.HasOutputTokens)
	}
}

func TestV0121ParseJSONFieldsUsesLargestRecognizedOutputLimit(t *testing.T) {
	fields := []string{"max_tokens", "max_completion_tokens", "max_output_tokens"}
	for _, body := range [][]byte{
		[]byte(`{"max_tokens":1,"max_completion_tokens":128,"max_output_tokens":64}`),
		[]byte(`{"max_output_tokens":64,"max_completion_tokens":128,"max_tokens":1}`),
		[]byte(`{"max_tokens":1,"max_tokens":128}`),
	} {
		result, ok := ParseJSONFields(body, fields)
		if !ok || !result.HasOutputTokens || result.OutputTokens != 128 {
			t.Fatalf("ParseJSONFields(%s)=%+v/%t, want conservative output limit 128", body, result, ok)
		}
	}
}

func TestParseJSONFieldsRejectsMalformedTrailingData(t *testing.T) {
	if _, ok := ParseJSONFields([]byte(`{"stream":true} trailing`), nil); ok {
		t.Fatal("ParseJSONFields accepted trailing data")
	}
}

func TestV0121ParseJSONFieldsPreservesStrictJSONAndEscapedKeys(t *testing.T) {
	valid := []struct {
		body       string
		wantOutput int
	}{
		{body: `{"max_tok\u0065ns":" 25 ","stream":false}`, wantOutput: 25},
		{body: `{"nested":{"array":[true,false,null,{"value":-1.25e+2}]},"max_tokens":7,"stream":true}`, wantOutput: 7},
	}
	for _, test := range valid {
		got, ok := ParseJSONFields([]byte(test.body), []string{"max_tokens"})
		if !ok || !got.HasOutputTokens || got.OutputTokens != test.wantOutput {
			t.Errorf("ParseJSONFields(%s)=%+v/%t, want output=%d", test.body, got, ok, test.wantOutput)
		}
	}

	for _, body := range []string{
		`{"max_tokens":01}`,
		`{"max_tokens":1.}`,
		`{"max_tokens":1e}`,
		`{"max_tokens":"bad\xescape"}`,
		`{"max_tokens":1,}`,
		`{"nested":[1 2]}`,
	} {
		if got, ok := ParseJSONFields([]byte(body), []string{"max_tokens"}); ok {
			t.Errorf("ParseJSONFields accepted malformed JSON %s: %+v", body, got)
		}
	}
}

func TestV0121ParseJSONFieldsHotPathAllocationsAreBounded(t *testing.T) {
	prefix := []byte(`{"messages":[{"role":"user","content":"`)
	suffix := []byte(`"}],"max_tokens":256,"stream":true}`)
	body := make([]byte, 0, 64*1024)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("word "), (cap(body)-len(prefix)-len(suffix))/len("word "))...)
	body = append(body, suffix...)

	allocations := testing.AllocsPerRun(10, func() {
		benchmarkJSONFields, benchmarkJSONFieldsOK = ParseJSONFields(body, []string{"max_tokens"})
	})
	if !benchmarkJSONFieldsOK || benchmarkJSONFields.OutputTokens != 256 {
		t.Fatalf("hot-path parse result=%+v/%t", benchmarkJSONFields, benchmarkJSONFieldsOK)
	}
	if allocations > 2 {
		t.Fatalf("hot-path allocations=%.1f, want <=2", allocations)
	}
}

func BenchmarkV0121ParseJSONFields4MiB(b *testing.B) {
	prefix := []byte(`{"messages":[{"role":"user","content":"`)
	suffix := []byte(`"}],"max_tokens":256,"stream":true}`)
	body := make([]byte, 0, 4*1024*1024)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("word "), (cap(body)-len(prefix)-len(suffix))/len("word "))...)
	body = append(body, suffix...)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		benchmarkJSONFields, benchmarkJSONFieldsOK = ParseJSONFields(body, []string{"max_tokens"})
		if !benchmarkJSONFieldsOK {
			b.Fatal("4 MiB JSON field scan failed")
		}
	}
}
