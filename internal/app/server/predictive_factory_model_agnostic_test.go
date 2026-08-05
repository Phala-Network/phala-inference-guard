package server

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestDefaultPredictiveFactoryConstructsApproximateShadowWithoutModelAssets(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `
vllm:cache_config_info{block_size="4",kv_cache_size_tokens="1000000",num_gpu_blocks="250000"} 1
vllm:kv_cache_usage_perc 0
vllm:num_requests_running{model_name="vendor/arbitrary-model-v17",engine="0"} 0
vllm:num_requests_waiting{model_name="vendor/arbitrary-model-v17",engine="0"} 0
vllm:num_preemptions_total{model_name="vendor/arbitrary-model-v17",engine="0"} 0
vllm:generation_tokens_total{model_name="vendor/arbitrary-model-v17",engine="0"} 0
`)
	}))
	defer metrics.Close()

	cfg := testProxyConfig(metrics.URL)
	cfg.PredictiveAdmissionMode = "shadow"
	cfg.Backends[0].MetricsURL = metrics.URL
	cfg.DynamicMetricsURLs = []string{metrics.URL}
	cfg.DynamicPollInterval = 20 * time.Millisecond
	cfg.KVAdmissionPolicy.MaxMetricsAge = 100 * time.Millisecond
	cfg.KVAdmissionPolicy.PreemptionCooldown = 0

	srv, err := newProxyServer(cfg)
	if err != nil {
		t.Fatalf("construct model-neutral predictive server without assets: %v", err)
	}
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close model-neutral predictive server: %v", err)
		}
	}()
	shadow, ok := srv.predictiveShadow.(*approximatePredictiveShadow)
	if !ok {
		t.Fatalf("default predictive shadow = %T, want *approximatePredictiveShadow", srv.predictiveShadow)
	}
	deadline := time.Now().Add(time.Second)
	for shadow.upstream != nil && !shadow.upstream.Healthy(time.Now()) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if shadow.upstream == nil || !shadow.upstream.Healthy(time.Now()) {
		t.Fatal("model-neutral predictive observer did not become healthy from coherent vLLM metrics")
	}
	scheduler, ok := shadow.learner.(*runtimepredictive.LearnedScheduler)
	if !ok {
		t.Fatalf("model-neutral learner = %T, want *predictive.LearnedScheduler", shadow.learner)
	}
	identity := scheduler.Identity()
	if identity.PredictorVersion != predictiveApproximatePredictorVersion {
		t.Fatalf("model-neutral predictor identity = %+v, want factory identity %q", identity, predictiveApproximatePredictorVersion)
	}
	if predictiveApproximatePredictorVersion != "adaptive-tps-kv-v8" {
		t.Fatalf("model-neutral predictor version = %q, want isolated v0.10.11 hard-memory semantics", predictiveApproximatePredictorVersion)
	}
	state := domainpredictive.VirtualState{DecodeSequences: 1}
	small := domainpredictive.RequestCost{InputTokens: 100, UncachedPrefillUpper: 100, DecodeSequencesUpper: 1}
	large := domainpredictive.RequestCost{InputTokens: 5_000, UncachedPrefillUpper: 5_000, DecodeSequencesUpper: 1}
	smallPrediction := scheduler.Predict(time.Now(), state, small)
	largePrediction := scheduler.Predict(time.Now(), state, large)
	if largePrediction.Prior.ExistingUserTPSLower >= smallPrediction.Prior.ExistingUserTPSLower {
		t.Fatalf("production request size did not reduce existing-user TPS prior: small=%+v large=%+v", smallPrediction.Prior, largePrediction.Prior)
	}
}

func TestPredictiveVLLMStartupProbeRejectsAmbiguousModelIdentityWithinBound(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `
vllm:cache_config_info{block_size="4",kv_cache_size_tokens="1000000"} 1
vllm:kv_cache_usage_perc 0
vllm:num_requests_running{model_name="vendor/model-a",engine="0"} 0
vllm:num_requests_waiting{model_name="vendor/model-b",engine="0"} 0
vllm:num_preemptions_total{model_name="vendor/model-a",engine="0"} 0
vllm:generation_tokens_total{model_name="vendor/model-a",engine="0"} 0
`)
	}))
	defer metrics.Close()
	started := time.Now()
	_, err := probePredictiveVLLMStartup(predictiveVLLMStartupProbeConfig{
		MetricsURL: metrics.URL, StartupTimeout: time.Second,
		RequestTimeout: 250 * time.Millisecond, RetryInterval: 10 * time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "model identity") {
		t.Fatalf("ambiguous startup error = %v, want model identity failure", err)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("bounded startup probe took %s", elapsed)
	}
}

func TestPredictiveVLLMStartupProbeRetainsSemanticErrorAcrossLaterFetchTimeout(t *testing.T) {
	var requests atomic.Int64
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if requests.Add(1) == 1 {
			_, _ = fmt.Fprint(w, `
vllm:cache_config_info{block_size="4",kv_cache_size_tokens="1000000"} 1
vllm:kv_cache_usage_perc 0
vllm:num_requests_running{model_name="vendor/model-a",engine="0"} 0
vllm:num_requests_waiting{model_name="vendor/model-b",engine="0"} 0
vllm:num_preemptions_total{model_name="vendor/model-a",engine="0"} 0
vllm:generation_tokens_total{model_name="vendor/model-a",engine="0"} 0
`)
			return
		}
		<-request.Context().Done()
	}))
	defer metrics.Close()

	_, err := probePredictiveVLLMStartup(predictiveVLLMStartupProbeConfig{
		MetricsURL: metrics.URL, StartupTimeout: 200 * time.Millisecond,
		RequestTimeout: 50 * time.Millisecond, RetryInterval: time.Millisecond,
	})
	if err == nil || !strings.Contains(err.Error(), "model identity") || !strings.Contains(err.Error(), "last fetch error") {
		t.Fatalf("startup error lost semantic or fetch evidence: %v", err)
	}
}

func TestPredictiveProtectedTokensUsesObservedCapacityAndBlockAlignment(t *testing.T) {
	protected, err := predictiveProtectedTokens(1_003, 64, 0.84)
	if err != nil {
		t.Fatalf("derive protected tokens: %v", err)
	}
	if protected != 832 {
		t.Fatalf("protected tokens = %d, want floor(floor(1003*0.84)/64)*64 = 832", protected)
	}
}

func TestPredictivePrefillTPSPenaltyUsesProtectedCapacityScale(t *testing.T) {
	const (
		baseTPS   = 50.0
		targetTPS = 25.0
		protected = int64(900_000)
	)
	penalty := predictivePrefillTPSPenaltyPerKToken(baseTPS, targetTPS, protected)
	usedHeadroom := penalty * float64(protected/predictivePrefillHeadroomSafetyShares) / 1_000
	if math.Abs(usedHeadroom-(baseTPS-targetTPS)) > 1e-9 {
		t.Fatalf("prefill penalty consumed %.6f TPS at one safety share, want %.6f", usedHeadroom, baseTPS-targetTPS)
	}
	featureBucket := predictiveFeatureTokenBucket(protected, int(simulationCompatibleBlockSizeForTest))
	if featureBucket >= protected || penalty >= predictivePrefillTPSPenaltyPerKToken(baseTPS, targetTPS, featureBucket) {
		t.Fatalf("prefill penalty still appears feature-bucket-scaled: protected=%d bucket=%d penalty=%.9f", protected, featureBucket, penalty)
	}
}

const simulationCompatibleBlockSizeForTest = 64
