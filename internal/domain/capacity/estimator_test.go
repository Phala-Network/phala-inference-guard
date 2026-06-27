package capacity

import "testing"

func TestEstimateCleanCapacityRepresentativeSafeLimit(t *testing.T) {
	cfg := cleanTestConfig()
	cfg.CapacitySafetyRatio = 0.50
	estimate := EstimateCleanCapacity(EstimateInput{
		Config:             cfg,
		BaseLimit:          100,
		GenerationTPS:      1000,
		GenerationTPSValid: true,
		Running:            20,
		DecodeRunning:      20,
	})

	if estimate.RawLimit != 40 {
		t.Fatalf("raw limit = %d, want 40", estimate.RawLimit)
	}
	if estimate.SafeLimit != 20 {
		t.Fatalf("safe limit = %d, want 20", estimate.SafeLimit)
	}
	if estimate.Confidence != "representative" {
		t.Fatalf("confidence = %q, want representative", estimate.Confidence)
	}
	if !estimate.RepresentativeLoad {
		t.Fatalf("representative load = false, want true")
	}
}

func TestEstimateCleanCapacityStaleSignalUsesPreviousTPSConservatively(t *testing.T) {
	cfg := cleanTestConfig()
	cfg.CapacitySafetyRatio = 0.50
	estimate := EstimateCleanCapacity(EstimateInput{
		Config:    cfg,
		Previous:  Previous{Source: "metrics", CapacityTPS: 500, CapacityLearnedLimit: 12},
		BaseLimit: 100,
		Running:   1,
	})

	if estimate.SmoothedTPS != 500 {
		t.Fatalf("smoothed TPS = %.1f, want 500", estimate.SmoothedTPS)
	}
	if estimate.RawLimit != 20 {
		t.Fatalf("raw limit = %d, want 20", estimate.RawLimit)
	}
	if estimate.SafeLimit != 10 {
		t.Fatalf("safe limit = %d, want 10", estimate.SafeLimit)
	}
	if estimate.LowConfidenceLimit != 10 {
		t.Fatalf("low-confidence limit = %d, want 10", estimate.LowConfidenceLimit)
	}
	if estimate.Confidence != "stale" {
		t.Fatalf("confidence = %q, want stale", estimate.Confidence)
	}
}

func TestEstimateCleanCapacityPrefillPreservesPreviousTPS(t *testing.T) {
	cfg := cleanTestConfig()
	cfg.CapacitySafetyRatio = 1
	estimate := EstimateCleanCapacity(EstimateInput{
		Config:             cfg,
		Previous:           Previous{Source: "metrics", CapacityTPS: 500, CapacityLearnedLimit: 20},
		BaseLimit:          100,
		GenerationTPS:      100,
		GenerationTPSValid: true,
		Running:            20,
		DecodeRunning:      0,
		PrefillTransition:  true,
	})

	if estimate.SmoothedTPS != 500 {
		t.Fatalf("smoothed TPS = %.1f, want preserved 500", estimate.SmoothedTPS)
	}
	if estimate.SafeLimit != 20 {
		t.Fatalf("safe limit = %d, want 20", estimate.SafeLimit)
	}
	if estimate.Confidence != "prefill" {
		t.Fatalf("confidence = %q, want prefill", estimate.Confidence)
	}
}
