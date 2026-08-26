package server

import (
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

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
