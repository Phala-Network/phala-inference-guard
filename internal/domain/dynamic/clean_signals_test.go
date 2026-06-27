package dynamic

import (
	"testing"
	"time"

	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
)

func TestMetricsCounterDeltaPreservesResetFallback(t *testing.T) {
	if got := metricsCounterDelta(125, 100, "metrics"); got != 25 {
		t.Fatalf("counter delta = %d, want 25", got)
	}
	if got := metricsCounterDelta(5, 100, "metrics"); got != 5 {
		t.Fatalf("counter reset delta = %d, want current counter fallback 5", got)
	}
	if got := metricsCounterDelta(125, 100, ""); got != 125 {
		t.Fatalf("non-metrics delta = %d, want current counter 125", got)
	}
}

func TestDeriveCleanPrefillStateSeparatesFreezeFromSettling(t *testing.T) {
	cfg := cleanEvaluateConfig()
	generation := generationObservation{GenerationTPS: 200, GenerationTPSValid: true}

	freeze := deriveCleanPrefillState(cfg, 20, 0, 8, runtimedynamic.Snapshot{}, generation)
	if !freeze.Freeze || !freeze.Transition || freeze.Settling {
		t.Fatalf("freeze state = %#v, want freeze transition without settling", freeze)
	}
	if freeze.Protected != 8 || freeze.DecodeRunning != 12 {
		t.Fatalf("freeze protected/decode = %d/%d, want 8/12", freeze.Protected, freeze.DecodeRunning)
	}

	settling := deriveCleanPrefillState(cfg, 20, 0, 1, runtimedynamic.Snapshot{
		PrefillTransition: true,
		Updated:           time.Unix(100, 0),
	}, generationObservation{GenerationTPS: 200, GenerationTPSValid: true})
	if settling.Freeze || !settling.Transition || !settling.Settling {
		t.Fatalf("settling state = %#v, want settling transition without freeze", settling)
	}
}
