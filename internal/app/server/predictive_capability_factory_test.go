package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestV012FactoryBuildsProfileFromOneBoundedStartupCalibration(t *testing.T) {
	fixture := newV012CapabilityFixture(t, 0)
	fixture.SetPostProbeKVUsage(0.10)
	fixture.SetDelayedProbeMetrics(true)
	defer fixture.Close()

	cfg := v012CapabilityFactoryConfig(fixture.URL())
	shadow, err := newDefaultPredictiveShadow(cfg)
	if err != nil {
		t.Fatalf("construct calibrated factory: %v", err)
	}
	adapter, ok := shadow.(*requestAwarePredictiveAdapter)
	if !ok {
		_ = shadow.Close()
		t.Fatalf("calibrated adapter = %T", shadow)
	}
	defer func() {
		if err := adapter.Close(); err != nil {
			t.Errorf("close calibrated adapter: %v", err)
		}
	}()
	profile := adapter.PredictiveAdmissionTelemetry().CapabilityProfile
	if profile.Source != runtimepredictive.CapabilityProfileCalibrated ||
		profile.KVHardLimitTokens != 880_000 {
		t.Fatalf("calibrated capability profile = %+v", profile)
	}
	if reason := adapter.PredictiveAdmissionTelemetry().CapabilityReason; reason != "calibrated" {
		t.Fatalf("calibrated capability reason = %q", reason)
	}
	if physical := adapter.PredictiveAdmissionTelemetry().Manager.Virtual.Upper.PhysicalKVUpper; physical != 100_000 {
		t.Fatalf("manager initial physical KV = %d, want post-probe 100000", physical)
	}

	models, completions := fixture.Calls()
	if models != 1 || completions != 2 {
		t.Fatalf("startup calibration calls models/completions = %d/%d, want 1/2", models, completions)
	}
	if fixture.AuthorizationSeen() {
		t.Fatal("startup calibration forwarded an Authorization header")
	}
	if got := v012PrefillClass(adapter, 50_000); got != runtimepredictive.RequestAwarePrefillWeighted {
		t.Fatalf("calibrated 50K Prefill class = %s, want weighted", got)
	}
	if got := v012PrefillClass(adapter, 200_000); got != runtimepredictive.RequestAwarePrefillExclusive {
		t.Fatalf("calibrated 200K Prefill class = %s, want exclusive", got)
	}
	if got := v012PrefillClass(adapter, 350_000); got != runtimepredictive.RequestAwarePrefillQuiescent {
		t.Fatalf(
			"calibrated 350K Prefill class = %s, want quiescent for 40000/160000/320000/160000 profile",
			got,
		)
	}
}

func TestV012BusyStartupUsesFallbackWithoutWaitingForCalibration(t *testing.T) {
	fixture := newV012CapabilityFixture(t, 1)
	defer fixture.Close()

	cfg := v012CapabilityFactoryConfig(fixture.URL())
	started := time.Now()
	shadow, err := newDefaultPredictiveShadow(cfg)
	if err != nil {
		t.Fatalf("construct busy fallback factory: %v", err)
	}
	defer func() {
		if err := shadow.Close(); err != nil {
			t.Errorf("close busy fallback: %v", err)
		}
	}()
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("busy startup fallback took %s", elapsed)
	}

	models, completions := fixture.Calls()
	if models != 0 || completions != 0 {
		t.Fatalf("busy startup calibration calls models/completions = %d/%d, want 0/0", models, completions)
	}
	adapter := shadow.(*requestAwarePredictiveAdapter)
	if telemetry := adapter.PredictiveAdmissionTelemetry(); telemetry.CapabilityProfile.Source != runtimepredictive.CapabilityProfileFallback || telemetry.CapabilityReason != "busy_fallback" {
		t.Fatalf("busy capability telemetry = %+v/%q, want fallback/busy_fallback", telemetry.CapabilityProfile, telemetry.CapabilityReason)
	}
	if got := v012PrefillClass(adapter, 50_000); got != runtimepredictive.RequestAwarePrefillRegular {
		t.Fatalf("fallback 50K Prefill class = %s, want regular", got)
	}
	if got := v012PrefillClass(adapter, 200_000); got != runtimepredictive.RequestAwarePrefillWeighted {
		t.Fatalf("fallback 200K Prefill class = %s, want weighted", got)
	}
	if got := v012PrefillClass(adapter, 350_000); got != runtimepredictive.RequestAwarePrefillExclusive {
		t.Fatalf("fallback 350K Prefill class = %s, want exclusive", got)
	}
}

