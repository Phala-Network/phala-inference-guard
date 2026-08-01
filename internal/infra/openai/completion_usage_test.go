package openai

import (
	"io"
	"strings"
	"testing"
	"time"
)

func TestCompletionUsageObserverParsesFragmentedFinalSSEUsageOnce(t *testing.T) {
	response := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}],\"usage\":{\"completion_tokens\":1}}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"completion_tokens\":5},\"metrics\":{\"mean_itl_ms\":20,\"generation_time_ms\":80}}\n\n" +
		"data: [DONE]\n\n"
	var observed []CompletionUsage
	body := ObserveCompletionUsageBody(&chunkedReadCloser{chunks: []string{response[:17], response[17:79], response[79:]}}, true, func(usage CompletionUsage) {
		observed = append(observed, usage)
	})
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read observed body: %v", err)
	}
	if len(observed) != 1 || observed[0].CompletionTokens != 5 || observed[0].MeanITL != 20*time.Millisecond || observed[0].GenerationTime != 80*time.Millisecond {
		t.Fatalf("observed usage = %+v", observed)
	}
}

func TestCompletionUsageObserverParsesBoundedNonStreamJSONAtEOF(t *testing.T) {
	payload := `{"id":"x","object":"chat.completion","model":"m","choices":[{"message":{"content":"ok"}}],"usage":{"completion_tokens":9},"metrics":{"mean_itl_ms":12.5,"generation_time_ms":100},"system_fingerprint":"fp"}`
	var observed []CompletionUsage
	body := ObserveCompletionUsageBody(io.NopCloser(strings.NewReader(payload)), false, func(usage CompletionUsage) {
		observed = append(observed, usage)
	})
	got, err := io.ReadAll(body)
	if err != nil || string(got) != payload {
		t.Fatalf("read body = %q/%v", got, err)
	}
	if len(observed) != 1 || observed[0].CompletionTokens != 9 || observed[0].MeanITL != 12_500*time.Microsecond || observed[0].GenerationTime != 100*time.Millisecond {
		t.Fatalf("observed usage = %+v", observed)
	}
}

func TestCompletionUsageObserverParsesFinalSSEUsageAtEOFWithoutBlankLine(t *testing.T) {
	payload := "data: {\"choices\":[],\"usage\":{\"completion_tokens\":3},\"metrics\":{\"mean_itl_ms\":10}}"
	var observed []CompletionUsage
	body := ObserveCompletionUsageBody(io.NopCloser(strings.NewReader(payload)), true, func(usage CompletionUsage) {
		observed = append(observed, usage)
	})
	got, err := io.ReadAll(body)
	if err != nil || string(got) != payload {
		t.Fatalf("read body = %q/%v", got, err)
	}
	if len(observed) != 1 || observed[0].CompletionTokens != 3 || observed[0].MeanITL != 10*time.Millisecond {
		t.Fatalf("observed usage = %+v", observed)
	}
}

func TestCompletionUsageObserverRejectsDuplicateFinalSSEUsage(t *testing.T) {
	payload := "data: {\"choices\":[],\"usage\":{\"completion_tokens\":3},\"metrics\":{\"mean_itl_ms\":10}}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"completion_tokens\":4},\"metrics\":{\"mean_itl_ms\":11}}\n\n" +
		"data: [DONE]\n\n"
	calls := 0
	body := ObserveCompletionUsageBody(io.NopCloser(strings.NewReader(payload)), true, func(CompletionUsage) { calls++ })
	got, err := io.ReadAll(body)
	if err != nil || string(got) != payload {
		t.Fatalf("read body = %q/%v", got, err)
	}
	if calls != 0 {
		t.Fatalf("duplicate final usage callbacks = %d, want 0", calls)
	}
}

func TestCompletionUsageObserverRejectsContinuousMalformedAndOversizedSignals(t *testing.T) {
	tests := []struct {
		name      string
		streaming bool
		payload   string
	}{
		{name: "continuous usage", streaming: true, payload: "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}],\"usage\":{\"completion_tokens\":5}}\n\n"},
		{name: "null final choices", streaming: true, payload: "data: {\"choices\":null,\"usage\":{\"completion_tokens\":5}}\n\n"},
		{name: "multiple non-stream choices", streaming: false, payload: `{"choices":[{},{}],"usage":{"completion_tokens":5}}`},
		{name: "fractional tokens", streaming: false, payload: `{"choices":[{}],"usage":{"completion_tokens":1.5}}`},
		{name: "invalid timing", streaming: false, payload: `{"choices":[{}],"usage":{"completion_tokens":5},"metrics":{"mean_itl_ms":-1}}`},
		{name: "trailing json", streaming: false, payload: `{"choices":[{}],"usage":{"completion_tokens":5}} {}`},
		{name: "oversized json", streaming: false, payload: strings.Repeat(" ", maximumCompletionUsageJSONBytes+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			body := ObserveCompletionUsageBody(io.NopCloser(strings.NewReader(test.payload)), test.streaming, func(CompletionUsage) { calls++ })
			_, _ = io.Copy(io.Discard, body)
			if calls != 0 {
				t.Fatalf("completion callback calls = %d, want 0", calls)
			}
		})
	}
}

func TestCompletionUsageContentTypeEligibilityIsExact(t *testing.T) {
	for _, test := range []struct {
		contentType string
		streaming   bool
		want        bool
	}{
		{contentType: "text/event-stream; charset=utf-8", streaming: true, want: true},
		{contentType: "application/json", streaming: false, want: true},
		{contentType: "application/problem+json; charset=utf-8", streaming: false, want: true},
		{contentType: "text/plain; note=application/json", streaming: false, want: false},
		{contentType: "application/json", streaming: true, want: false},
		{contentType: "", streaming: false, want: false},
	} {
		if got := CompletionUsageContentTypeEligible(test.contentType, test.streaming); got != test.want {
			t.Fatalf("eligible(%q, %t) = %t, want %t", test.contentType, test.streaming, got, test.want)
		}
	}
}

func BenchmarkCompletionUsageObserverStreaming(b *testing.B) {
	var response strings.Builder
	for index := 0; index < 64; index++ {
		response.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"token\"}}]}\n\n")
	}
	response.WriteString("data: {\"choices\":[],\"usage\":{\"completion_tokens\":65},\"metrics\":{\"mean_itl_ms\":20}}\n\ndata: [DONE]\n\n")
	payload := response.String()
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for index := 0; index < b.N; index++ {
		calls := 0
		body := ObserveCompletionUsageBody(io.NopCloser(strings.NewReader(payload)), true, func(CompletionUsage) { calls++ })
		_, _ = io.Copy(io.Discard, body)
		if calls != 1 {
			b.Fatalf("completion callbacks = %d", calls)
		}
	}
}

type chunkedReadCloser struct {
	chunks []string
}

func (r *chunkedReadCloser) Read(buffer []byte) (int, error) {
	if len(r.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := r.chunks[0]
	r.chunks = r.chunks[1:]
	return copy(buffer, chunk), nil
}

func (*chunkedReadCloser) Close() error { return nil }
