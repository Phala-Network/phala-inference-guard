package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func TestPredictiveStartupAcceptsCoherentSGLangSample(t *testing.T) {
	sample := telemetry.Sample{
		BackendKind: "sglang", ModelName: "meta/sglang-model", ModelNameValid: true,
		KVCapacityTokens: 1_000_000, KVBlockSize: 16, KVBlockSizeValid: true,
		KVUsedTokens: 100_000, KVTokenMetricsValid: true,
		Running: 2, RunningValid: true, Waiting: 1, WaitingValid: true,
		Preemptions: 3, PreemptionsValid: true,
		Generation: 100, GenerationValid: true,
	}
	startup, err := predictiveVLLMStartupFromSample(sample, time.Unix(100, 0))
	if err != nil {
		t.Fatalf("coherent SGLang startup rejected: %v", err)
	}
	if startup.modelName != "meta/sglang-model" || startup.CapacityTokens != 1_000_000 || startup.BlockSize != 16 ||
		startup.Generation != 100 || startup.Preemptions != 3 {
		t.Fatalf("SGLang startup=%#v", startup)
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
