package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/prefill"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/semantic"
)

func TestClassifyRequestDetectsJSONStreamAndPreservesBody(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.ClassifyOutputTokens = true
	cfg.OutputTokenFields = []string{"max_tokens", "max_completion_tokens", "max_output_tokens"}
	srv, err := newProxyServer(cfg)
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	body := `{"model":"m","stream":true,"max_tokens":128,"messages":[]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	classification := srv.classifyRequest(request)
	if !classification.Streaming {
		t.Fatal("JSON stream=true was not classified as streaming")
	}
	if !classification.HasOutputTokens || classification.OutputTokens != 128 {
		t.Fatalf("output tokens = %d/%t, want 128/true", classification.OutputTokens, classification.HasOutputTokens)
	}
	preserved, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read preserved body: %v", err)
	}
	if string(preserved) != body {
		t.Fatalf("preserved body = %q, want %q", preserved, body)
	}
}

func TestClassifyRequestDetectsJSONStreamWhenOutputClassificationDisabled(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.ClassifyOutputTokens = false
	srv, err := newProxyServer(cfg)
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true,"max_tokens":128}`))
	classification := srv.classifyRequest(request)
	if !classification.Streaming {
		t.Fatal("JSON stream=true depends on output-token classification")
	}
	if classification.HasOutputTokens {
		t.Fatal("output tokens were classified while feature is disabled")
	}
}

func TestTrackActiveRequestMarkDecodeAndDoneAreIdempotent(t *testing.T) {
	srv := &proxyServer{activeRequests: prefill.New()}
	markDecode, done := srv.trackActiveRequest(time.Minute)
	if got := srv.activeRequests.ProtectedCount(time.Now()); got != 1 {
		t.Fatalf("protected count = %d, want 1", got)
	}
	markDecode()
	markDecode()
	done()
	done()
	if got := srv.activeRequests.ProtectedCount(time.Now()); got != 0 {
		t.Fatalf("protected count after semantic decode = %d, want 0", got)
	}
}

func TestStreamingPrefillGraceUsesConfiguredMaximum(t *testing.T) {
	srv := &proxyServer{cfg: config{
		DynamicUserTPSEnabled:  true,
		DynamicUserTPSGraceMin: 2 * time.Second,
		DynamicUserTPSGraceMax: 30 * time.Second,
		DynamicUserTPSGraceBps: 64 * 1024,
		DynamicUserTPSGraceMul: 1,
	}}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	if got := srv.prefillGraceDuration(request, true); got != 30*time.Second {
		t.Fatalf("streaming grace = %s, want 30s", got)
	}
	if got := srv.prefillGraceDuration(request, false); got != 2*time.Second {
		t.Fatalf("non-streaming grace = %s, want 2s", got)
	}
}

func TestCopyResponseBodyMarksDecodeOnFirstSemanticDelta(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	srv, err := newProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	marked := 0
	body := strings.NewReader("data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	err = srv.copyResponseBody(context.Background(), httptest.NewRecorder(), body, true, semantic.New(time.Now()), semanticResponseCallbacks{
		observed: func() { marked++ },
	}, nil)
	if err != nil {
		t.Fatalf("copyResponseBody: %v", err)
	}
	if marked != 1 {
		t.Fatalf("semantic mark count = %d, want 1", marked)
	}
}

func TestCopyResponseBodyCommitsUpstreamSemanticTimeAfterSuccessfulDownstreamWrite(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	srv, err := newProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	release := make(chan struct{})
	writer := &blockingPredictiveResponseWriter{
		header:  make(http.Header),
		blocked: make(chan struct{}),
		release: release,
	}
	started := time.Now().Add(-time.Second)
	observed := make(chan time.Duration, 1)
	done := make(chan error, 1)
	body := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	go func() {
		done <- srv.copyResponseBody(context.Background(), writer, body, true, semantic.New(started), semanticResponseCallbacks{
			delivered: func(ttft time.Duration) { observed <- ttft },
		}, nil)
	}()
	select {
	case <-writer.blocked:
	case <-time.After(time.Second):
		t.Fatal("downstream writer did not block")
	}
	blockedAt := time.Since(started)
	select {
	case ttft := <-observed:
		t.Fatalf("semantic TTFT committed before downstream write succeeded: %s", ttft)
	default:
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("copyResponseBody: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("response copy did not finish after downstream release")
	}
	select {
	case ttft := <-observed:
		if ttft <= 0 || ttft > blockedAt {
			t.Fatalf("semantic TTFT = %s, want upstream observation no later than downstream block at %s", ttft, blockedAt)
		}
	default:
		t.Fatal("successful downstream write did not commit semantic TTFT")
	}
}

func TestCopyResponseBodyMarksUpstreamPrefillBeforeTerminalReleaseAndDefersTTFT(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	srv, err := newProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	release := make(chan struct{})
	writer := &blockingPredictiveResponseWriter{
		header:  make(http.Header),
		blocked: make(chan struct{}),
		release: release,
	}
	order := make([]string, 0, 3)
	completion := openai.NewCompletionUsageObserverWithTerminal(true, nil, func() {
		order = append(order, "terminal")
	})
	body := strings.NewReader("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n")
	done := make(chan error, 1)
	go func() {
		done <- srv.copyResponseBody(context.Background(), writer, body, true, semantic.New(time.Now()), semanticResponseCallbacks{
			observed:  func() { order = append(order, "observed") },
			delivered: func(time.Duration) { order = append(order, "delivered") },
		}, completion)
	}()
	select {
	case <-writer.blocked:
	case <-time.After(time.Second):
		t.Fatal("downstream writer did not block")
	}
	if got := strings.Join(order, ","); got != "observed,terminal" {
		t.Fatalf("pre-write semantic/terminal order = %q, want observed,terminal", got)
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("copyResponseBody: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("response copy did not finish")
	}
	if got := strings.Join(order, ","); got != "observed,terminal,delivered" {
		t.Fatalf("final semantic/terminal order = %q, want observed,terminal,delivered", got)
	}
}
