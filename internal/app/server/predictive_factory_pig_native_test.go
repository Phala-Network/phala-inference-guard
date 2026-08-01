//go:build pig_native && cgo

package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDefaultNativePredictiveFactoryConstructsRealCountOnlyShadow(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `
vllm:cache_config_info{block_size="4",kv_cache_size_tokens="1000000",num_gpu_blocks="250000"} 1
vllm:kv_cache_usage_perc 0.10
vllm:num_requests_running{model_name="google/gemma-4-fixture",engine="0"} 1
vllm:num_requests_waiting{model_name="google/gemma-4-fixture",engine="0"} 0
vllm:num_preemptions_total{model_name="google/gemma-4-fixture",engine="0"} 0
vllm:generation_tokens_total{model_name="google/gemma-4-fixture",engine="0"} 100
`)
	}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "shadow"
	cfg.Backends[0].MetricsURL = backend.URL
	cfg.DynamicPollInterval = 9 * time.Second
	cfg.KVAdmissionPolicy.MaxMetricsAge = 27 * time.Second
	cfg.KVAdmissionPolicy.PreemptionCooldown = 45 * time.Second
	writePredictiveFactoryTestProfile(t, &cfg)
	srv, err := newProxyServer(cfg)
	if err != nil {
		t.Fatalf("newProxyServer with native predictive profile: %v", err)
	}
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close native predictive server: %v", err)
		}
	}()
	shadow, ok := srv.predictiveShadow.(*realPredictiveShadow)
	if !ok {
		t.Fatalf("default predictive shadow = %T, want *realPredictiveShadow", srv.predictiveShadow)
	}
	if shadow.upstream == nil {
		t.Fatal("default predictive shadow did not construct an upstream metrics observer")
	}
	observer, ok := shadow.upstream.(*predictiveVLLMObserver)
	if !ok {
		t.Fatalf("default predictive upstream = %T, want *predictiveVLLMObserver", shadow.upstream)
	}
	if observer.pollInterval != 250*time.Millisecond || observer.maximumAge != 750*time.Millisecond || observer.client.Timeout != 500*time.Millisecond || observer.preemptionCooldown != time.Second {
		t.Fatalf("observer timing = poll=%s age=%s timeout=%s cooldown=%s, want manifest-pinned 250ms/750ms/500ms/1s", observer.pollInterval, observer.maximumAge, observer.client.Timeout, observer.preemptionCooldown)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !shadow.upstream.Healthy(time.Now()) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !shadow.upstream.Healthy(time.Now()) {
		t.Fatal("default predictive upstream observer did not reconcile a fresh matching vLLM sample")
	}
}
