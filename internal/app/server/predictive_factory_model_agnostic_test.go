package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func TestPredictiveProtectedTokensUsesObservedCapacityAndBlockAlignment(t *testing.T) {
	protected, err := predictiveProtectedTokens(1_003, 64, 0.84)
	if err != nil {
		t.Fatalf("derive protected tokens: %v", err)
	}
	if protected != 832 {
		t.Fatalf("protected tokens = %d, want floor(floor(1003*0.84)/64)*64 = 832", protected)
	}
}