func TestV012CompleteExplicitPrefillProfileSkipsCalibration(t *testing.T) {
	fixture := newV012CapabilityFixture(t, 0)
	defer fixture.Close()

	cfg := v012CapabilityFactoryConfig(fixture.URL())
	cfg.PredictivePrefillRegularTokens = 32_768
	cfg.PredictivePrefillExclusiveTokens = 131_072
	cfg.PredictivePrefillQuiescentTokens = 262_144
	cfg.PredictivePrefillAggregateBudgetTokens = 196_608
	shadow, err := newDefaultPredictiveShadow(cfg)
	if err != nil {
		t.Fatalf("construct explicit capability factory: %v", err)
	}
	defer func() {
		if err := shadow.Close(); err != nil {
			t.Errorf("close explicit capability factory: %v", err)
		}
	}()
	models, completions := fixture.Calls()
	if models != 0 || completions != 0 {
		t.Fatalf("explicit profile calls models/completions = %d/%d, want 0/0", models, completions)
	}
	telemetry := shadow.(*requestAwarePredictiveAdapter).PredictiveAdmissionTelemetry()
	profile := telemetry.CapabilityProfile
	if profile.Source != runtimepredictive.CapabilityProfileExplicit ||
		profile.PrefillRegularTokens != 32_768 || profile.PrefillExclusiveTokens != 131_072 ||
		profile.PrefillQuiescentTokens != 262_144 || profile.PrefillAggregateBudgetTokens != 196_608 {
		t.Fatalf("explicit capability profile = %+v", profile)
	}
	if telemetry.CapabilityReason != "explicit_override" {
		t.Fatalf("explicit capability reason = %q", telemetry.CapabilityReason)
	}
}

func TestV012CacheHitContaminatedProbeFallsBack(t *testing.T) {
	fixture := newV012CapabilityFixture(t, 0)
	fixture.SetProbeCacheHit(true)
	defer fixture.Close()

	shadow, err := newDefaultPredictiveShadow(v012CapabilityFactoryConfig(fixture.URL()))
	if err != nil {
		t.Fatalf("construct cache-hit fallback factory: %v", err)
	}
	defer func() {
		if err := shadow.Close(); err != nil {
			t.Errorf("close cache-hit fallback factory: %v", err)
		}
	}()
	models, completions := fixture.Calls()
	if models != 1 || completions != 1 {
		t.Fatalf("cache-hit fallback calls models/completions = %d/%d, want 1/1", models, completions)
	}
	telemetry := shadow.(*requestAwarePredictiveAdapter).PredictiveAdmissionTelemetry()
	profile := telemetry.CapabilityProfile
	if profile.Source != runtimepredictive.CapabilityProfileFallback || profile.SafeColdPrefillTokensPerSec != 0 {
		t.Fatalf("cache-hit contaminated capability profile = %+v", profile)
	}
	if telemetry.CapabilityReason != "probe_fallback" {
		t.Fatalf("cache-hit fallback reason = %q", telemetry.CapabilityReason)
	}
}

func TestV012CapabilityMetadataRedirectIsNotFollowed(t *testing.T) {
	redirectCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectCalls++
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(w, r)
	}))
	defer origin.Close()

	initialization, err := initializePredictiveCapability(predictiveCapabilityInitializationConfig{
		MetricsURL: origin.URL + "/metrics", UpstreamURL: origin.URL,
		RequestTimeout: 50 * time.Millisecond, RetryInterval: 10 * time.Millisecond,
		KVHardRatio: 0.88,
	}, v012CapabilityStartup())
	if err != nil {
		t.Fatalf("redirect fallback initialization: %v", err)
	}
	if redirectCalls != 0 {
		t.Fatalf("capability client followed metadata redirect %d times", redirectCalls)
	}
	if initialization.Profile.Source != runtimepredictive.CapabilityProfileFallback || initialization.Reason != "metadata_fallback" {
		t.Fatalf("redirect initialization = %+v/%q, want metadata fallback", initialization.Profile, initialization.Reason)
	}
}

func TestV012CapabilityClientBypassesEnvironmentProxy(t *testing.T) {
	client := newPredictiveCalibrationHTTPClient()
	defer client.CloseIdleConnections()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("capability transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("capability transport unexpectedly consults an environment proxy")
	}
}

