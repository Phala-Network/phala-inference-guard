package server

import (
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

func TestV0125AutomaticCapabilityUsesMetadataWithoutCompletion(t *testing.T) {
	fixture := newV0125CapabilityFixture(t, 0, 256*1024)
	defer fixture.Close()

	shadow, err := newDefaultPredictiveShadow(v0125CapabilityFactoryConfig(fixture.URL()))
	if err != nil {
		t.Fatalf("construct metadata-derived factory: %v", err)
	}
	defer func() {
		if err := shadow.Close(); err != nil {
			t.Errorf("close metadata-derived factory: %v", err)
		}
	}()

	models, completions := fixture.Calls()
	if models != 1 || completions != 0 {
		t.Fatalf("automatic initialization calls models/completions = %d/%d, want 1/0", models, completions)
	}
	if fixture.AuthorizationSeen() {
		t.Fatal("automatic metadata request forwarded an Authorization header")
	}
	telemetry := shadow.(*requestAwarePredictiveAdapter).PredictiveAdmissionTelemetry()
	assertV0125CapabilityProfile(t, telemetry.CapabilityProfile, "automatic", 880_000, 32_768, 131_072, 262_144, 131_072)
	if telemetry.CapabilityReason != "metadata" {
		t.Fatalf("automatic initialization reason = %q, want metadata", telemetry.CapabilityReason)
	}
}

func TestV0125AutomaticCapabilityIsBusyInvariant(t *testing.T) {
	var profiles [2]runtimepredictive.BackendCapabilityProfile
	var reasons [2]string
	for index, running := range []int{0, 1} {
		fixture := newV0125CapabilityFixture(t, running, 256*1024)
		shadow, err := newDefaultPredictiveShadow(v0125CapabilityFactoryConfig(fixture.URL()))
		if err != nil {
			fixture.Close()
			t.Fatalf("construct automatic factory with running=%d: %v", running, err)
		}
		models, completions := fixture.Calls()
		telemetry := shadow.(*requestAwarePredictiveAdapter).PredictiveAdmissionTelemetry()
		profiles[index] = telemetry.CapabilityProfile
		reasons[index] = telemetry.CapabilityReason
		closeErr := shadow.Close()
		fixture.Close()
		if closeErr != nil {
			t.Errorf("close automatic factory with running=%d: %v", running, closeErr)
		}
		if models != 1 || completions != 0 {
			t.Errorf("running=%d initialization calls models/completions = %d/%d, want 1/0", running, models, completions)
		}
	}
	if profiles[0] != profiles[1] || reasons[0] != "metadata" || reasons[1] != "metadata" {
		t.Fatalf(
			"idle and busy startup derived different capability contracts:\nidle=%+v/%s\nbusy=%+v/%s",
			profiles[0], reasons[0], profiles[1], reasons[1],
		)
	}
}

func TestV0125AutomaticCapabilityGeometry(t *testing.T) {
	tests := []struct {
		name           string
		maxModelLen    int64
		metadataStatus int
		capacity       int64
		wantHard       int64
		wantRegular    int64
		wantExclusive  int64
		wantQuiescent  int64
		wantAggregate  int64
		wantReason     string
	}{
		{name: "32K context", maxModelLen: 32 * 1024, metadataStatus: http.StatusOK, capacity: 1_000_000, wantHard: 880_000, wantRegular: 4 * 1024, wantExclusive: 16 * 1024, wantQuiescent: 32 * 1024, wantAggregate: 16 * 1024, wantReason: "metadata"},
		{name: "256K context", maxModelLen: 256 * 1024, metadataStatus: http.StatusOK, capacity: 1_000_000, wantHard: 880_000, wantRegular: 32 * 1024, wantExclusive: 128 * 1024, wantQuiescent: 256 * 1024, wantAggregate: 128 * 1024, wantReason: "metadata"},
		{name: "650K context", maxModelLen: 650 * 1024, metadataStatus: http.StatusOK, capacity: 1_000_000, wantHard: 880_000, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantAggregate: 256 * 1024, wantReason: "metadata"},
		{name: "KV limited", maxModelLen: 650 * 1024, metadataStatus: http.StatusOK, capacity: 300_000, wantHard: 264_000, wantRegular: 32_960, wantExclusive: 131_968, wantQuiescent: 264_000, wantAggregate: 131_968, wantReason: "metadata"},
		{name: "metadata fallback", metadataStatus: http.StatusServiceUnavailable, capacity: 1_000_000, wantHard: 880_000, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantAggregate: 256 * 1024, wantReason: "metadata_fallback"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var models atomic.Int64
			var completions atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models":
					models.Add(1)
					if test.metadataStatus != http.StatusOK {
						w.WriteHeader(test.metadataStatus)
						return
					}
					_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":"vendor/capability-model","max_model_len":%d}]}`, test.maxModelLen)
				case "/v1/completions":
					completions.Add(1)
					w.WriteHeader(http.StatusInternalServerError)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			startup := v0125CapabilityStartup(test.capacity)
			initialization, err := initializePredictiveCapability(predictiveCapabilityInitializationConfig{
				UpstreamURL: server.URL, RequestTimeout: 50 * time.Millisecond, KVHardRatio: 0.88,
			}, startup)
			if err != nil {
				t.Fatalf("initialize automatic capability: %v", err)
			}
			if gotModels, gotCompletions := models.Load(), completions.Load(); gotModels != 1 || gotCompletions != 0 {
				t.Fatalf("initialization calls models/completions = %d/%d, want 1/0", gotModels, gotCompletions)
			}
			assertV0125CapabilityProfile(t, initialization.Profile, "automatic", test.wantHard, test.wantRegular, test.wantExclusive, test.wantQuiescent, test.wantAggregate)
			if initialization.Reason != test.wantReason {
				t.Fatalf("initialization reason = %q, want %q", initialization.Reason, test.wantReason)
			}
		})
	}
}

