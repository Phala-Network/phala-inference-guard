package server

import (
	"context"
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

	limit, err := probeSGLangRunningLimit(context.Background(), sglangRunningLimitProbeConfig{
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
			if limit, err := probeSGLangRunningLimit(context.Background(), sglangRunningLimitProbeConfig{
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
	got := initializePredictiveRunningLimit(context.Background(), explicit, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
	// An explicit limit is kept, but server_info is still read once for the
	// warm Prompt size.
	if got.Value != 192 || got.Source != coreadmission.RunningLimitSourceEnvironment || calls.Load() != 1 {
		t.Fatalf("explicit initialization=%+v calls=%d", got, calls.Load())
	}

	t.Setenv("PREDICTIVE_RUNNING_LIMIT", "0")
	disabled, err := loadConfig()
	if err != nil {
		t.Fatalf("load explicit zero running limit: %v", err)
	}
	disabled.PredictiveMetricsRequestTimeout = time.Second
	got = initializePredictiveRunningLimit(context.Background(), disabled, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
	if got.Value != 0 || got.Source != coreadmission.RunningLimitSourceEnvironment || calls.Load() != 2 {
		t.Fatalf("explicit zero initialization=%+v calls=%d", got, calls.Load())
	}

	got = initializePredictiveRunningLimit(context.Background(), base, predictiveBackendStartup{BackendKind: "vllm"}, server.URL+"/metrics")
	if got.Value != 0 || got.Source != coreadmission.RunningLimitSourceUnknown || calls.Load() != 2 {
		t.Fatalf("vLLM initialization=%+v calls=%d", got, calls.Load())
	}

	got = initializePredictiveRunningLimit(context.Background(), base, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
	if got.Value != 256 || got.Source != coreadmission.RunningLimitSourceSGLangServerInfo || calls.Load() != 3 {
		t.Fatalf("SGLang initialization=%+v calls=%d", got, calls.Load())
	}
}

func TestSGLangServerInfoDiscoversTheWarmPromptSize(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
		want int64
		err  bool
	}{
		{name: "chunked", body: `{"max_running_requests":64,"chunked_prefill_size":8192}`, want: 8192},
		{name: "absent", body: `{"max_running_requests":64}`, want: 0},
		{name: "unchunked", body: `{"max_running_requests":64,"chunked_prefill_size":-1}`, want: 0},
		{name: "string", body: `{"max_running_requests":64,"chunked_prefill_size":"8192"}`, err: true},
		{name: "duplicate inconsistent", body: `{"max_running_requests":64,"chunked_prefill_size":8192,"chunked_prefill_size":4096}`, err: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, test.body)
			}))
			t.Cleanup(server.Close)
			cfg := testProxyConfig("http://backend.invalid/v1")
			cfg.PredictiveMetricsRequestTimeout = time.Second
			cfg.PredictiveRunningLimit = 192
			got := discoverPredictiveBackend(context.Background(), cfg, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
			if test.err {
				if got.WarmPromptTokens != 0 || got.RunningLimit.Value != 192 {
					t.Fatalf("untrusted server_info must leave the warm size unset: %+v", got)
				}
				return
			}
			if got.WarmPromptTokens != test.want || got.RunningLimit.Value != 192 ||
				got.RunningLimit.Source != coreadmission.RunningLimitSourceEnvironment {
				t.Fatalf("discovery=%+v want warm=%d", got, test.want)
			}
		})
	}
	got := discoverPredictiveBackend(context.Background(), testProxyConfig("http://backend.invalid/v1"), predictiveBackendStartup{BackendKind: "vllm"}, "http://backend.invalid/metrics")
	if got.WarmPromptTokens != 0 {
		t.Fatalf("vLLM has no warm Prompt size: %+v", got)
	}
}

func TestSGLangTierSchedulingRequiresVerifiedBackendFlags(t *testing.T) {
	for _, test := range []struct {
		name   string
		fields string
		want   bool
	}{
		{name: "enabled", fields: `,"enable_priority_scheduling":true,"schedule_low_priority_values_first":false`, want: true},
		{name: "disabled", fields: `,"enable_priority_scheduling":false,"schedule_low_priority_values_first":false`},
		{name: "reversed", fields: `,"enable_priority_scheduling":true,"schedule_low_priority_values_first":true`},
		{name: "missing both"},
		{name: "missing direction", fields: `,"enable_priority_scheduling":true`},
		{name: "missing enabled", fields: `,"schedule_low_priority_values_first":false`},
		{name: "null direction", fields: `,"enable_priority_scheduling":true,"schedule_low_priority_values_first":null`},
		{name: "string enabled", fields: `,"enable_priority_scheduling":"true","schedule_low_priority_values_first":false`},
		{name: "inconsistent enabled", fields: `,"enable_priority_scheduling":false,"enable_priority_scheduling":true,"schedule_low_priority_values_first":false`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, `{"max_running_requests":64`+test.fields+`}`)
			}))
			defer server.Close()
			cfg := testProxyConfig("http://backend.invalid/v1")
			cfg.PredictiveMetricsRequestTimeout = time.Second
			got := discoverPredictiveBackend(context.Background(), cfg, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
			if got.BackendTierScheduling != test.want {
				t.Fatalf("tier scheduling=%t want %t for %s", got.BackendTierScheduling, test.want, test.name)
			}
		})
	}
}

