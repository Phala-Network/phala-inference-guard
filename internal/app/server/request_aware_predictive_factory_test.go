package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestDefaultPredictiveFactoryUsesDeterministicRequestAwareStack(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `
vllm:cache_config_info{block_size="16",kv_cache_size_tokens="1000000",num_gpu_blocks="62500"} 1
vllm:kv_cache_usage_perc 0.10
vllm:num_requests_running{model_name="vendor/arbitrary-model-v17",engine="0"} 2
vllm:num_requests_waiting{model_name="vendor/arbitrary-model-v17",engine="0"} 0
vllm:num_preemptions_total{model_name="vendor/arbitrary-model-v17",engine="0"} 0
vllm:generation_tokens_total{model_name="vendor/arbitrary-model-v17",engine="0"} 100
`)
	}))
	defer metrics.Close()

	for _, mode := range []string{"shadow", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			cfg := requestAwareFactoryTestConfig(metrics.URL, mode)
			shadow, err := newDefaultPredictiveShadow(cfg)
			if err != nil {
				t.Fatalf("construct deterministic factory: %v", err)
			}
			adapter, ok := shadow.(*requestAwarePredictiveAdapter)
			if !ok {
				_ = shadow.Close()
				t.Fatalf("default predictive adapter = %T, want *requestAwarePredictiveAdapter", shadow)
			}
			observer, ok := adapter.snapshot.(*predictiveVLLMObserver)
			if !ok {
				_ = adapter.Close()
				t.Fatalf("request-aware snapshot = %T, want *predictiveVLLMObserver", adapter.snapshot)
			}
			if observer.pollInterval != 20*time.Millisecond || observer.maximumAge != 100*time.Millisecond {
				_ = adapter.Close()
				t.Fatalf(
					"observer cadence/freshness = %s/%s, want canonical 20ms/100ms",
					observer.pollInterval, observer.maximumAge,
				)
			}
			if adapter.mode != mode || adapter.manager == nil || adapter.policy == nil {
				_ = adapter.Close()
				t.Fatalf("deterministic adapter is incomplete: mode=%q manager=%p policy=%p", adapter.mode, adapter.manager, adapter.policy)
			}
			decision := adapter.Decide(context.Background(), "factory-prefill-"+mode, requestAwareAdapterInput(4*1024, 0))
			if decision.Outcome != predictiveAdmissionOutcomeRequestReject || decision.Reservation != nil ||
				decision.Reason != domainpredictive.ReasonRequestSizeAtPressure {
				_ = adapter.Close()
				t.Fatalf("factory did not wire custom quiescent prefill threshold: %+v", decision)
			}
			if err := adapter.Close(); err != nil {
				t.Fatalf("close deterministic adapter: %v", err)
			}
		})
	}
}

func requestAwareFactoryTestConfig(metricsURL, mode string) config {
	cfg := testProxyConfig(metricsURL)
	cfg.PredictiveAdmissionMode = mode
	cfg.PredictiveMetricsURL = metricsURL
	cfg.PredictiveObservationPollInterval = 20 * time.Millisecond
	cfg.PredictiveMaximumMetricsAge = 100 * time.Millisecond
	cfg.PredictiveMaxModelLenTokens = 16 * 1024
	cfg.PredictivePrefillRegularTokens = 1024
	cfg.PredictivePrefillExclusiveTokens = 2 * 1024
	cfg.PredictivePrefillQuiescentTokens = 3 * 1024
	cfg.PredictivePrefillAggregateBudgetTokens = 2 * 1024
	return cfg
}
