package backend

import (
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func TestNormalizeSampleUsesPerBackendCounterDeltas(t *testing.T) {
	now := time.Unix(100, 0)
	previous := Runtime{
		Name:        "backend-a",
		Generation:  1000,
		Preemptions: 5,
		Updated:     now.Add(-time.Second),
	}
	sample := telemetry.Sample{
		Running:     8,
		Preemptions: 7,
		Generation:  1250,
	}

	status := FromSample("backend-a", sample, previous, now)
	normalized := NormalizeSample(sample, status)

	if !normalized.GenerationTPSDirect {
		t.Fatalf("generation TPS direct = false, want true")
	}
	if normalized.GenerationTPS != 250 {
		t.Fatalf("generation TPS = %.1f, want 250", normalized.GenerationTPS)
	}
	if !normalized.PreemptionDeltaDirect {
		t.Fatalf("preemption delta direct = false, want true")
	}
	if normalized.PreemptionDelta != 2 {
		t.Fatalf("preemption delta = %d, want 2", normalized.PreemptionDelta)
	}
	if normalized.Generation != sample.Generation || normalized.Preemptions != sample.Preemptions {
		t.Fatalf("normalized sample should preserve cumulative counters")
	}
}

func TestNormalizeSampleRejectsPerBackendCounterReset(t *testing.T) {
	now := time.Unix(100, 0)
	previous := Runtime{
		Name:        "backend-a",
		Generation:  1000,
		Preemptions: 5,
		Updated:     now.Add(-time.Second),
	}
	sample := telemetry.Sample{
		Running:     8,
		Preemptions: 1,
		Generation:  10,
	}

	status := FromSample("backend-a", sample, previous, now)
	normalized := NormalizeSample(sample, status)

	if normalized.GenerationTPSDirect {
		t.Fatalf("generation TPS direct = true after counter reset, want false")
	}
	if normalized.GenerationTPS != 0 {
		t.Fatalf("generation TPS = %.1f, want 0", normalized.GenerationTPS)
	}
	if normalized.PreemptionDeltaDirect {
		t.Fatalf("preemption delta direct = true after counter reset, want false")
	}
	if normalized.PreemptionDelta != 0 {
		t.Fatalf("preemption delta = %d, want 0", normalized.PreemptionDelta)
	}
}