func TestSGLangRunningLimitUsesEffectiveRequestPool(t *testing.T) {
	for _, test := range []struct {
		name, states string
		want         int64
		invalid      bool
	}{
		{name: "one pool", states: `[{"effective_max_running_requests_per_dp":43}]`, want: 43},
		{name: "multiple pools conservative minimum", states: `[{"effective_max_running_requests_per_dp":43},{"effective_max_running_requests_per_dp":44}]`, want: 43},
		{name: "configured maximum still caps", states: `[{"effective_max_running_requests_per_dp":64}]`, want: 48},
		{name: "legacy states", states: `[{}]`, want: 48},
		{name: "empty states", states: `[]`, want: 48},
		{name: "mixed schema", states: `[{"effective_max_running_requests_per_dp":43},{}]`, invalid: true},
		{name: "zero pool", states: `[{"effective_max_running_requests_per_dp":0}]`, invalid: true},
		{name: "negative pool", states: `[{"effective_max_running_requests_per_dp":-1}]`, invalid: true},
		{name: "string pool", states: `[{"effective_max_running_requests_per_dp":"43"}]`, invalid: true},
		{name: "fraction pool", states: `[{"effective_max_running_requests_per_dp":43.5}]`, invalid: true},
		{name: "duplicate pool", states: `[{"effective_max_running_requests_per_dp":43,"effective_max_running_requests_per_dp":44}]`, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(`{"max_running_requests":48,"internal_states":` + test.states + `}`)
			info, err := decodeSGLangServerInfo(body)
			if test.invalid {
				if err == nil {
					t.Fatal("ambiguous effective pool was accepted")
				}
				return
			}
			if err != nil || info.MaxRunningRequests != test.want {
				t.Fatalf("effective running limit=%d want=%d error=%v", info.MaxRunningRequests, test.want, err)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"max_running_requests":48,"internal_states":[{"effective_max_running_requests_per_dp":43}]}`)
	}))
	defer server.Close()
	cfg := testProxyConfig("http://backend.invalid/v1")
	cfg.PredictiveMetricsRequestTimeout = time.Second
	cfg.PredictiveRunningLimit = 192
	got := discoverPredictiveBackend(context.Background(), cfg, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
	if got.RunningLimit.Value != 192 || got.RunningLimit.Source != coreadmission.RunningLimitSourceEnvironment {
		t.Fatalf("effective pool replaced explicit limit: %+v", got)
	}
}

func TestV01223SGLangRunningLimitDiscoveryFailureRemainsUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)
	cfg := testProxyConfig("http://backend.invalid/v1")
	cfg.PredictiveMetricsRequestTimeout = time.Second
	got := initializePredictiveRunningLimit(context.Background(), cfg, predictiveBackendStartup{BackendKind: "sglang"}, server.URL+"/metrics")
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
	limit, err := probeSGLangRunningLimit(context.Background(), sglangRunningLimitProbeConfig{
		MetricsURL: server.URL + "/metrics", RequestTimeout: 25 * time.Millisecond,
	})
	if err == nil || limit != 0 {
		t.Fatalf("timed-out SGLang discovery limit=%d error=%v", limit, err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("SGLang discovery timeout was not bounded: %s", elapsed)
	}
}

func TestV01223SGLangRunningLimitProbeDoesNotFollowRedirects(t *testing.T) {
	var targetCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"max_running_requests":256}`)
	}))
	t.Cleanup(target.Close)

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/server_info", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirect.Close)

	limit, err := probeSGLangRunningLimit(context.Background(), sglangRunningLimitProbeConfig{
		MetricsURL: redirect.URL + "/metrics", RequestTimeout: time.Second,
	})
	if err == nil || limit != 0 || targetCalls.Load() != 0 {
		t.Fatalf("redirected SGLang discovery limit=%d error=%v target_calls=%d", limit, err, targetCalls.Load())
	}
}