func TestV0125CompleteExplicitPrefillProfileSkipsMetadata(t *testing.T) {
	fixture := newV0125CapabilityFixture(t, 0, 256*1024)
	defer fixture.Close()
	cfg := v0125CapabilityFactoryConfig(fixture.URL())
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
	if models, completions := fixture.Calls(); models != 0 || completions != 0 {
		t.Fatalf("explicit profile calls models/completions = %d/%d, want 0/0", models, completions)
	}
	telemetry := shadow.(*requestAwarePredictiveAdapter).PredictiveAdmissionTelemetry()
	assertV0125CapabilityProfile(t, telemetry.CapabilityProfile, "explicit", 880_000, 32_768, 131_072, 262_144, 196_608)
	if telemetry.CapabilityReason != "explicit_override" {
		t.Fatalf("explicit capability reason = %q", telemetry.CapabilityReason)
	}
}

func TestV0125InvalidExplicitCapabilityFailsBeforeMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*config)
	}{
		{name: "partial", mutate: func(cfg *config) { cfg.PredictivePrefillRegularTokens = 4 * 1024 }},
		{name: "ordering", mutate: func(cfg *config) {
			cfg.PredictivePrefillRegularTokens = 32 * 1024
			cfg.PredictivePrefillExclusiveTokens = 16 * 1024
			cfg.PredictivePrefillQuiescentTokens = 64 * 1024
			cfg.PredictivePrefillAggregateBudgetTokens = 32 * 1024
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newV0125CapabilityFixture(t, 0, 256*1024)
			defer fixture.Close()
			cfg := v0125CapabilityFactoryConfig(fixture.URL())
			test.mutate(&cfg)
			if _, err := newDefaultPredictiveShadow(cfg); err == nil {
				t.Fatal("invalid explicit capability was accepted")
			}
			if models, completions := fixture.Calls(); models != 0 || completions != 0 {
				t.Fatalf("invalid explicit capability calls models/completions = %d/%d, want 0/0", models, completions)
			}
		})
	}
}

func TestV0125CapabilityMetadataRedirectIsNotFollowed(t *testing.T) {
	var redirectCalls atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectCalls.Add(1)
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
		UpstreamURL: origin.URL, RequestTimeout: 50 * time.Millisecond, KVHardRatio: 0.88,
	}, v0125CapabilityStartup(1_000_000))
	if err != nil {
		t.Fatalf("redirect fallback initialization: %v", err)
	}
	if redirectCalls.Load() != 0 {
		t.Fatalf("capability client followed metadata redirect %d times", redirectCalls.Load())
	}
	if string(initialization.Profile.Source) != "automatic" || initialization.Reason != "metadata_fallback" {
		t.Fatalf("redirect initialization = %+v/%q, want automatic/metadata_fallback", initialization.Profile, initialization.Reason)
	}
}

func TestV0125CapabilityMetadataResponseIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, strings.Repeat("x", predictiveMetadataMaximumModelBody+1))
	}))
	defer server.Close()
	initialization, err := initializePredictiveCapability(predictiveCapabilityInitializationConfig{
		UpstreamURL: server.URL, RequestTimeout: 50 * time.Millisecond, KVHardRatio: 0.88,
	}, v0125CapabilityStartup(1_000_000))
	if err != nil {
		t.Fatalf("bounded metadata fallback initialization: %v", err)
	}
	if initialization.Reason != "metadata_fallback" {
		t.Fatalf("oversized metadata reason = %q, want metadata_fallback", initialization.Reason)
	}
}

