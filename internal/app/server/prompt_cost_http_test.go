package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPromptMirrorWaitsForQualifiedResponseUsage(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		body       string
		wantCached bool
	}{
		{name: "qualified success", status: http.StatusOK, body: `{"choices":[{}],"usage":{"prompt_tokens":1024,"completion_tokens":1,"prompt_tokens_details":{"cached_tokens":0}}}`, wantCached: true},
		{name: "missing usage", status: http.StatusOK, body: `{"choices":[{}]}`},
		{name: "backend error", status: http.StatusBadRequest, body: `{"error":{"message":"invalid request"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			var srv *proxyServer
			var before atomic.Int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				before.Store(int64(srv.promptCost.Snapshot().indexBlocks))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer backend.Close()
			runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{})
			srv = newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
			srv.promptCost = newPromptCostEstimator(1 << 20)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
				`{"messages":[{"role":"user","content":"`+strings.Repeat("x", 4096)+`"}]}`,
			))
			request.Header.Set("Authorization", "Bearer secret")
			request.Header.Set("Content-Type", "application/json")
			srv.ServeHTTP(httptest.NewRecorder(), request)
			if before.Load() != 0 {
				t.Fatalf("in-flight Prompt already retained %d prefixes", before.Load())
			}
			if got := srv.promptCost.Snapshot().indexBlocks > 0; got != test.wantCached {
				t.Fatalf("completed Prompt retained=%t want %t", got, test.wantCached)
			}
		})
	}
}
