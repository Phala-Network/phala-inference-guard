package capacity

import "testing"

func cleanTestConfig() Config {
	return Config{
		UserTPSEnabled:      true,
		UserTPSYellow:       25,
		UserTPSRed:          20,
		UserTPSMinRun:       1,
		CapacityLearn:       true,
		CapacitySafetyRatio: 1,
		CapacityStepUp:      0.10,
		CapacityHealthyN:    2,
		CapacityHealthyMul:  1.20,
		PressureEnabled:     true,
		PressureHeadroom:    1,
		PressureMinLimit:    1,
		PressureLearnRatio:  0.75,
		PressureLearnMinRun: 8,
		KVYellow:            0.70,
		KVRed:               0.80,
		WaitingYellow:       1,
		WaitingRed:          2,
	}
}

func TestCleanLearnCapHoldsUnderutilizedHealthyTraffic(t *testing.T) {
	result := CleanLearnCap(CleanLearnInput{
		Config:             cleanTestConfig(),
		Previous:           Previous{Source: "metrics", CapacityLearnedLimit: 10, CapacityTargetLimit: 20},
		BaseLimit:          100,
		GenerationTPSValid: true,
		CapacityTPS:        1000,
		Running:            1,
		DecodeRunning:      1,
		UserTPS:            1000,
		TTFTHealthy:        true,
	})

	if result.LearnedLimit != 10 {
		t.Fatalf("learned limit = %d, want 10", result.LearnedLimit)
	}
	if result.State != "hold_underutilized" {
		t.Fatalf("state = %q, want hold_underutilized", result.State)
	}
	if result.Reason != "insufficient_representative_load" {
		t.Fatalf("reason = %q, want insufficient_representative_load", result.Reason)
	}
	if result.TargetReason != "smoothed_tps" {
		t.Fatalf("target reason = %q, want smoothed_tps", result.TargetReason)
	}
}

func TestCleanLearnCapHoldsHealthyRepresentativeLoadWithoutPressure(t *testing.T) {
	result := CleanLearnCap(CleanLearnInput{
		Config: cleanTestConfig(),
		Previous: Previous{
			Source:                    "metrics",
			CapacityLearnedLimit:      10,
			CapacityTargetLimit:       40,
			CapacityRatioHealthyCount: 1,
		},
		BaseLimit:          100,
		GenerationTPSValid: true,
		CapacityTPS:        1000,
		Running:            10,
		DecodeRunning:      10,
		UserTPS:            100,
		TTFTHealthy:        true,
	})

	if result.LearnedLimit != 10 {
		t.Fatalf("learned limit = %d, want 10", result.LearnedLimit)
	}
	if result.State != "green_hold" {
		t.Fatalf("state = %q, want green_hold", result.State)
	}
	if result.Reason != "healthy_without_pressure" {
		t.Fatalf("reason = %q, want healthy_without_pressure", result.Reason)
	}
	if result.TargetReason != "smoothed_tps" {
		t.Fatalf("target reason = %q, want smoothed_tps", result.TargetReason)
	}
	if result.ProjectedLimit != 40 {
		t.Fatalf("projected limit = %d, want 40", result.ProjectedLimit)
	}
}

func TestCleanLearnCapProbesUpUnderHealthyDemandPressure(t *testing.T) {
	result := CleanLearnCap(CleanLearnInput{
		Config: cleanTestConfig(),
		Previous: Previous{
			Source:                    "metrics",
			CapacityLearnedLimit:      10,
			CapacityTargetLimit:       40,
			CapacityRatioHealthyCount: 1,
		},
		BaseLimit:          100,
		GenerationTPSValid: true,
		CapacityTPS:        1000,
		Running:            10,
		DecodeRunning:      10,
		UserTPS:            100,
		TTFTHealthy:        true,
		DynamicRejected:    1,
	})

	if result.LearnedLimit != 11 {
		t.Fatalf("learned limit = %d, want 11", result.LearnedLimit)
	}
	if result.State != "probe_up" {
		t.Fatalf("state = %q, want probe_up", result.State)
	}
	if result.Reason != "healthy_window_satisfied" {
		t.Fatalf("reason = %q, want healthy_window_satisfied", result.Reason)
	}
	if result.TargetReason != "smoothed_tps" {
		t.Fatalf("target reason = %q, want smoothed_tps", result.TargetReason)
	}
	if result.ProjectedLimit != 40 {
		t.Fatalf("projected limit = %d, want 40", result.ProjectedLimit)
	}
}

