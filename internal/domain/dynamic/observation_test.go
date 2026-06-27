package dynamic

import (
	"testing"
	"time"

	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func TestObserveGenerationWindowUsesDirectBackendNormalization(t *testing.T) {
	observation := observeGenerationWindow(time.Unix(100, 0), telemetry.Sample{
		PreemptionDelta:       2,
		PreemptionDeltaDirect: true,
		GenerationTPS:         500,
		GenerationTPSDirect:   true,
	}, runtimedynamic.Snapshot{})

	if !observation.GenerationTPSValid {
		t.Fatalf("generation TPS valid = false, want true")
	}
	if observation.GenerationTPS != 500 {
		t.Fatalf("generation TPS = %.1f, want 500", observation.GenerationTPS)
	}
	if observation.PreemptionDelta != 2 {
		t.Fatalf("preemption delta = %d, want 2", observation.PreemptionDelta)
	}
}

func TestObserveGenerationWindowUsesRawCountersForStaticMetrics(t *testing.T) {
	now := time.Unix(100, 0)
	observation := observeGenerationWindow(now, telemetry.Sample{
		Preemptions: 12,
		Generation:  1250,
	}, runtimedynamic.Snapshot{
		Source:      "metrics",
		Updated:     now.Add(-time.Second),
		Preemptions: 10,
		Generation:  1000,
	})

	if !observation.GenerationTPSValid {
		t.Fatalf("generation TPS valid = false, want true")
	}
	if observation.GenerationTPS != 250 {
		t.Fatalf("generation TPS = %.1f, want 250", observation.GenerationTPS)
	}
	if observation.PreemptionDelta != 2 {
		t.Fatalf("preemption delta = %d, want 2", observation.PreemptionDelta)
	}
}