func TestV012SubmittedProbeFailsClosedWhenFinalStateIsUnknownOrDrifts(t *testing.T) {
	tests := []struct {
		name       string
		finalModel string
		capacity   int64
		blockSize  int
		status     int
	}{
		{name: "metrics unavailable", finalModel: "vendor/capability-model", capacity: 1_000_000, blockSize: 64, status: http.StatusServiceUnavailable},
		{name: "identity drift", finalModel: "vendor/other-model", capacity: 1_000_000, blockSize: 64, status: http.StatusOK},
		{name: "capacity drift", finalModel: "vendor/capability-model", capacity: 2_000_000, blockSize: 64, status: http.StatusOK},
		{name: "block-size drift", finalModel: "vendor/capability-model", capacity: 1_000_000, blockSize: 32, status: http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var submitted atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/completions":
					submitted.Store(true)
					_, _ = io.WriteString(w, `{"choices":[{"text":"x"}]}`)
				case "/metrics":
					if !submitted.Load() {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if test.status != http.StatusOK {
						w.WriteHeader(test.status)
						return
					}
					writeV012FinalProbeMetrics(w, test.finalModel, test.capacity, test.blockSize)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
			defer cancel()
			result, err := runPredictiveColdPrefillProbe(ctx, &http.Client{}, predictiveColdPrefillProbeConfig{
				MetricsURL: server.URL + "/metrics", UpstreamURL: server.URL,
				RequestTimeout: 20 * time.Millisecond, RetryInterval: 5 * time.Millisecond,
				ModelName: "vendor/capability-model", IdentitySHA256: predictiveModelIdentitySHA256("vendor/capability-model"),
				CapacityTokens: 1_000_000, BlockSize: 64,
			}, v012CapabilityStartup(), 2_048)
			if err == nil || result.Final.ModelIdentitySHA256 != "" || !strings.Contains(err.Error(), "final state is unavailable") {
				t.Fatalf("submitted probe result/error = %+v/%v, want fail-closed unknown final state", result, err)
			}
		})
	}
}

func v012CapabilityStartup() predictiveVLLMStartup {
	return predictiveVLLMStartup{
		modelName: "vendor/capability-model", ModelIdentitySHA256: predictiveModelIdentitySHA256("vendor/capability-model"),
		CapacityTokens: 1_000_000, BlockSize: 64, CapabilityMetricsOK: true,
		ObservedAt: time.Unix(1, 0),
	}
}

func writeV012FinalProbeMetrics(w io.Writer, model string, capacity int64, blockSize int) {
	_, _ = fmt.Fprintf(w, `
vllm:cache_config_info{block_size="%d",kv_cache_size_tokens="%d"} 1
vllm:kv_cache_usage_perc 0
vllm:num_requests_running{model_name="%s",engine="0"} 0
vllm:num_requests_waiting{model_name="%s",engine="0"} 0
vllm:num_preemptions_total{model_name="%s",engine="0"} 0
vllm:generation_tokens_total{model_name="%s",engine="0"} 1
vllm:prompt_tokens_by_source_total{model_name="%s",engine="0",source="local_compute"} 2048
vllm:prompt_tokens_by_source_total{model_name="%s",engine="0",source="local_cache_hit"} 0
vllm:request_prefill_time_seconds_count{model_name="%s",engine="0"} 1
vllm:request_prefill_time_seconds_sum{model_name="%s",engine="0"} 0.2
`, blockSize, capacity, model, model, model, model, model, model, model, model)
}

func v012PrefillClass(adapter *requestAwarePredictiveAdapter, tokens int64) runtimepredictive.RequestAwarePrefillClass {
	decision := adapter.policy.Evaluate(runtimepredictive.RequestAwareInput{
		MetricsFresh:           true,
		IdentityValid:          true,
		CapacityTokens:         1_000_000,
		RequestReservedTokens:  tokens,
		SelectionInputTokens:   tokens,
		EstimatedPrefillTokens: tokens,
	})
	return decision.PrefillClass
}

type v012CapabilityFixture struct {
	t                 *testing.T
	server            *httptest.Server
	mu                sync.Mutex
	running           int
	models            int
	probes            int
	local             uint64
	prefill           float64
	cacheHit          bool
	postProbeKVUsage  float64
	publishedProbes   int
	delayProbeMetrics bool
	unpublishedProbe  int
	authorizationSeen bool
}

