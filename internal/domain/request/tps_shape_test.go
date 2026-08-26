package request

import (
	"strings"
	"testing"
)

func TestParseTPSRequestShapeExtractsOnlyDecodeDemand(t *testing.T) {
	tests := []struct {
		name       string
		endpoint   EndpointKind
		body       string
		base       int64
		sequences  int64
		streaming  bool
		streamSeen bool
	}{
		{name: "chat", endpoint: EndpointChatCompletions, body: `{"messages":[{"content":"hello"}],"n":4,"max_tokens":1}`, base: 1, sequences: 4},
		{name: "completion string batch", endpoint: EndpointCompletions, body: `{"prompt":["one","two"],"n":2,"best_of":3,"max_tokens":999999}`, base: 2, sequences: 6},
		{name: "completion token prompt", endpoint: EndpointCompletions, body: `{"prompt":[1,2,3],"n":2}`, base: 1, sequences: 2},
		{name: "completion token batch", endpoint: EndpointCompletions, body: `{"prompt":[[1,2],[3]],"n":2}`, base: 2, sequences: 4},
		{name: "responses ignores foreign fanout", endpoint: EndpointResponses, body: `{"input":"hello","n":99,"best_of":100,"stream":true}`, base: 1, sequences: 1, streaming: true, streamSeen: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shape, valid := ParseTPSRequestShape([]byte(test.body), test.endpoint)
			if !valid || !shape.Supported || shape.BasePromptCount != test.base ||
				shape.DecodeSequences != test.sequences || shape.Streaming != test.streaming ||
				shape.StreamingPresent != test.streamSeen {
				t.Fatalf("shape=%+v valid=%t", shape, valid)
			}
		})
	}
}

func TestParseTPSRequestShapeRejectsFanoutAmbiguityAndOverflow(t *testing.T) {
	for _, body := range []string{
		`{"prompt":"hello","n":2,"n":3}`,
		`{"prompt":"hello","best_of":null,"best_of":2}`,
		`{"prompt":"hello","n":0}`,
		`{"prompt":"hello","n":1.5}`,
		`{"prompt":"hello","n":9223372036854775808}`,
		`{"prompt":["one","two"],"n":9223372036854775807}`,
		`{"prompt":["one",[1,2]],"n":2}`,
	} {
		shape, valid := ParseTPSRequestShape([]byte(body), EndpointCompletions)
		if !valid || shape.Supported || shape.DecodeSequences != 0 ||
			shape.UnsupportedReason != "unsupported_request_shape" {
			t.Errorf("ambiguous shape=%+v valid=%t body=%s", shape, valid, body)
		}
	}
	for _, body := range []string{
		`{"prompt":["one","two"],"n":2,"n":2,"best_of":3,"best_of":3}`,
		`{"prompt":"hello","best_of":null}`,
		`{"prompt":"hello","n":" 2 "}`,
	} {
		shape, valid := ParseTPSRequestShape([]byte(body), EndpointCompletions)
		if !valid || !shape.Supported {
			t.Errorf("valid duplicate shape=%+v valid=%t body=%s", shape, valid, body)
		}
	}
}

func TestParseTPSRequestShapeTreatsConflictingStreamAsUnknownEvidence(t *testing.T) {
	tests := []struct {
		body      string
		known     bool
		streaming bool
	}{
		{body: `{"stream":true,"stream":true}`, known: true, streaming: true},
		{body: `{"stream":false,"stream":false}`, known: true},
		{body: `{"stream":true,"stream":false}`},
		{body: `{"stream":"true"}`},
	}
	for _, test := range tests {
		shape, valid := ParseTPSRequestShape([]byte(test.body), EndpointChatCompletions)
		if !valid || !shape.Supported || !shape.StreamingPresent ||
			shape.StreamingKnown != test.known || shape.Streaming != test.streaming {
			t.Errorf("shape=%+v valid=%t body=%s", shape, valid, test.body)
		}
	}
}

func TestParseTPSRequestShapeIsStrictAndHandlesEscapedKeys(t *testing.T) {
	shape, valid := ParseTPSRequestShape(
		[]byte(`{"prompt":["one","two"],"\u006e":2,"str\u0065am":false}`),
		EndpointCompletions,
	)
	if !valid || !shape.Supported || shape.DecodeSequences != 4 ||
		!shape.StreamingPresent || !shape.StreamingKnown || shape.Streaming {
		t.Fatalf("escaped-key shape=%+v valid=%t", shape, valid)
	}
	for _, body := range []string{
		`{"n":2} trailing`,
		`{"n":2,}`,
		`{"n":01}`,
		`[1,2,3]`,
		`{"messages":[}`,
	} {
		if got, ok := ParseTPSRequestShape([]byte(body), EndpointChatCompletions); ok {
			t.Errorf("invalid JSON accepted: shape=%+v body=%s", got, body)
		}
	}
}

func TestParseTPSRequestShapeScansLargeBodyWithoutResourceEstimation(t *testing.T) {
	content := strings.Repeat("word ", 800_000)
	body := `{"messages":[{"role":"user","content":"` + content + `"}],"n":2,"max_tokens":999999}`
	shape, valid := ParseTPSRequestShape([]byte(body), EndpointChatCompletions)
	if !valid || !shape.Supported || shape.DecodeSequences != 2 {
		t.Fatalf("large shape=%+v valid=%t", shape, valid)
	}
}

func BenchmarkParseTPSRequestShape4MiB(b *testing.B) {
	content := strings.Repeat("word ", 800_000)
	body := []byte(`{"messages":[{"role":"user","content":"` + content + `"}],"n":2}`)
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		shape, valid := ParseTPSRequestShape(body, EndpointChatCompletions)
		if !valid || !shape.Supported || shape.DecodeSequences != 2 {
			b.Fatalf("shape=%+v valid=%t", shape, valid)
		}
	}
}
