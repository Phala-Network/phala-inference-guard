package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

func TestV01223SGLangRunningLimitProbeReadsExactTopLevelInteger(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/server_info" || r.URL.RawQuery != "" {
			t.Fatalf("server_info request path=%q query=%q", r.URL.Path, r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = fmt.Fprint(w, `{"max_running_requests":256,"scheduler":{"max_running_requests":128}}`)
	}))
	t.Cleanup(server.Close)

	limit, err := probeSGLangRunningLimit(sglangRunningLimitProbeConfig{
		MetricsURL: server.URL + "/metrics", RequestTimeout: time.Second,
	})
	if err != nil || limit != 256 {
		t.Fatalf("SGLang running limit=%d error=%v", limit, err)
	}
}

func TestV01223SGLangRunningLimitProbeRejectsUntrustedShapes(t *testing.T) {
	for _, test := range []struct {
		name        string
		body        string
		contentType string
	}{
		{name: "missing", body: `{}`, contentType: "application/json"},
		{name: "string", body: `{"max_running_requests":"256"}`, contentType: "application/json"},
		{name: "fraction", body: `{"max_running_requests":256.5}`, contentType: "application/json"},
		{name: "zero", body: `{"max_running_requests":0}`, contentType: "application/json"},
		{name: "out of range", body: `{"max_running_requests":1048577}`, contentType: "application/json"},
		{name: "nested only", body: `{"scheduler":{"max_running_requests":256}}`, contentType: "application/json"},
		{name: "duplicate inconsistent", body: `{"max_running_requests":256,"max_running_requests":128}`, contentType: "application/json"},
		{name: "trailing object", body: `{"max_running_requests":256}{}`, contentType: "application/json"},
		{name: "wrong content type", body: `{"max_running_requests":256}`, contentType: "text/plain"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				_, _ = fmt.Fprint(w, test.body)
			}))
			t.Cleanup(server.Close)
			if limit, err := probeSGLangRunningLimit(sglangRunningLimitProbeConfig{
				MetricsURL: server.URL + "/metrics", RequestTimeout: time.Second,
			}); err == nil || limit != 0 {
				t.Fatalf("untrusted server_info limit=%d error=%v", limit, err)
			}
		})
	}
}

func TestV01223RunningLimitInitializationHonorsExplicitAndBackendContracts(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"max_running_requests":256}`)
	}))
	t.Cleanup(server.Close)
	base := testProxyConfig("http://backend.invalid/v1")
	base.PredictiveMetricsRequestTimeout = time.Second

	explicit := base
	explicit.PredictiveRunningLimit = 192
	got := initializePredictiveRunningLimit(explicit, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
	if got.Value != 192 || got.Source != coreadmission.RunningLimitSourceEnvironment || calls.Load() != 0 {
		t.Fatalf("explicit initialization=%+v calls=%d", got, calls.Load())
	}

	t.Setenv("PREDICTIVE_RUNNING_LIMIT", "0")
	disabled, err := loadConfig()
	if err != nil {
		t.Fatalf("load explicit zero running limit: %v", err)
	}
	disabled.PredictiveMetricsRequestTimeout = time.Second
	got = initializePredictiveRunningLimit(disabled, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
	if got.Value != 0 || got.Source != coreadmission.RunningLimitSourceEnvironment || calls.Load() != 0 {
		t.Fatalf("explicit zero initialization=%+v calls=%d", got, calls.Load())
	}

	got = initializePredictiveRunningLimit(base, predictiveBackendStartup{BackendKind: "vllm"}, server.URL+"/metrics")
	if got.Value != 0 || got.Source != coreadmission.RunningLimitSourceUnknown || calls.Load() != 0 {
		t.Fatalf("vLLM initialization=%+v calls=%d", got, calls.Load())
	}

	got = initializePredictiveRunningLimit(base, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
	if got.Value != 256 || got.Source != coreadmission.RunningLimitSourceSGLangServerInfo || calls.Load() != 1 {
		t.Fatalf("SGLang initialization=%+v calls=%d", got, calls.Load())
	}
}

func TestV01223SGLangRunningLimitDiscoveryFailureRemainsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	cfg := testProxyConfig("http://backend.invalid/v1")
	cfg.PredictiveMetricsRequestTimeout = time.Second
	got := initializePredictiveRunningLimit(cfg, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
	if got.Value != 0 || got.Source != coreadmission.RunningLimitSourceUnknown {
		t.Fatalf("failed SGLang discovery guessed a limit: %+v", got)
	}
	if endpoint, err := predictiveSGLangServerInfoURL("not-a-url"); err == nil || strings.TrimSpace(endpoint) != "" {
		t.Fatalf("invalid metrics URL endpoint=%q error=%v", endpoint, err)
	}
}

func TestV01223SGLangRunningLimitProbeTimeoutIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		<-request.Context().Done()
	}))
	t.Cleanup(server.Close)
	started := time.Now()
	limit, err := probeSGLangRunningLimit(sglangRunningLimitProbeConfig{
		MetricsURL: server.URL + "/metrics", RequestTimeout: 25 * time.Millisecond,
	})
	if err == nil || limit != 0 {
		t.Fatalf("timed-out SGLang discovery limit=%d error=%v", limit, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("SGLang discovery timeout was not bounded: %s", elapsed)
	}
}
