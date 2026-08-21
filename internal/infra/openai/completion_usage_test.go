package openai

import (
	"errors"
	"fmt"
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

func TestCompletionUsageObserverNotifiesTerminalAtDoneAfterUsageOnce(t *testing.T) {
	payload := "data: {\"choices\":[],\"usage\":{\"completion_tokens\":5},\"metrics\":{\"mean_itl_ms\":20}}\n\n" +
		"data: [DONE]\n\n"
	var order []string
	observer := NewCompletionUsageObserverWithTerminal(true, func(CompletionUsage) {
		order = append(order, "usage")
	}, func() {
		order = append(order, "terminal")
	})
	observer.Observe([]byte(payload))
	observer.Finish()
	observer.Observe([]byte(payload))

	if got := strings.Join(order, ","); got != "terminal,usage" {
		t.Fatalf("stream completion callback order = %q, want terminal,usage exactly once", got)
	}
}

func TestCompletionUsageObserverWithoutDoneDoesNotNotifyTerminalUntilEOF(t *testing.T) {
	payload := "data: {\"choices\":[],\"usage\":{\"completion_tokens\":5},\"metrics\":{\"mean_itl_ms\":20}}\n\n"
	usageCalls := 0
	terminalCalls := 0
	observer := NewCompletionUsageObserverWithTerminal(true, func(CompletionUsage) {
		usageCalls++
	}, func() {
		terminalCalls++
	})
	observer.Observe([]byte(payload))
	if terminalCalls != 0 || observer.TerminalSeen() {
		t.Fatalf("unterminated SSE released early: terminal=%d seen=%t", terminalCalls, observer.TerminalSeen())
	}
	observer.Finish()
	if terminalCalls != 1 || usageCalls != 1 || observer.TerminalSeen() {
		t.Fatalf("EOF-only SSE completion = terminal %d usage %d explicit-seen %t", terminalCalls, usageCalls, observer.TerminalSeen())
	}
}

func TestCompletionUsageBodyNonStreamNotifiesTerminalBeforeLastReadReturns(t *testing.T) {
	payload := `{"choices":[{}],"usage":{"completion_tokens":9},"metrics":{"mean_itl_ms":12.5}}`
	terminal := false
	body := ObserveCompletionUsageBodyWithTerminal(io.NopCloser(strings.NewReader(payload)), false, func(usage CompletionUsage) {
		if usage.CompletionTokens != 9 {
			t.Fatalf("completion tokens = %d, want 9", usage.CompletionTokens)
		}
	}, func() {
		terminal = true
	})
	buffer := make([]byte, len(payload))
	n, err := body.Read(buffer)
	if err != nil || n != len(payload) || string(buffer[:n]) != payload {
		t.Fatalf("first observed read = %d/%v/%q", n, err, buffer[:n])
	}
	if !terminal {
		t.Fatal("non-stream upstream terminal was not observed before the last body bytes returned")
	}
}

func TestCompletionUsageBodyKnownLengthNotifiesTerminalWithoutLookahead(t *testing.T) {
	payload := `{"choices":[{}],"usage":{"completion_tokens":9},"metrics":{"mean_itl_ms":12.5}}`
	terminal := false
	body := ObserveCompletionUsageBodyWithTerminalLength(io.NopCloser(strings.NewReader(payload)), false, int64(len(payload)), nil, func() {
		terminal = true
	})
	observed, ok := body.(*completionUsageBody)
	if !ok {
		t.Fatalf("known-length observer body type = %T", body)
	}
	if observed.lookahead != nil || !observed.hasExpectedLength {
		t.Fatalf("known-length body unexpectedly allocated lookahead: %+v", observed)
	}
	buffer := make([]byte, len(payload))
	n, err := body.Read(buffer)
	if err != nil || n != len(payload) || string(buffer[:n]) != payload {
		t.Fatalf("known-length read = %d/%v/%q", n, err, buffer[:n])
	}
	if !terminal {
		t.Fatal("known-length terminal was not notified before the last body bytes returned")
	}
}

func TestCompletionUsageBodyKnownLengthTruncatedEOFDoesNotNotifyTerminal(t *testing.T) {
	payload := `{"choices":[{}],"usage":{"completion_tokens":9}}`
	terminalCalls := 0
	body := ObserveCompletionUsageBodyWithTerminalLength(io.NopCloser(strings.NewReader(payload)), false, int64(len(payload)+7), nil, func() {
		terminalCalls++
	})
	got, err := io.ReadAll(body)
	if err != nil || string(got) != payload {
		t.Fatalf("known-length truncated read = %q/%v", got, err)
	}
	if terminalCalls != 0 {
		t.Fatalf("known-length truncated EOF notified terminal %d times", terminalCalls)
	}
}

func TestCompletionUsageBodyKnownLengthOverrunDoesNotNotifyTerminal(t *testing.T) {
	payload := `{"choices":[{}],"usage":{"completion_tokens":9}}`
	terminalCalls := 0
	body := ObserveCompletionUsageBodyWithTerminalLength(io.NopCloser(strings.NewReader(payload)), false, int64(len(payload)-1), nil, func() {
		terminalCalls++
	})
	got, err := io.ReadAll(body)
	if err != nil || string(got) != payload {
		t.Fatalf("known-length overrun read = %q/%v", got, err)
	}
	if terminalCalls != 0 {
		t.Fatalf("known-length overrun notified terminal %d times", terminalCalls)
	}
}

func TestCompletionUsageBodyUnexpectedEOFDoesNotNotifyTerminal(t *testing.T) {
	payload := `{"choices":[{}],"usage":{"completion_tokens":9}}`
	terminalCalls := 0
	body := ObserveCompletionUsageBodyWithTerminal(&errorReadCloser{
		payload: []byte(payload),
		err:     io.ErrUnexpectedEOF,
	}, false, nil, func() {
		terminalCalls++
	})
	got, err := io.ReadAll(body)
	if !errors.Is(err, io.ErrUnexpectedEOF) || string(got) != payload {
		t.Fatalf("unexpected-EOF read = %q/%v", got, err)
	}
	if terminalCalls != 0 {
		t.Fatalf("unexpected EOF notified terminal %d times", terminalCalls)
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

func TestCompletionUsageObserverIncludesPromptTokens(t *testing.T) {
	payload := `{"choices":[{"index":0}],"usage":{"prompt_tokens":17,"completion_tokens":5}}`
	var observed []CompletionUsage
	body := ObserveCompletionUsageBody(io.NopCloser(strings.NewReader(payload)), false, func(usage CompletionUsage) {
		observed = append(observed, usage)
	})
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read observed body: %v", err)
	}
	if len(observed) != 1 || observed[0].PromptTokens != 17 || observed[0].CompletionTokens != 5 {
		t.Fatalf("prompt/completion usage = %+v", observed)
	}
}

func TestCompletionUsageEvidenceClassifiesBoundedBodiesWithoutChangingBytes(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		outcome CompletionUsageEvidenceOutcome
		tokens  int64
	}{
		{
			name:    "available",
			payload: `{"choices":[{}],"usage":{"completion_tokens":50}}`,
			outcome: CompletionUsageAvailable,
			tokens:  50,
		},
		{
			name:    "unavailable",
			payload: `{"choices":[{}]}`,
			outcome: CompletionUsageUnavailable,
		},
		{
			name:    "malformed",
			payload: `{"choices":[{}],"usage":{"completion_tokens":"bad"}}`,
			outcome: CompletionUsageMalformed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var observed []CompletionUsageEvidence
			body := ObserveCompletionUsageEvidenceBody(
				io.NopCloser(strings.NewReader(test.payload)),
				false,
				func(evidence CompletionUsageEvidence) { observed = append(observed, evidence) },
			)
			got, err := io.ReadAll(body)
			if err != nil || string(got) != test.payload {
				t.Fatalf("observed body=%q err=%v", got, err)
			}
			if len(observed) != 1 || observed[0].Outcome != test.outcome ||
				observed[0].Usage.CompletionTokens != test.tokens {
				t.Fatalf("evidence=%+v want outcome=%d tokens=%d", observed, test.outcome, test.tokens)
			}
		})
	}
}