func TestCleanLearnCapProbesUpUnderExplicitDemandPressure(t *testing.T) {
	result := CleanLearnCap(CleanLearnInput{
		Config: cleanTestConfig(),
		Previous: Previous{
			Source:                    "metrics",
			CapacityLearnedLimit:      10,
			CapacityTargetLimit:       40,
			CapacityRatioHealthyCount: 1,
		},
		BaseLimit:          100,
		GenerationTPSValid: true,
		CapacityTPS:        1000,
		Running:            10,
		DecodeRunning:      10,
		UserTPS:            100,
		TTFTHealthy:        true,
		DemandPressure:     true,
	})

	if result.LearnedLimit != 11 {
		t.Fatalf("learned limit = %d, want 11", result.LearnedLimit)
	}
	if result.State != "probe_up" {
		t.Fatalf("state = %q, want probe_up", result.State)
	}
	if result.Reason != "healthy_window_satisfied" {
		t.Fatalf("reason = %q, want healthy_window_satisfied", result.Reason)
	}
	if result.TargetReason != "smoothed_tps" {
		t.Fatalf("target reason = %q, want smoothed_tps", result.TargetReason)
	}
}

func TestCleanLearnCapLearnsDownOnRepresentativeBadQoS(t *testing.T) {
	result := CleanLearnCap(CleanLearnInput{
		Config:             cleanTestConfig(),
		Previous:           Previous{Source: "metrics", CapacityLearnedLimit: 20, CapacityTargetLimit: 20},
		BaseLimit:          100,
		GenerationTPSValid: true,
		CapacityTPS:        250,
		Running:            20,
		DecodeRunning:      20,
		UserTPS:            12.5,
		QOSYellowReady:     true,
		QOSRedReady:        true,
		TTFTHealthy:        true,
	})

	if result.LearnedLimit != 10 {
		t.Fatalf("learned limit = %d, want 10", result.LearnedLimit)
	}
	if result.State != "pig_down" {
		t.Fatalf("state = %q, want pig_down", result.State)
	}
	if result.Reason != "pig_below_target" {
		t.Fatalf("reason = %q, want pig_below_target", result.Reason)
	}
	if result.TargetReason != "smoothed_tps" {
		t.Fatalf("target reason = %q, want smoothed_tps", result.TargetReason)
	}
}

func TestCleanLearnCapWaitingDoesNotZeroLearnedCapacity(t *testing.T) {
	result := CleanLearnCap(CleanLearnInput{
		Config:             cleanTestConfig(),
		Previous:           Previous{Source: "metrics", CapacityLearnedLimit: 20, CapacityTargetLimit: 30},
		BaseLimit:          100,
		GenerationTPSValid: true,
		CapacityTPS:        800,
		Running:            20,
		DecodeRunning:      20,
		Waiting:            1,
		UserTPS:            40,
		TTFTHealthy:        true,
	})

	if result.LearnedLimit != 20 {
		t.Fatalf("learned limit = %d, want 20", result.LearnedLimit)
	}
	if result.State != "pressure_hold" {
		t.Fatalf("state = %q, want pressure_hold", result.State)
	}
	if result.Reason != "pressure_not_representative" {
		t.Fatalf("reason = %q, want pressure_not_representative", result.Reason)
	}
}

func TestCleanLearnCapUsesLowConfidenceEstimateBound(t *testing.T) {
	result := CleanLearnCap(CleanLearnInput{
		Config:    cleanTestConfig(),
		Previous:  Previous{Source: "metrics", CapacityLearnedLimit: 10, CapacityTargetLimit: 10},
		BaseLimit: 100,
		Estimate: EstimateResult{
			SafeLimit:          40,
			LowConfidenceLimit: 10,
			Confidence:         "sparse",
		},
		GenerationTPSValid: true,
		CapacityTPS:        1000,
		Running:            1,
		DecodeRunning:      1,
		UserTPS:            1000,
		TTFTHealthy:        true,
	})

	if result.TargetLimit != 10 {
		t.Fatalf("target limit = %d, want low-confidence bound 10", result.TargetLimit)
	}
	if result.LearnedLimit != 10 {
		t.Fatalf("learned limit = %d, want 10", result.LearnedLimit)
	}
	if result.TargetReason != "low_confidence_bound" {
		t.Fatalf("target reason = %q, want low_confidence_bound", result.TargetReason)
	}
	if result.ProjectedLimit != 10 {
		t.Fatalf("projected limit = %d, want 10", result.ProjectedLimit)
	}
}