func newV012CapabilityFixture(t *testing.T, running int) *v012CapabilityFixture {
	t.Helper()
	fixture := &v012CapabilityFixture{t: t, running: running}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.ServeHTTP))
	return fixture
}

func (f *v012CapabilityFixture) URL() string {
	return f.server.URL
}

func (f *v012CapabilityFixture) Close() {
	f.server.Close()
}

func (f *v012CapabilityFixture) Calls() (models, completions int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.models, f.probes
}

func (f *v012CapabilityFixture) AuthorizationSeen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorizationSeen
}

func (f *v012CapabilityFixture) SetProbeCacheHit(enabled bool) {
	f.mu.Lock()
	f.cacheHit = enabled
	f.mu.Unlock()
}

func (f *v012CapabilityFixture) SetPostProbeKVUsage(usage float64) {
	f.mu.Lock()
	f.postProbeKVUsage = usage
	f.mu.Unlock()
}

func (f *v012CapabilityFixture) SetDelayedProbeMetrics(enabled bool) {
	f.mu.Lock()
	f.delayProbeMetrics = enabled
	f.mu.Unlock()
}

func (f *v012CapabilityFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "" {
		f.authorizationSeen = true
	}
	switch r.URL.Path {
	case "/metrics":
		f.writeMetrics(w)
		if f.unpublishedProbe > 0 {
			f.publishProbeMetrics(f.unpublishedProbe)
			f.unpublishedProbe = 0
		}
	case "/v1/models":
		f.models++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"vendor/capability-model","max_model_len":262144}]}`)
	case "/v1/completions":
		f.probes++
		if f.delayProbeMetrics {
			f.unpublishedProbe = f.probes
		} else {
			f.publishProbeMetrics(f.probes)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"calibration","choices":[{"text":"x","finish_reason":"length"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *v012CapabilityFixture) publishProbeMetrics(probe int) {
	switch probe {
	case 1:
		f.local += 4_000
		f.prefill += 0.4
	case 2:
		f.local += 40_000
		f.prefill += 4.0
	default:
		f.t.Errorf("unexpected startup probe %d", probe)
	}
	f.publishedProbes = probe
}

func (f *v012CapabilityFixture) writeMetrics(w io.Writer) {
	cacheHit := 0
	if f.cacheHit {
		cacheHit = f.publishedProbes
	}
	kvUsage := 0.0
	if f.publishedProbes > 0 {
		kvUsage = f.postProbeKVUsage
	}
	_, _ = fmt.Fprintf(w, `
vllm:cache_config_info{block_size="64",kv_cache_size_tokens="1000000",num_gpu_blocks="15625"} 1
vllm:kv_cache_usage_perc %.6f
vllm:num_requests_running{model_name="vendor/capability-model",engine="0"} %d
vllm:num_requests_waiting{model_name="vendor/capability-model",engine="0"} 0
vllm:num_preemptions_total{model_name="vendor/capability-model",engine="0"} 0
vllm:generation_tokens_total{model_name="vendor/capability-model",engine="0"} %d
vllm:prompt_tokens_by_source_total{model_name="vendor/capability-model",engine="0",source="local_compute"} %d
vllm:prompt_tokens_by_source_total{model_name="vendor/capability-model",engine="0",source="local_cache_hit"} %d
vllm:request_prefill_time_seconds_count{model_name="vendor/capability-model",engine="0"} %d
vllm:request_prefill_time_seconds_sum{model_name="vendor/capability-model",engine="0"} %.6f
`, kvUsage, f.running, f.probes, f.local, cacheHit, f.publishedProbes, f.prefill)
}

func v012CapabilityFactoryConfig(upstream string) config {
	cfg := testProxyConfig(upstream)
	cfg.PredictiveAdmissionMode = "shadow"
	cfg.PredictiveMetricsURL = upstream + "/metrics"
	cfg.PredictiveStartupProbeTimeout = 2 * time.Second
	cfg.PredictiveMetricsRequestTimeout = 500 * time.Millisecond
	cfg.PredictiveObservationPollInterval = time.Hour
	cfg.PredictiveMaximumMetricsAge = 2 * time.Hour
	cfg.PredictivePrefillRegularTokens = 0
	cfg.PredictivePrefillExclusiveTokens = 0
	cfg.PredictivePrefillQuiescentTokens = 0
	cfg.PredictivePrefillAggregateBudgetTokens = 0
	return cfg
}
