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

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestV0125AutomaticCapabilityUsesMetadataWithoutCompletion(t *testing.T) {
	fixture := newV0125CapabilityFixture(t, 0, 256*1024)
	defer fixture.Close()

	service, err := newDefaultAdmissionService(v0125CapabilityFactoryConfig(fixture.URL()))
	if err != nil {
		t.Fatalf("construct metadata-derived factory: %v", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
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
	telemetry := service.Snapshot(time.Now())
	assertV0125CapabilityProfile(
		t, telemetry.CapabilityProfile, "automatic",
		880_000, 256*1024, 256*1024-256,
		64*1024, 256*1024, 512*1024, 64*1024, 256*1024-256,
	)
	if telemetry.CapabilityReason != "metadata" {
		t.Fatalf("automatic initialization reason = %q, want metadata", telemetry.CapabilityReason)
	}
}

func TestV0125AutomaticCapabilityIsBusyInvariant(t *testing.T) {
	var profiles [2]runtimepredictive.BackendCapabilityProfile
	var reasons [2]string
	for index, running := range []int{0, 1} {
		fixture := newV0125CapabilityFixture(t, running, 256*1024)
		service, err := newDefaultAdmissionService(v0125CapabilityFactoryConfig(fixture.URL()))
		if err != nil {
			fixture.Close()
			t.Fatalf("construct automatic factory with running=%d: %v", running, err)
		}
		models, completions := fixture.Calls()
		telemetry := service.Snapshot(time.Now())
		profiles[index] = telemetry.CapabilityProfile
		reasons[index] = telemetry.CapabilityReason
		closeErr := service.Close()
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

func TestV0125AutomaticCapabilityBusyStartupAdmitsAndDrainsFittingRegularRequest(t *testing.T) {
	fixture := newV0125CapabilityFixture(t, 1, 256*1024)
	defer fixture.Close()
	cfg := v0125CapabilityFactoryConfig(fixture.URL())
	cfg.PredictiveAdmissionMode = "enforce"

	service, err := newDefaultAdmissionService(cfg)
	if err != nil {
		t.Fatalf("construct automatic factory against busy backend: %v", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			t.Errorf("close automatic factory against busy backend: %v", err)
		}
	}()
	runtime := service.(*admissionRuntime)

	decision := runtime.Decide(context.Background(), domainpredictive.RequestEstimate{
		SelectionInputTokens: 8 * 1024, MaximumSequenceInputTokens: 8 * 1024,
		KVReservationInputTokens:                8 * 1024,
		MaximumSequenceKVReservationInputTokens: 8 * 1024,
		BasePromptCount:                         1, DecodeSequences: 1,
	})
	if decision.Record.State.RawRunning != 1 {
		t.Fatalf("decision observation running=%d, want coherent busy startup base", decision.Record.State.RawRunning)
	}
	if !decision.Record.Admitted() || decision.Reservation == nil {
		t.Fatalf("busy-startup fitting decision=%+v, want immediate forward", decision)
	}
	if snapshot := runtime.Snapshot(time.Now()).Capacity.State; snapshot.LiveReservations != 1 ||
		snapshot.PendingPrefillSequences != 1 || snapshot.PendingPrefillTokens != 8*1024 {
		t.Fatalf("busy-startup reservation snapshot=%+v, want one unforwarded reservation", snapshot)
	}
	if !decision.Reservation.Terminate(coreadmission.TerminalCancel) {
		t.Fatalf("busy-startup fitting cancellation failed for decision=%+v", decision)
	}
	if snapshot := runtime.Snapshot(time.Now()).Capacity.State; snapshot.LiveReservations != 0 ||
		snapshot.PendingPrefillSequences != 0 || snapshot.PendingPrefillTokens != 0 {
		t.Fatalf("busy-startup fitting lifecycle leaked: %+v", snapshot)
	}
}

func TestV0125AutomaticCapabilityGeometry(t *testing.T) {
	tests := []struct {
		name          string
		maxModelLen   int64
		capacity      int64
		wantHard      int64
		wantMaxInput  int64
		wantRegular   int64
		wantExclusive int64
		wantQuiescent int64
		wantContended int64
		wantAggregate int64
		wantReason    string
	}{
		{name: "32K context", maxModelLen: 32 * 1024, capacity: 1_000_000, wantHard: 880_000, wantMaxInput: 32*1024 - 256, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantContended: 32*1024 - 256, wantAggregate: 32*1024 - 256, wantReason: "metadata"},
		{name: "256K context", maxModelLen: 256 * 1024, capacity: 1_000_000, wantHard: 880_000, wantMaxInput: 256*1024 - 256, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantContended: 64 * 1024, wantAggregate: 256*1024 - 256, wantReason: "metadata"},
		{name: "650K context", maxModelLen: 650 * 1024, capacity: 1_000_000, wantHard: 880_000, wantMaxInput: 650*1024 - 256, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantContended: 64 * 1024, wantAggregate: 256 * 1024, wantReason: "metadata"},
		{name: "KV limited", maxModelLen: 650 * 1024, capacity: 300_000, wantHard: 264_000, wantMaxInput: 264_000 - 256, wantRegular: 64 * 1024, wantExclusive: 256 * 1024, wantQuiescent: 512 * 1024, wantContended: 64 * 1024, wantAggregate: 256 * 1024, wantReason: "metadata"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var models atomic.Int64
			var completions atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/models":
					models.Add(1)
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
			assertV0125CapabilityProfile(
				t, initialization.Profile, "automatic",
				test.wantHard, test.maxModelLen, test.wantMaxInput,
				test.wantRegular, test.wantExclusive, test.wantQuiescent,
				test.wantContended, test.wantAggregate,
			)
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
	cfg.PredictiveMaxModelLenTokens = 650 * 1024
	cfg.PredictivePrefillRegularTokens = 32_768
	cfg.PredictivePrefillExclusiveTokens = 131_072
	cfg.PredictivePrefillQuiescentTokens = 262_144
	cfg.PredictivePrefillAggregateBudgetTokens = 196_608

	service, err := newDefaultAdmissionService(cfg)
	if err != nil {
		t.Fatalf("construct explicit capability factory: %v", err)
	}
	defer func() {
		if err := service.Close(); err != nil {
			t.Errorf("close explicit capability factory: %v", err)
		}
	}()
	if models, completions := fixture.Calls(); models != 0 || completions != 0 {
		t.Fatalf("explicit profile calls models/completions = %d/%d, want 0/0", models, completions)
	}
	telemetry := service.Snapshot(time.Now())
	assertV0125CapabilityProfile(
		t, telemetry.CapabilityProfile, "explicit",
		880_000, 650*1024, 650*1024-256,
		32_768, 131_072, 262_144, 32_768, 196_608,
	)
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
		{name: "max model length only", mutate: func(cfg *config) { cfg.PredictiveMaxModelLenTokens = 256 * 1024 }},
		{name: "ordering", mutate: func(cfg *config) {
			cfg.PredictiveMaxModelLenTokens = 256 * 1024
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
			if _, err := newDefaultAdmissionService(cfg); err == nil {
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

	_, err := initializePredictiveCapability(predictiveCapabilityInitializationConfig{
		UpstreamURL: origin.URL, RequestTimeout: 50 * time.Millisecond, KVHardRatio: 0.88,
	}, v0125CapabilityStartup(1_000_000))
	if err == nil {
		t.Fatal("metadata redirect unexpectedly produced an automatic capability profile")
	}
	if redirectCalls.Load() != 0 {
		t.Fatalf("capability client followed metadata redirect %d times", redirectCalls.Load())
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
	_, err := initializePredictiveCapability(predictiveCapabilityInitializationConfig{
		UpstreamURL: server.URL, RequestTimeout: 50 * time.Millisecond, KVHardRatio: 0.88,
	}, v0125CapabilityStartup(1_000_000))
	if err == nil {
		t.Fatal("oversized metadata unexpectedly produced an automatic capability profile")
	}
}

func TestV0125CapabilityMetadataFailuresDoNotGuessContext(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "non-success", statusCode: http.StatusServiceUnavailable, body: `{}`},
		{name: "malformed JSON", statusCode: http.StatusOK, body: `{`},
		{name: "missing model", statusCode: http.StatusOK, body: `{"object":"list","data":[]}`},
		{name: "ambiguous models", statusCode: http.StatusOK, body: `{"object":"list","data":[{"id":"vendor/capability-model","max_model_len":262144},{"id":"other","max_model_len":262144}]}`},
		{name: "identity mismatch", statusCode: http.StatusOK, body: `{"object":"list","data":[{"id":"other","max_model_len":262144}]}`},
		{name: "missing max model length", statusCode: http.StatusOK, body: `{"object":"list","data":[{"id":"vendor/capability-model"}]}`},
		{name: "negative max model length", statusCode: http.StatusOK, body: `{"object":"list","data":[{"id":"vendor/capability-model","max_model_len":-1}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/models" {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(test.statusCode)
				_, _ = io.WriteString(w, test.body)
			}))
			defer server.Close()

			_, err := initializePredictiveCapability(predictiveCapabilityInitializationConfig{
				UpstreamURL: server.URL, RequestTimeout: 50 * time.Millisecond, KVHardRatio: 0.88,
			}, v0125CapabilityStartup(1_000_000))
			if err == nil {
				t.Fatal("invalid metadata unexpectedly produced an automatic capability profile")
			}
		})
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
	wantHard, wantMaxModelLen, wantMaxInput int64,
	wantRegular, wantExclusive, wantQuiescent, wantContended, wantAggregate int64,
) {
	t.Helper()
	if profile.SchemaVersion != runtimepredictive.CapabilityProfileSchema || string(profile.Source) != wantSource ||
		profile.KVHardLimitTokens != wantHard || profile.MaxModelLenTokens != wantMaxModelLen ||
		profile.MaximumAdmissibleInputTokens != wantMaxInput || profile.PrefillRegularTokens != wantRegular ||
		profile.PrefillExclusiveTokens != wantExclusive || profile.PrefillQuiescentTokens != wantQuiescent ||
		profile.PrefillContendedBudgetTokens != wantContended ||
		profile.PrefillAggregateBudgetTokens != wantAggregate {
		t.Fatalf(
			"capability profile = %+v, want source=%s hard/model/input=%d/%d/%d prefill=%d/%d/%d budgets=%d/%d",
			profile, wantSource, wantHard, wantMaxModelLen, wantMaxInput,
			wantRegular, wantExclusive, wantQuiescent, wantContended, wantAggregate,
		)
	}
}

func v0125CapabilityStartup(capacity int64) predictiveBackendStartup {
	return predictiveBackendStartup{
		BackendKind: "vllm",
		modelName:   "vendor/capability-model", ModelIdentitySHA256: predictiveModelIdentitySHA256("vendor/capability-model"),
		CapacityTokens: capacity, BlockSize: 64,
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
# TYPE vllm:cache_config_info gauge
# TYPE vllm:kv_cache_usage_perc gauge
# TYPE vllm:num_requests_running gauge
# TYPE vllm:num_requests_waiting gauge
# TYPE vllm:num_preemptions_total counter
# TYPE vllm:generation_tokens_total counter
vllm:cache_config_info{block_size="64",kv_cache_size_tokens="1000000",num_gpu_blocks="15625"} 1
vllm:kv_cache_usage_perc 0
vllm:num_requests_running{model_name="vendor/capability-model",engine="0"} %d
vllm:num_requests_waiting{model_name="vendor/capability-model",engine="0"} 0
vllm:num_preemptions_total{model_name="vendor/capability-model",engine="0"} 0
vllm:generation_tokens_total{model_name="vendor/capability-model",engine="0"} 0
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