func TestCleanLearnCapAppliesSparseProbeFloorAfterLowConfidenceBound(t *testing.T) {
	result := CleanLearnCap(CleanLearnInput{
		Config:    cleanTestConfig(),
		Previous:  Previous{Source: "metrics", CapacityLearnedLimit: 1, CapacityTargetLimit: 1},
		BaseLimit: 100,
		Estimate: EstimateResult{
			SafeLimit:          80,
			LowConfidenceLimit: 1,
			Confidence:         "sparse",
		},
		GenerationTPSValid: true,
		Running:            1,
		DecodeRunning:      1,
		UserTPS:            50,
		TTFTHealthy:        true,
	})

	if result.LearnedLimit != 1 {
		t.Fatalf("learned limit = %d, want 1", result.LearnedLimit)
	}
	if result.TargetLimit != sparseProbeFloor {
		t.Fatalf("target limit = %d, want sparse probe floor %d", result.TargetLimit, sparseProbeFloor)
	}
	if result.State != "sparse_probe" {
		t.Fatalf("state = %q, want sparse_probe", result.State)
	}
	if result.Reason != "low_traffic_probe_floor" {
		t.Fatalf("reason = %q, want low_traffic_probe_floor", result.Reason)
	}
	if result.TargetReason != "sparse_probe_floor" {
		t.Fatalf("target reason = %q, want sparse_probe_floor", result.TargetReason)
	}
	if result.ProjectedLimit != 1 {
		t.Fatalf("projected limit = %d, want low-confidence projection 1", result.ProjectedLimit)
	}
}

func TestCleanLearnCapRecoversSparseLearnedLimitUnderRepresentativeHealthyLoad(t *testing.T) {
	cfg := cleanTestConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.CapacitySafetyRatio = 0.42

	result := CleanLearnCap(CleanLearnInput{
		Config: cfg,
		Previous: Previous{
			Source:               "metrics",
			CapacityLearnedLimit: 3,
			CapacityTargetLimit:  16,
			CapacityLearnState:   "sparse_probe",
		},
		BaseLimit: 33,
		Estimate: EstimateResult{
			RawLimit:           21,
			SafeLimit:          8,
			Confidence:         "representative",
			RepresentativeLoad: true,
		},
		GenerationTPSValid: true,
		Running:            10,
		DecodeRunning:      10,
		UserTPS:            115,
		TTFTHealthy:        true,
		DynamicRejected:    2053,
	})

	if result.LearnedLimit != 18 {
		t.Fatalf("learned limit = %d, want demand-backed healthy observed recovery 18", result.LearnedLimit)
	}
	if result.TargetLimit != 18 {
		t.Fatalf("target limit = %d, want demand-backed healthy observed target 18", result.TargetLimit)
	}
	if result.State != "sparse_recovery" {
		t.Fatalf("state = %q, want sparse_recovery", result.State)
	}
	if result.Reason != "representative_sparse_recovery" {
		t.Fatalf("reason = %q, want representative_sparse_recovery", result.Reason)
	}
	if result.TargetReason != "healthy_observed_load" {
		t.Fatalf("target reason = %q, want healthy_observed_load", result.TargetReason)
	}
	if result.ProjectedLimit != 18 {
		t.Fatalf("projected limit = %d, want demand-backed healthy observed projection 18", result.ProjectedLimit)
	}
}

func TestCleanLearnCapHoldsHealthyObservedLoadWithoutDemandPressure(t *testing.T) {
	cfg := cleanTestConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.CapacitySafetyRatio = 0.42

	result := CleanLearnCap(CleanLearnInput{
		Config: cfg,
		Previous: Previous{
			Source:               "metrics",
			CapacityLearnedLimit: 3,
			CapacityTargetLimit:  3,
		},
		BaseLimit: 33,
		Estimate: EstimateResult{
			RawLimit:           21,
			SafeLimit:          8,
			Confidence:         "representative",
			RepresentativeLoad: true,
		},
		GenerationTPSValid: true,
		Running:            10,
		DecodeRunning:      10,
		UserTPS:            115,
		TTFTHealthy:        true,
	})

	if result.LearnedLimit != 3 {
		t.Fatalf("learned limit = %d, want 3", result.LearnedLimit)
	}
	if result.TargetLimit != 14 {
		t.Fatalf("target limit = %d, want healthy observed target 14", result.TargetLimit)
	}
	if result.State != "green_hold" {
		t.Fatalf("state = %q, want green_hold", result.State)
	}
	if result.Reason != "healthy_without_pressure" {
		t.Fatalf("reason = %q, want healthy_without_pressure", result.Reason)
	}
	if result.TargetReason != "healthy_observed_load" {
		t.Fatalf("target reason = %q, want healthy_observed_load", result.TargetReason)
	}
	if result.ProjectedLimit != 14 {
		t.Fatalf("projected limit = %d, want healthy observed projection 14", result.ProjectedLimit)
	}
}