func TestCompletionUsageEvidenceParsesResponsesAPIJSONAndSSE(t *testing.T) {
	tests := []struct {
		name      string
		streaming bool
		payload   string
	}{
		{
			name: "json",
			payload: `{"object":"response","usage":{` +
				`"input_tokens":10,"output_tokens":200,"total_tokens":210}}`,
		},
		{
			name:      "sse",
			streaming: true,
			payload: "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":10,\"output_tokens\":200}}}\n\n" +
				"data: [DONE]\n\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var observed []CompletionUsageEvidence
			body := ObserveCompletionUsageEvidenceBodyForFormat(
				io.NopCloser(strings.NewReader(test.payload)),
				test.streaming,
				CompletionUsageFormatResponses,
				func(evidence CompletionUsageEvidence) { observed = append(observed, evidence) },
			)
			got, err := io.ReadAll(body)
			if err != nil || string(got) != test.payload {
				t.Fatalf("Responses observed body=%q err=%v", got, err)
			}
			if len(observed) != 1 || observed[0].Outcome != CompletionUsageAvailable ||
				observed[0].Usage.PromptTokens != 10 || observed[0].Usage.CompletionTokens != 200 {
				t.Fatalf("Responses evidence=%+v", observed)
			}
		})
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

func BenchmarkCompletionUsageObserverStreamingTerminalHotPath(b *testing.B) {
	var response strings.Builder
	for index := 0; index < 64; index++ {
		response.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"token\"}}]}\n\n")
	}
	response.WriteString("data: {\"choices\":[],\"usage\":{\"completion_tokens\":65},\"metrics\":{\"mean_itl_ms\":20}}\n\ndata: [DONE]\n\n")
	payload := []byte(response.String())
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for b.Loop() {
		usageCalls := 0
		terminalCalls := 0
		observer := NewCompletionUsageObserverWithTerminal(true, func(CompletionUsage) { usageCalls++ }, func() { terminalCalls++ })
		observer.Observe(payload)
		observer.Finish()
		if usageCalls != 1 || terminalCalls != 1 {
			b.Fatalf("completion/terminal callbacks = %d/%d", usageCalls, terminalCalls)
		}
	}
}

