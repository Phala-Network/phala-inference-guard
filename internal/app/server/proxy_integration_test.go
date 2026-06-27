package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/config/pigconfig"
)

func TestTrustedGatewayHeadersAreForwardedWithoutRequestMutation(t *testing.T) {
	var seenBody string
	var seenGatewayTrace string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		seenGatewayTrace = r.Header.Get("X-Gateway-Trace")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer backend.Close()

	srv, err := newProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"plain"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gateway-Trace", "trace-123")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if seenBody != body {
		t.Fatalf("backend body=%s want %s", seenBody, body)
	}
	if seenGatewayTrace != "trace-123" {
		t.Fatalf("gateway trace header = %q, want forwarded trace", seenGatewayTrace)
	}
}

func TestAPIAuthRejectsGenerationWithoutBearer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("backend should not be called")
	}))
	defer backend.Close()
	srv, err := newProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unauthorized body is not json: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Fatalf("missing OpenAI error body: %s", recorder.Body.String())
	}
}

func TestAPIAuthRejectsCompletionAndResponsesWithoutBearer(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv, err := newProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}

	for _, path := range []string{"/v1/completions", "/v1/responses"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"m"}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		srv.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d want 401", path, recorder.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s unauthorized body is not json: %v", path, err)
		}
		if _, ok := body["error"]; !ok {
			t.Fatalf("%s missing OpenAI error body: %s", path, recorder.Body.String())
		}
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls=%d want 0", backendCalls)
	}
}

func TestProtectedPIGRoutesRequireBearerAndDoNotProxy(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv, err := newProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}

	for _, path := range []string{"/pig/metrics", "/v1/metrics", "/v1/attestation/report"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()

		srv.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d want 401", path, recorder.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/attestation/report", nil)
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("POST /v1/attestation/report status=%d want 401 before method handling", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/pig/metrics", nil)
	request.Header.Add("Authorization", "Bearer secret")
	request.Header.Add("Authorization", "Bearer attacker")
	recorder = httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate Authorization status=%d want 401", recorder.Code)
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls=%d want 0", backendCalls)
	}
}

func TestCompletionAndResponsesProxyWithCombinedBodyRewrite(t *testing.T) {
	for _, path := range []string{"/v1/completions", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			var seenPath string
			var seenBody string
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seenPath = r.URL.Path
				seenBody = string(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"cmpl-test","choices":[{"text":"ok"}]}`))
			}))
			defer backend.Close()

			cfg := testProxyConfig(backend.URL)
			cfg.OpenAICompatStripEmptyToolCalls = true
			cfg.BackendPriorityInjectionEnabled = true
			cfg.BackendPriorityMode = "all"
			cfg.BackendPriorityRewriteStrategy = "field_scan"
			cfg.BackendPriorityField = "priority"
			cfg.BackendPriorityPremiumValue = -100
			cfg.BackendPriorityBasicValue = 0
			cfg.BackendPriorityBodyBytes = defaultOpenAICompatBodyBytesForTest
			cfg.BackendPriorityBufferBytes = 0
			cfg.BackendPriorityStreamBufferBytes = 4 * 1024
			cfg.BackendPriorityRewriteLimit = 8
			cfg.BackendPriorityFailOpen = false
			srv, err := newProxyServer(cfg)
			if err != nil {
				t.Fatalf("newProxyServer: %v", err)
			}

			body := `{"model":"m","messages":[{"role":"assistant","tool_calls":[]}],"priority":100}`
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer secret")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-User-Tier", "premium")
			recorder := httptest.NewRecorder()

			srv.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if seenPath != path {
				t.Fatalf("backend path=%q want %q", seenPath, path)
			}
			var payload map[string]any
			if err := json.Unmarshal([]byte(seenBody), &payload); err != nil {
				t.Fatalf("backend body is not json: %v; body=%s", err, seenBody)
			}
			if payload["priority"].(float64) != -100 {
				t.Fatalf("priority=%v want -100; body=%s", payload["priority"], seenBody)
			}
			messages := payload["messages"].([]any)
			message := messages[0].(map[string]any)
			if _, ok := message["tool_calls"]; ok {
				t.Fatalf("empty tool_calls was not stripped: %s", seenBody)
			}
		})
	}
}

func testProxyConfig(upstream string) config {
	return config{
		Listen:                          ":0",
		Upstream:                        upstream,
		Backends:                        []pigconfig.Backend{{Name: "backend1", Upstream: upstream}},
		Token:                           "secret",
		QoSPaths:                        []string{"/v1/chat/completions", "/v1/completions", "/v1/responses"},
		APIAuthEnabled:                  true,
		APIAuthPaths:                    []string{"/v1/chat/completions", "/v1/completions", "/v1/responses"},
		GlobalLimit:                     16,
		OpenAICompatStripEmptyToolCalls: false,
		OpenAICompatBodyBytes:           defaultOpenAICompatBodyBytesForTest,
		OpenAICompatFailOpen:            true,
		JSONClassifyBodyBytes:           2 * 1024 * 1024,
		JSONClassifyLimit:               16,
		MediumBodyBytes:                 60000,
		LongBodyBytes:                   100000,
		VeryLongBodyBytes:               524288,
		MediumOutputTokens:              1024,
		LongOutputTokens:                4096,
		VeryLongOutputTokens:            8192,
		BackendPriorityInjectionEnabled: false,
		DynamicPollInterval:             time.Second,
		DynamicFailsafeState:            "yellow",
		DynamicKVYellow:                 0.70,
		DynamicKVRed:                    0.80,
		DynamicWaitingYellow:            1,
		DynamicWaitingRed:               2,
		ProxyTimeout:                    10 * time.Second,
		QoSQueuePoll:                    10 * time.Millisecond,
	}
}

const defaultOpenAICompatBodyBytesForTest = 32 * 1024 * 1024
