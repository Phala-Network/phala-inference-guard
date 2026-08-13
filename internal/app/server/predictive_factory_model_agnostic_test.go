package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

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