func TestV0125CapabilityClientBypassesEnvironmentProxy(t *testing.T) {
	client := newPredictiveMetadataHTTPClient()
	defer client.CloseIdleConnections()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("capability transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("capability transport unexpectedly consults an environment proxy")
	}
}

func TestV0125AutomaticCapabilityRejectsInvalidDerivedGeometry(t *testing.T) {
	var completions atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"vendor/capability-model","max_model_len":32768}]}`)
		case "/v1/completions":
			completions.Add(1)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	startup := v0125CapabilityStartup(128)
	if _, err := initializePredictiveCapability(predictiveCapabilityInitializationConfig{
		UpstreamURL: server.URL, RequestTimeout: 50 * time.Millisecond, KVHardRatio: 0.88,
	}, startup); err == nil {
		t.Fatal("invalid derived geometry was accepted")
	}
	if completions.Load() != 0 {
		t.Fatalf("invalid geometry triggered %d completion calls", completions.Load())
	}
}

func assertV0125CapabilityProfile(
	t *testing.T,
	profile runtimepredictive.BackendCapabilityProfile,
	wantSource string,
	wantHard, wantRegular, wantExclusive, wantQuiescent, wantAggregate int64,
) {
	t.Helper()
	if profile.SchemaVersion != "request-aware-capability-v2" || string(profile.Source) != wantSource ||
		profile.KVHardLimitTokens != wantHard || profile.PrefillRegularTokens != wantRegular ||
		profile.PrefillExclusiveTokens != wantExclusive || profile.PrefillQuiescentTokens != wantQuiescent ||
		profile.PrefillAggregateBudgetTokens != wantAggregate {
		t.Fatalf(
			"capability profile = %+v, want source=%s hard=%d prefill=%d/%d/%d/%d",
			profile, wantSource, wantHard, wantRegular, wantExclusive, wantQuiescent, wantAggregate,
		)
	}
}

func v0125CapabilityStartup(capacity int64) predictiveVLLMStartup {
	return predictiveVLLMStartup{
		modelName: "vendor/capability-model", ModelIdentitySHA256: predictiveModelIdentitySHA256("vendor/capability-model"),
		CapacityTokens: capacity, BlockSize: 64, CapabilityMetricsOK: true,
		ObservedAt: time.Unix(1, 0),
	}
}

type v0125CapabilityFixture struct {
	t                 *testing.T
	server            *httptest.Server
	mu                sync.Mutex
	running           int
	maxModelLen       int64
	models            int
	completions       int
	authorizationSeen bool
}

func newV0125CapabilityFixture(t *testing.T, running int, maxModelLen int64) *v0125CapabilityFixture {
	t.Helper()
	fixture := &v0125CapabilityFixture{t: t, running: running, maxModelLen: maxModelLen}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.ServeHTTP))
	return fixture
}

func (f *v0125CapabilityFixture) URL() string {
	return f.server.URL
}

func (f *v0125CapabilityFixture) Close() {
	f.server.Close()
}

func (f *v0125CapabilityFixture) Calls() (models, completions int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.models, f.completions
}

func (f *v0125CapabilityFixture) AuthorizationSeen() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorizationSeen
}

func (f *v0125CapabilityFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "" {
		f.authorizationSeen = true
	}
	switch r.URL.Path {
	case "/metrics":
		f.writeMetrics(w)
	case "/v1/models":
		f.models++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"object":"list","data":[{"id":"vendor/capability-model","max_model_len":%d}]}`, f.maxModelLen)
	case "/v1/completions":
		f.completions++
		w.WriteHeader(http.StatusInternalServerError)
	default:
		http.NotFound(w, r)
	}
}

func (f *v0125CapabilityFixture) writeMetrics(w io.Writer) {
	_, _ = fmt.Fprintf(w, `
vllm:cache_config_info{block_size="64",kv_cache_size_tokens="1000000",num_gpu_blocks="15625"} 1
vllm:kv_cache_usage_perc 0
vllm:num_requests_running{model_name="vendor/capability-model",engine="0"} %d
vllm:num_requests_waiting{model_name="vendor/capability-model",engine="0"} 0
vllm:num_preemptions_total{model_name="vendor/capability-model",engine="0"} 0
vllm:generation_tokens_total{model_name="vendor/capability-model",engine="0"} 0
vllm:prompt_tokens_by_source_total{model_name="vendor/capability-model",engine="0",source="local_compute"} 0
vllm:prompt_tokens_by_source_total{model_name="vendor/capability-model",engine="0",source="local_cache_hit"} 0
vllm:request_prefill_time_seconds_count{model_name="vendor/capability-model",engine="0"} 0
vllm:request_prefill_time_seconds_sum{model_name="vendor/capability-model",engine="0"} 0
`, f.running)
}

func v0125CapabilityFactoryConfig(upstream string) config {
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
