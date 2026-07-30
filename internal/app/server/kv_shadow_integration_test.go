package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	runtimebackend "github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
)

func TestKVShadowDoesNotChangeClientVisibleResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			_, _ = w.Write([]byte("vllm:kv_cache_usage_perc 0.95\n"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Upstream-Proof", "same")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"answer"}}]}`))
	}))
	defer backend.Close()

	off := runKVShadowRequest(t, backend.URL, "off", 95000)
	shadow := runKVShadowRequest(t, backend.URL, "shadow", 95000)
	if off.Code != shadow.Code || off.Body.String() != shadow.Body.String() {
		t.Fatalf("off=%d/%q shadow=%d/%q", off.Code, off.Body.String(), shadow.Code, shadow.Body.String())
	}
	if off.Header().Get("Content-Type") != shadow.Header().Get("Content-Type") || off.Header().Get("X-Upstream-Proof") != shadow.Header().Get("X-Upstream-Proof") {
		t.Fatalf("response headers changed: off=%v shadow=%v", off.Header(), shadow.Header())
	}
	if shadow.Code != http.StatusOK {
		t.Fatalf("emergency-red shadow changed response status to %d", shadow.Code)
	}
}

func TestKVShadowUsesStaticSingleBackendTokenMetrics(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/metrics" {
			_, _ = w.Write([]byte("vllm:cache_config_info{kv_cache_size_tokens=\"100000\"} 1\nvllm:kv_cache_usage_perc 0.95\nvllm:generation_tokens_total 1000\n"))
			return
		}
		w.Header().Set("X-Upstream-Proof", "static-metrics")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer backend.Close()

	cfg := testProxyConfig(backend.URL)
	cfg.KVAdmissionMode = "shadow"
	cfg.KVAdmissionPolicy = kvadmission.DefaultPolicy()
	cfg.KVAdmissionEstimator = kvadmission.DefaultEstimatorConfig()
	cfg.OutputTokenFields = []string{"max_tokens", "max_completion_tokens", "max_output_tokens"}
	cfg.DynamicEnabled = true
	cfg.DynamicEnforce = false
	cfg.DynamicMetricsURL = backend.URL + "/metrics"
	cfg.DynamicMetricsURLs = []string{cfg.DynamicMetricsURL}
	cfg.DynamicPollInterval = 5 * time.Millisecond
	cfg.Backends[0].MetricsURL = cfg.DynamicMetricsURL
	srv, err := newProxyServer(cfg)
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for {
		status := srv.backendRuntimeStatus(0, srv.backends[0])
		if status.KVTokenMetricsValid && status.KVCapacityTokens == 100000 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("static metrics runtime not observed: %#v", status)
		}
		time.Sleep(time.Millisecond)
	}
	if status := srv.backends[0].Status(); !status.Updated.IsZero() {
		t.Fatalf("single-backend proxy status unexpectedly populated: %#v", status)
	}

	body := `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("X-Upstream-Proof") != "static-metrics" {
		t.Fatalf("response=%d headers=%v body=%s", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if decision := srv.kvShadow.Snapshot().LastDecision; decision.Reason != kvadmission.ReasonEmergencyRed || decision.CapacityTokens != 100000 {
		t.Fatalf("static shadow decision=%#v want emergency_red at capacity 100000", decision)
	}
}

func runKVShadowRequest(t *testing.T, upstream, mode string, used int64) *httptest.ResponseRecorder {
	t.Helper()
	cfg := testProxyConfig(upstream)
	cfg.KVAdmissionMode = mode
	cfg.KVAdmissionPolicy = kvadmission.DefaultPolicy()
	cfg.KVAdmissionEstimator = kvadmission.DefaultEstimatorConfig()
	cfg.OutputTokenFields = []string{"max_tokens", "max_completion_tokens", "max_output_tokens"}
	if mode == "shadow" {
		cfg.DynamicEnabled = true
		cfg.DynamicEnforce = false
		cfg.DynamicMetricsURL = upstream + "/metrics"
		cfg.DynamicMetricsURLs = []string{cfg.DynamicMetricsURL}
		cfg.Backends[0].MetricsURL = cfg.DynamicMetricsURL
	}
	srv, err := newProxyServer(cfg)
	if err != nil {
		t.Fatalf("newProxyServer(%s): %v", mode, err)
	}
	srv.backends[0].StoreStatus(runtimebackend.Runtime{
		Name:                "backend1",
		BackendKind:         "vllm",
		KVCapacityTokens:    100000,
		KVUsedTokens:        used,
		KVAvailableTokens:   100000 - used,
		KVTokenMetricsValid: true,
		Generation:          1000,
		Updated:             time.Now(),
	})
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if srv.kvShadow != nil {
		snapshot := srv.kvShadow.Snapshot()
		if snapshot.LastDecision.Reason != kvadmission.ReasonEmergencyRed {
			t.Fatalf("last shadow decision=%s want emergency_red", snapshot.LastDecision.Reason)
		}
		if snapshot.LastDecision.BoundedDecodeTokens != 64 {
			t.Fatalf("bounded decode=%d want request max 64 even when lane output classification is disabled", snapshot.LastDecision.BoundedDecodeTokens)
		}
		if snapshot.Reservations != 0 {
			t.Fatalf("reservations=%d want 0 after request", snapshot.Reservations)
		}
	}
	_, _ = io.Copy(io.Discard, request.Body)
	return recorder
}