func BenchmarkCompletionUsageBodyNonStreamLookahead(b *testing.B) {
	for _, size := range []int{2 * 1024, 64 * 1024} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			prefix := `{"choices":[{}],"usage":{"completion_tokens":9},"padding":"`
			suffix := `"}`
			padding := size - len(prefix) - len(suffix)
			if padding < 0 {
				b.Fatal("benchmark payload size is too small")
			}
			payload := prefix + strings.Repeat("x", padding) + suffix
			buffer := make([]byte, 32*1024)
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for b.Loop() {
				usageCalls := 0
				terminalCalls := 0
				body := ObserveCompletionUsageBodyWithTerminal(io.NopCloser(strings.NewReader(payload)), false, func(CompletionUsage) { usageCalls++ }, func() { terminalCalls++ })
				for {
					_, err := body.Read(buffer)
					if err == io.EOF {
						break
					}
					if err != nil {
						b.Fatal(err)
					}
				}
				if usageCalls != 1 || terminalCalls != 1 {
					b.Fatalf("completion/terminal callbacks = %d/%d", usageCalls, terminalCalls)
				}
			}
		})
	}
}

func BenchmarkCompletionUsageBodyNonStreamKnownLength(b *testing.B) {
	for _, size := range []int{2 * 1024, 64 * 1024} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			prefix := `{"choices":[{}],"usage":{"completion_tokens":9},"padding":"`
			suffix := `"}`
			padding := size - len(prefix) - len(suffix)
			if padding < 0 {
				b.Fatal("benchmark payload size is too small")
			}
			payload := prefix + strings.Repeat("x", padding) + suffix
			buffer := make([]byte, 32*1024)
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for b.Loop() {
				usageCalls := 0
				terminalCalls := 0
				body := ObserveCompletionUsageBodyWithTerminalLength(io.NopCloser(strings.NewReader(payload)), false, int64(len(payload)), func(CompletionUsage) { usageCalls++ }, func() { terminalCalls++ })
				for {
					_, err := body.Read(buffer)
					if err == io.EOF {
						break
					}
					if err != nil {
						b.Fatal(err)
					}
				}
				if usageCalls != 1 || terminalCalls != 1 {
					b.Fatalf("completion/terminal callbacks = %d/%d", usageCalls, terminalCalls)
				}
			}
		})
	}
}

func BenchmarkCompletionUsageEvidenceNonStream(b *testing.B) {
	for _, size := range []int{2 * 1024, 64 * 1024, 1024 * 1024, 4 * 1024 * 1024} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			prefix := `{"choices":[{}],"usage":{"completion_tokens":9},"padding":"`
			suffix := `"}`
			padding := size - len(prefix) - len(suffix)
			if padding < 0 {
				b.Fatal("benchmark payload size is too small")
			}
			payload := prefix + strings.Repeat("x", padding) + suffix
			b.ReportAllocs()
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for b.Loop() {
				calls := 0
				body := ObserveCompletionUsageEvidenceBody(
					io.NopCloser(strings.NewReader(payload)),
					false,
					func(evidence CompletionUsageEvidence) {
						if evidence.Outcome != CompletionUsageAvailable {
							b.Fatalf("completion usage outcome = %d", evidence.Outcome)
						}
						calls++
					},
				)
				_, _ = io.Copy(io.Discard, body)
				if calls != 1 {
					b.Fatalf("completion evidence callbacks = %d", calls)
				}
			}
		})
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

type errorReadCloser struct {
	payload []byte
	err     error
	done    bool
}

func (r *errorReadCloser) Read(buffer []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(buffer, r.payload), r.err
}

func (*errorReadCloser) Close() error { return nil }
