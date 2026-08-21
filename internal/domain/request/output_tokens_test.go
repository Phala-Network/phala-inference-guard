package request

import (
	"bytes"
	"strings"
	"testing"
)

var (
	benchmarkJSONFields         JSONFields
	benchmarkEndpointJSONFields EndpointJSONFields
	benchmarkJSONFieldsOK       bool
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

func TestV01218ParseJSONFieldsPreservesTopLevelStreamingEvidenceWithoutChangingShape(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		present   bool
		known     bool
		streaming bool
	}{
		{name: "true", body: `{"stream":true,"prompt":"x"}`, present: true, known: true, streaming: true},
		{name: "false", body: `{"stream":false,"prompt":"x"}`, present: true, known: true},
		{name: "unspecified", body: `{"prompt":"x"}`},
		{name: "invalid type", body: `{"stream":"yes","prompt":"x"}`, present: true},
		{name: "nested is not root", body: `{"messages":[{"stream":true}]}`},
		{name: "same duplicate", body: `{"stream":true,"stream":true}`, present: true, known: true, streaming: true},
		{name: "conflicting duplicate", body: `{"stream":true,"stream":false}`, present: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fields, valid := ParseJSONFields([]byte(test.body), nil)
			if !valid || !fields.ShapeSupported || fields.DecodeSequences != 1 ||
				fields.StreamingPresent != test.present || fields.StreamingKnown != test.known ||
				fields.Streaming != test.streaming {
				t.Fatalf("streaming evidence=%+v valid=%t", fields, valid)
			}
		})
	}
}

func TestV01215ParseJSONFieldsDerivesDecodeMultiplicityAndExplicitPromptTokens(t *testing.T) {
	tests := []struct {
		name           string
		body           string
		batch          int64
		stringBytes    int64
		maximumString  int64
		explicitTokens int64
		maximumTokens  int64
		sequences      int64
		supported      bool
	}{
		{name: "chat n", body: `{"messages":[],"n":8}`, batch: 1, sequences: 8, supported: true},
		{name: "string batch ignores best of", body: `{"prompt":["a","b","c"],"n":2,"best_of":4}`, batch: 3, stringBytes: 3, maximumString: 1, sequences: 6, supported: true},
		{name: "flat token ids", body: `{"prompt":[1,2,3],"n":2}`, batch: 1, explicitTokens: 3, maximumTokens: 3, sequences: 2, supported: true},
		{name: "token id batch", body: `{"prompt":[[1,2,3],[4,5]],"n":2}`, batch: 2, explicitTokens: 5, maximumTokens: 3, sequences: 4, supported: true},
		{name: "escaped multiplicity key", body: `{"prompt":"x","\u006e":3}`, batch: 1, stringBytes: 1, maximumString: 1, sequences: 3, supported: true},
		{name: "mixed prompt", body: `{"prompt":["a",2]}`, batch: 1, sequences: 0, supported: false},
		{name: "empty prompt batch", body: `{"prompt":[]}`, batch: 1, sequences: 0, supported: false},
		{name: "zero n", body: `{"prompt":"x","n":0}`, batch: 1, sequences: 0, supported: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := ParseJSONFields([]byte(test.body), nil)
			if !ok || got.PromptBatchSize != test.batch ||
				got.PromptStringBytes != test.stringBytes ||
				got.MaximumPromptStringBytes != test.maximumString ||
				got.ExplicitPromptTokens != test.explicitTokens ||
				got.MaximumExplicitPromptTokens != test.maximumTokens ||
				got.DecodeSequences != test.sequences || got.ShapeSupported != test.supported {
				t.Fatalf("ParseJSONFields(%s)=%+v/%t", test.body, got, ok)
			}
		})
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

func TestV01218EndpointParserEscapedKeysDoNotAllocateCompleteStrings(t *testing.T) {
	body := []byte(`{"\u0061` + strings.Repeat("a", 64*1024) +
		`":0,"\u006dessages":[{"role":"user","content":"hello"}],"\u006e":2,"max_tokens":8}`)
	fields := []string{"max_tokens"}
	allocations := testing.AllocsPerRun(10, func() {
		benchmarkEndpointJSONFields, benchmarkJSONFieldsOK = ParseEndpointJSONFields(
			body,
			fields,
			EndpointChatCompletions,
		)
	})
	if !benchmarkJSONFieldsOK || !benchmarkEndpointJSONFields.ShapeSupported ||
		benchmarkEndpointJSONFields.DecodeSequences != 2 {
		t.Fatalf("escaped endpoint fields=%+v/%t", benchmarkEndpointJSONFields, benchmarkJSONFieldsOK)
	}
	if allocations > 1 {
		t.Fatalf("escaped endpoint keys allocations=%.1f, want <=1", allocations)
	}
}

func TestV01218EndpointParserEscapedOutputLimitDoesNotAllocateCompleteString(t *testing.T) {
	body := []byte(`{"messages":[],"max_tokens":"\u0032\u0035"}`)
	fields := []string{"max_tokens"}
	allocations := testing.AllocsPerRun(10, func() {
		benchmarkEndpointJSONFields, benchmarkJSONFieldsOK = ParseEndpointJSONFields(
			body,
			fields,
			EndpointChatCompletions,
		)
	})
	if !benchmarkJSONFieldsOK || !benchmarkEndpointJSONFields.ShapeSupported ||
		!benchmarkEndpointJSONFields.HasOutputTokens || benchmarkEndpointJSONFields.OutputTokens != 25 {
		t.Fatalf("escaped output-limit fields=%+v/%t", benchmarkEndpointJSONFields, benchmarkJSONFieldsOK)
	}
	if allocations != 0 {
		t.Fatalf("escaped output-limit allocations=%.1f want 0", allocations)
	}
}

func TestV01218ParseJSONOutputTokensHandlesEscapedDecimalsStrictly(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  int
		valid bool
	}{
		{name: "number", value: `25`, want: 25, valid: true},
		{name: "quoted number", value: `"25"`, want: 25, valid: true},
		{name: "escaped digits", value: `"\u0032\u0035"`, want: 25, valid: true},
		{name: "escaped ascii whitespace", value: `"\t \u0032\u0035\f\r\n"`, want: 25, valid: true},
		{name: "digit after trailing whitespace", value: `"2\u00205"`},
		{name: "non digit", value: `"\u0032x"`},
		{name: "negative number", value: `-1`},
		{name: "quoted negative number", value: `"-1"`},
		{name: "non ascii escape", value: `"\u00a025"`},
		{name: "unterminated unicode escape", value: `"\u003"`},
		{name: "unquoted overflow", value: strings.Repeat("9", 128)},
		{name: "escaped overflow", value: `"` + strings.Repeat(`\u0039`, 128) + `"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, valid := parseJSONOutputTokens([]byte(test.value))
			if got != test.want || valid != test.valid {
				t.Fatalf("parseJSONOutputTokens(%q)=%d/%t, want %d/%t", test.value, got, valid, test.want, test.valid)
			}
		})
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
