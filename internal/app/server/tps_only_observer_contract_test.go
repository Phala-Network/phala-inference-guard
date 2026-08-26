package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func TestTPSOnlyDefaultFactoryDoesNotProbeModelMetadataOrRequireKVTelemetry(t *testing.T) {
	var upstreamCalls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		upstreamCalls.Add(1)
		http.Error(w, "metadata unavailable", http.StatusServiceUnavailable)
	}))
	t.Cleanup(upstream.Close)

	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `
# TYPE vllm:num_requests_running gauge
# TYPE vllm:num_requests_waiting gauge
# TYPE vllm:num_preemptions_total counter
# TYPE vllm:generation_tokens_total counter
vllm:num_requests_running{model_name="vendor/model",engine="0"} 0
vllm:num_requests_waiting{model_name="vendor/model",engine="0"} 0
vllm:num_preemptions_total{model_name="vendor/model",engine="0"} 0
vllm:generation_tokens_total{model_name="vendor/model",engine="0"} 1
`)
	}))
	t.Cleanup(metrics.Close)

	cfg := testProxyConfig(upstream.URL)
	cfg.PredictiveMetricsURL = metrics.URL
	cfg.PredictiveStartupProbeTimeout = time.Second
	cfg.PredictiveMetricsRequestTimeout = 250 * time.Millisecond
	cfg.PredictiveObservationPollInterval = 500 * time.Millisecond
	cfg.PredictiveMaximumMetricsAge = 1500 * time.Millisecond
	service, err := newDefaultAdmissionService(cfg)
	if err != nil {
		t.Fatalf("TPS-only factory retained a capability dependency: %v", err)
	}
	t.Cleanup(func() { _ = service.Close() })
	if calls := upstreamCalls.Load(); calls != 0 {
		t.Fatalf("TPS-only factory made %d model metadata calls", calls)
	}
}

func TestTPSOnlyServerStartupDoesNotRequireKVTelemetry(t *testing.T) {
	at := time.Unix(40_000, 0)
	startup, err := predictiveBackendStartupFromSample(telemetry.Sample{
		BackendKind:      "vllm",
		ModelName:        "vendor/model",
		ModelNameValid:   true,
		Running:          2,
		RunningValid:     true,
		Waiting:          0,
		WaitingValid:     true,
		Preemptions:      3,
		PreemptionsValid: true,
		Generation:       100,
		GenerationValid:  true,
	}, at)
	if err != nil {
		t.Fatalf("TPS startup retained a KV telemetry dependency: %v", err)
	}
	if startup.BackendKind != "vllm" || startup.ModelIdentitySHA256 == "" ||
		startup.Running != 2 || startup.Generation != 100 || startup.ObservedAt != at {
		t.Fatalf("TPS startup normalization=%+v", startup)
	}
}

func TestTPSOnlyServerObserverDoesNotRequireKVTelemetry(t *testing.T) {
	at := time.Unix(40_100, 0)
	fingerprint := predictiveModelIdentitySHA256("vendor/model")
	observer := &admissionBackendObserver{
		backendKind:           "sglang",
		capabilityFingerprint: fingerprint,
		maximumAge:            time.Second,
	}
	observation, disposition := observer.observation(telemetry.Sample{
		BackendKind:      "sglang",
		ModelName:        "vendor/model",
		ModelNameValid:   true,
		Running:          3,
		RunningValid:     true,
		Waiting:          1,
		WaitingValid:     true,
		Preemptions:      4,
		PreemptionsValid: true,
		Generation:       200,
		GenerationValid:  true,
	}, at)
	if disposition != admissionSampleUsable {
		t.Fatalf("TPS observer retained a KV telemetry dependency: disposition=%d observation=%+v", disposition, observation)
	}
	if observation != (coreadmission.BackendObservation{
		CapabilityFingerprint: fingerprint,
		ObservedAt:            at,
		MaximumAge:            time.Second,
		Running:               3,
		Waiting:               1,
		GenerationTokensTotal: 200,
		PreemptionsTotal:      4,
	}) {
		t.Fatalf("TPS observation normalization=%+v", observation)
	}
}
