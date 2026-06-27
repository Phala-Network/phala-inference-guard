package dynamic

import (
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/tier"
	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func TestCleanPipelineThroughputLimitIsFinalEnforcerComponent(t *testing.T) {
	now := time.Unix(200, 0)
	snapshot := Evaluate(cleanEvaluateConfig(), Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:    20,
			Generation: 750,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:               "metrics",
				Updated:              now.Add(-time.Second),
				Generation:           500,
				CapacityTPS:          500,
				CapacityLearnedLimit: 20,
				CapacityTargetLimit:  20,
				GlobalLimit:          20,
				CapacityLimit:        20,
			},
		},
		GlobalLimit: 100,
	})

	if snapshot.GlobalLimit != 10 {
		t.Fatalf("global limit = %d, want throughput-enforced 10", snapshot.GlobalLimit)
	}
	if snapshot.CapacityLearnState != "pig_down" {
		t.Fatalf("capacity learn state = %q, want pig_down", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnReason != "pig_below_target" {
		t.Fatalf("capacity learn reason = %q, want pig_below_target", snapshot.CapacityLearnReason)
	}
	if snapshot.CapacityTargetReason != "estimate_safe" {
		t.Fatalf("capacity target reason = %q, want estimate_safe", snapshot.CapacityTargetReason)
	}
	if snapshot.CapacityProjectedLimit != 10 {
		t.Fatalf("capacity projected limit = %d, want 10", snapshot.CapacityProjectedLimit)
	}
	if snapshot.ThroughputLimit != 10 {
		t.Fatalf("throughput limit = %d, want 10", snapshot.ThroughputLimit)
	}
	if snapshot.FinalLimitReason != "throughput" {
		t.Fatalf("final limit reason = %q, want throughput", snapshot.FinalLimitReason)
	}
	if snapshot.CapacityRawLimit != 10 || snapshot.CapacitySafeLimit != 10 {
		t.Fatalf("capacity estimate raw/safe = %d/%d, want 10/10", snapshot.CapacityRawLimit, snapshot.CapacitySafeLimit)
	}
	if snapshot.CapacityEstimateConfidence != "representative" {
		t.Fatalf("capacity estimate confidence = %q, want representative", snapshot.CapacityEstimateConfidence)
	}
	if !snapshot.CapacityRepresentativeLoad || !snapshot.RepresentativeUserTPSLoad {
		t.Fatalf("representative capacity/user load = %t/%t, want true/true", snapshot.CapacityRepresentativeLoad, snapshot.RepresentativeUserTPSLoad)
	}
}

func TestCleanPipelineThroughputLimitStillWinsWhenCapacityLearningDisabled(t *testing.T) {
	now := time.Unix(250, 0)
	cfg := cleanEvaluateConfig()
	cfg.Capacity.CapacityLearn = false
	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:    20,
			Generation: 750,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:      "metrics",
				Updated:     now.Add(-time.Second),
				Generation:  500,
				GlobalLimit: 100,
			},
		},
		GlobalLimit: 100,
	})

	if snapshot.GlobalLimit != 10 {
		t.Fatalf("global limit = %d, want inherited QoS throughput limit 10", snapshot.GlobalLimit)
	}
	if snapshot.ThroughputLimit != 10 {
		t.Fatalf("throughput limit = %d, want inherited QoS throughput limit 10", snapshot.ThroughputLimit)
	}
	if snapshot.FinalLimitReason != "throughput" {
		t.Fatalf("final limit reason = %q, want throughput", snapshot.FinalLimitReason)
	}
	if snapshot.CapacityLearnState != "disabled" {
		t.Fatalf("capacity learn state = %q, want disabled", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnReason != "disabled" {
		t.Fatalf("capacity learn reason = %q, want disabled", snapshot.CapacityLearnReason)
	}
}

func TestCleanPipelineUserTPSDisabledStillExplainsThroughputStage(t *testing.T) {
	now := time.Unix(275, 0)
	cfg := cleanEvaluateConfig()
	cfg.UserTPSEnabled = false
	cfg.Capacity.UserTPSEnabled = false
	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:    20,
			Generation: 750,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:     "metrics",
				Updated:    now.Add(-time.Second),
				Generation: 500,
			},
		},
		GlobalLimit: 100,
	})

	if snapshot.GlobalLimit != 100 {
		t.Fatalf("global limit = %d, want 100", snapshot.GlobalLimit)
	}
	if snapshot.ThroughputLimit != 100 || snapshot.CapacityLimit != 100 {
		t.Fatalf("throughput/capacity limit = %d/%d, want 100/100", snapshot.ThroughputLimit, snapshot.CapacityLimit)
	}
	if snapshot.CapacityLearnedLimit != 100 || snapshot.CapacityTargetLimit != 100 {
		t.Fatalf("capacity learned/target = %d/%d, want 100/100", snapshot.CapacityLearnedLimit, snapshot.CapacityTargetLimit)
	}
	if snapshot.CapacityLearnState != "disabled" {
		t.Fatalf("capacity learn state = %q, want disabled", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnReason != "disabled" {
		t.Fatalf("capacity learn reason = %q, want disabled", snapshot.CapacityLearnReason)
	}
	if snapshot.CapacityTargetReason != "base_limit" {
		t.Fatalf("capacity target reason = %q, want base_limit", snapshot.CapacityTargetReason)
	}
	if snapshot.CapacityEstimateConfidence != "none" {
		t.Fatalf("capacity estimate confidence = %q, want none", snapshot.CapacityEstimateConfidence)
	}
}

func TestCleanPipelineAvailabilityLimitClosesFinalIntake(t *testing.T) {
	now := time.Unix(300, 0)
	snapshot := Evaluate(cleanEvaluateConfig(), Input{
		Now:           now,
		BackendFailed: 1,
		GlobalLimit:   100,
	})

	if snapshot.GlobalLimit != 0 {
		t.Fatalf("global limit = %d, want 0", snapshot.GlobalLimit)
	}
	if snapshot.AvailabilityLimit != 0 {
		t.Fatalf("availability limit = %d, want 0", snapshot.AvailabilityLimit)
	}
	if snapshot.FinalLimitReason != "backend_unavailable" {
		t.Fatalf("final limit reason = %q, want backend_unavailable", snapshot.FinalLimitReason)
	}
	if snapshot.PressureLimit != 0 || snapshot.PressureReason != "backend_unavailable" || snapshot.PressureTargetReason != "backend_unavailable" {
		t.Fatalf("pressure limit/reason/target = %d/%s/%s, want 0/backend_unavailable/backend_unavailable", snapshot.PressureLimit, snapshot.PressureReason, snapshot.PressureTargetReason)
	}
	if snapshot.PrefillLimit != 0 || snapshot.PrefillReason != "backend_unavailable" || snapshot.PrefillTargetReason != "backend_unavailable" {
		t.Fatalf("prefill limit/reason/target = %d/%s/%s, want 0/backend_unavailable/backend_unavailable", snapshot.PrefillLimit, snapshot.PrefillReason, snapshot.PrefillTargetReason)
	}
	if !containsString(snapshot.YellowReasons, "backend_unavailable") {
		t.Fatalf("yellow reasons = %v, want backend_unavailable", snapshot.YellowReasons)
	}
}

func TestCleanPipelinePressureLimitIsFinalEnforcerComponent(t *testing.T) {
	now := time.Unix(400, 0)
	snapshot := Evaluate(cleanEvaluateConfig(), Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:     20,
			Preemptions: 1,
			Generation:  1500,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:      "metrics",
				Updated:     now.Add(-time.Second),
				Generation:  1000,
				Preemptions: 0,
				GlobalLimit: 100,
			},
		},
		GlobalLimit: 100,
	})

	if snapshot.PressureLimit >= snapshot.StateLimit {
		t.Fatalf("pressure limit = %d, state limit = %d, want pressure below state", snapshot.PressureLimit, snapshot.StateLimit)
	}
	if snapshot.GlobalLimit != snapshot.PressureLimit {
		t.Fatalf("global limit = %d, pressure limit = %d, want pressure to win", snapshot.GlobalLimit, snapshot.PressureLimit)
	}
	if snapshot.FinalLimitReason != "pressure" {
		t.Fatalf("final limit reason = %q, want pressure", snapshot.FinalLimitReason)
	}
	if snapshot.PressureReason != "severe_pressure" {
		t.Fatalf("pressure reason = %q, want severe_pressure", snapshot.PressureReason)
	}
	if snapshot.PressureTargetReason != "preemption" {
		t.Fatalf("pressure target reason = %q, want preemption", snapshot.PressureTargetReason)
	}
	if !containsString(snapshot.RedReasons, "preemptions") {
		t.Fatalf("red reasons = %v, want preemptions", snapshot.RedReasons)
	}
}

func TestCleanPipelinePressureCapHoldsDuringHealthyNoDemandWindow(t *testing.T) {
	now := time.Unix(410, 0)
	cfg := cleanEvaluateConfig()
	pressureCap := &capacity.PressureCap{}

	pressure := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             20,
			Preemptions:         1,
			GenerationTPS:       800,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:      "metrics",
				Updated:     now.Add(-time.Second),
				Preemptions: 0,
				GlobalLimit: 100,
			},
		},
		GlobalLimit: 100,
		PressureCap: pressureCap,
	})
	if got := int(pressureCap.Load()); got != 15 {
		t.Fatalf("learned pressure cap = %d, want 15 after preemption pressure", got)
	}
	if pressure.PressureLimit != 15 {
		t.Fatalf("pressure limit = %d, want 15", pressure.PressureLimit)
	}

	healthy := Evaluate(cfg, Input{
		Now: now.Add(time.Second),
		Samples: []telemetry.Sample{{
			Running:             15,
			Preemptions:         1,
			GenerationTPS:       600,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:               "metrics",
				Updated:              now,
				Preemptions:          1,
				CapacityTPS:          600,
				CapacityLearnedLimit: 50,
				CapacityTargetLimit:  50,
				CapacityLimit:        50,
				ThroughputLimit:      50,
				GlobalLimit:          15,
				AvailabilityLimit:    100,
			},
		},
		GlobalLimit: 100,
		PressureCap: pressureCap,
	})

	if got := int(pressureCap.Load()); got != 15 {
		t.Fatalf("learned pressure cap = %d, want unchanged 15 without demand pressure", got)
	}
	if healthy.Waiting != 0 || healthy.DynamicRejectedDelta != 0 {
		t.Fatalf("waiting/reject_delta = %d/%d, want 0/0", healthy.Waiting, healthy.DynamicRejectedDelta)
	}
	if healthy.PressureLimit != 15 || healthy.PressureReason != "learned_cap" {
		t.Fatalf("pressure limit/reason = %d/%s, want 15/learned_cap", healthy.PressureLimit, healthy.PressureReason)
	}
	if healthy.GlobalLimit != 15 {
		t.Fatalf("global limit = %d, want stable learned pressure cap 15", healthy.GlobalLimit)
	}
	if healthy.FinalLimitReason != "pressure" {
		t.Fatalf("final limit reason = %q, want pressure", healthy.FinalLimitReason)
	}
}

func TestCleanPipelineTTFTLimitExplainsLatencyDown(t *testing.T) {
	now := time.Unix(425, 0)
	cfg := cleanEvaluateConfig()
	cfg.TTFTEnabled = true
	previousTTFT := telemetry.HistogramSample{
		Count: 10,
		Sum:   5,
		Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 1, Count: 10},
			{UpperBound: 2, Count: 10},
		},
	}
	currentTTFT := telemetry.HistogramSample{
		Count: 12,
		Sum:   9,
		Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 1, Count: 10},
			{UpperBound: 2, Count: 12},
		},
	}
	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             20,
			GenerationTPS:       1000,
			GenerationTPSDirect: true,
			TTFT:                currentTTFT,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:            "metrics",
				Updated:           now.Add(-time.Second),
				TTFTCumulative:    previousTTFT,
				TTFTHighCount:     1,
				TTFTLearnedLimit:  20,
				TTFTTargetLimit:   20,
				GlobalLimit:       20,
				ThroughputLimit:   20,
				AvailabilityLimit: 20,
			},
		},
		GlobalLimit: 100,
	})

	if snapshot.GlobalLimit != 17 {
		t.Fatalf("global limit = %d, want 17", snapshot.GlobalLimit)
	}
	if snapshot.FinalLimitReason != "ttft" {
		t.Fatalf("final limit reason = %q, want ttft", snapshot.FinalLimitReason)
	}
	if snapshot.TTFTLearnState != "ttft_down" {
		t.Fatalf("ttft learn state = %q, want ttft_down", snapshot.TTFTLearnState)
	}
	if snapshot.TTFTLearnReason != "ttft_above_target" {
		t.Fatalf("ttft learn reason = %q, want ttft_above_target", snapshot.TTFTLearnReason)
	}
	if snapshot.TTFTTargetReason != "p95_latency" {
		t.Fatalf("ttft target reason = %q, want p95_latency", snapshot.TTFTTargetReason)
	}
	if snapshot.TTFTLimit != 17 || snapshot.TTFTLearnedLimit != 17 || snapshot.TTFTTargetLimit != 17 {
		t.Fatalf("ttft limits = limit %d learned %d target %d, want all 17", snapshot.TTFTLimit, snapshot.TTFTLearnedLimit, snapshot.TTFTTargetLimit)
	}
}

func TestCleanPipelineTTFTSparseHighLatencyDoesNotClampWithoutPressure(t *testing.T) {
	now := time.Unix(430, 0)
	cfg := cleanEvaluateConfig()
	cfg.TTFTEnabled = true
	cfg.GlobalGreen = 50
	cfg.GlobalYellow = 3
	cfg.GlobalRed = 1

	previousTTFT := telemetry.HistogramSample{
		Count: 10,
		Sum:   5,
		Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 1, Count: 10},
			{UpperBound: 4, Count: 10},
		},
	}
	currentTTFT := telemetry.HistogramSample{
		Count: 12,
		Sum:   13,
		Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 1, Count: 10},
			{UpperBound: 4, Count: 12},
		},
	}
	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             1,
			KVCacheUsage:        0.02,
			GenerationTPS:       51,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:                 "metrics",
				Updated:                now.Add(-time.Second),
				SemanticTTFTCumulative: previousTTFT,
				TTFTSource:             "semantic",
				TTFTHighCount:          1,
				TTFTLearnedLimit:       512,
				TTFTTargetLimit:        512,
				TTFTLimit:              512,
				CapacityTPS:            51,
				CapacityLearnedLimit:   50,
				CapacityTargetLimit:    50,
				CapacityLimit:          50,
				ThroughputLimit:        50,
				GlobalLimit:            50,
				AvailabilityLimit:      512,
			},
		},
		GlobalLimit:  512,
		SemanticTTFT: currentTTFT,
	})

	if snapshot.State != "green" {
		t.Fatalf("state = %q, want green without representative TTFT load or pressure", snapshot.State)
	}
	if containsString(snapshot.YellowReasons, "ttft_latency") || containsString(snapshot.RedReasons, "ttft_latency") {
		t.Fatalf("yellow/red reasons = %v/%v, want no TTFT pressure reason", snapshot.YellowReasons, snapshot.RedReasons)
	}
	if snapshot.TTFTHighCount != 0 {
		t.Fatalf("ttft high count = %d, want decayed 0", snapshot.TTFTHighCount)
	}
	if snapshot.TTFTLearnedLimit != 512 || snapshot.TTFTLimit != 512 {
		t.Fatalf("ttft learned/limit = %d/%d, want 512/512", snapshot.TTFTLearnedLimit, snapshot.TTFTLimit)
	}
	if snapshot.GlobalLimit != 50 {
		t.Fatalf("global limit = %d, want stable previous active limit 50", snapshot.GlobalLimit)
	}
}

func TestCleanPipelineTTFTPressureCanLearnDownWithoutClampingToStateLimit(t *testing.T) {
	now := time.Unix(435, 0)
	cfg := cleanEvaluateConfig()
	cfg.TTFTEnabled = true
	cfg.GlobalGreen = 50
	cfg.GlobalYellow = 3
	cfg.GlobalRed = 1

	previousTTFT := telemetry.HistogramSample{
		Count: 10,
		Sum:   5,
		Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 1, Count: 10},
			{UpperBound: 4, Count: 10},
		},
	}
	currentTTFT := telemetry.HistogramSample{
		Count: 12,
		Sum:   13,
		Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 1, Count: 10},
			{UpperBound: 4, Count: 12},
		},
	}
	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             1,
			KVCacheUsage:        0.02,
			GenerationTPS:       51,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:                 "metrics",
				Updated:                now.Add(-time.Second),
				SemanticTTFTCumulative: previousTTFT,
				TTFTSource:             "semantic",
				TTFTHighCount:          1,
				TTFTLearnedLimit:       512,
				TTFTTargetLimit:        512,
				TTFTLimit:              512,
				CapacityTPS:            51,
				CapacityLearnedLimit:   50,
				CapacityTargetLimit:    50,
				CapacityLimit:          50,
				ThroughputLimit:        50,
				GlobalLimit:            50,
				AvailabilityLimit:      512,
				DynamicRejected:        10,
			},
		},
		GlobalLimit:     512,
		SemanticTTFT:    currentTTFT,
		DynamicRejected: 11,
	})

	if snapshot.State != "red" {
		t.Fatalf("state = %q, want red under qualified TTFT pressure", snapshot.State)
	}
	if snapshot.StateLimit != 1 {
		t.Fatalf("state limit = %d, want configured red state limit 1", snapshot.StateLimit)
	}
	if snapshot.TTFTLearnState != "ttft_down" {
		t.Fatalf("ttft learn state = %q, want ttft_down", snapshot.TTFTLearnState)
	}
	if snapshot.TTFTLearnedLimit != 3 || snapshot.TTFTLimit != 3 {
		t.Fatalf("ttft learned/limit = %d/%d, want learned TTFT cap 3 independent of red state limit 1", snapshot.TTFTLearnedLimit, snapshot.TTFTLimit)
	}
	if snapshot.GlobalLimit != 1 || snapshot.FinalLimitReason != "state" {
		t.Fatalf("global/reason = %d/%s, want final state cap 1/state", snapshot.GlobalLimit, snapshot.FinalLimitReason)
	}
}

func TestCleanPipelineTTFTNoSignalDoesNotClampDemandProbe(t *testing.T) {
	now := time.Unix(440, 0)
	cfg := cleanEvaluateConfig()
	cfg.TTFTEnabled = true
	cfg.Capacity.CapacitySafetyRatio = 0.42

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             1,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       100,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:               "metrics",
				Updated:              now.Add(-time.Second),
				CapacityTPS:          100,
				CapacityLearnedLimit: 4,
				CapacityTargetLimit:  4,
				CapacityLimit:        4,
				ThroughputLimit:      4,
				GlobalLimit:          4,
				TTFTSource:           "semantic",
				TTFTLearnedLimit:     3,
				TTFTTargetLimit:      3,
				TTFTLimit:            3,
			},
		},
		GlobalLimit:     50,
		DynamicRejected: 32,
	})

	if snapshot.TTFTLearnState != "no_signal" || snapshot.TTFTLearnReason != "insufficient_latency_signal" {
		t.Fatalf("ttft learn = %s/%s, want no_signal/insufficient_latency_signal", snapshot.TTFTLearnState, snapshot.TTFTLearnReason)
	}
	if snapshot.TTFTLearnedLimit != 3 || snapshot.TTFTLimit != 50 {
		t.Fatalf("ttft learned/limit = %d/%d, want learned 3 but non-clamping limit 50", snapshot.TTFTLearnedLimit, snapshot.TTFTLimit)
	}
	if snapshot.CapacityLearnState != "demand_probe" {
		t.Fatalf("capacity learn state = %q, want demand_probe", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnReason != "healthy_demand_probe_floor" {
		t.Fatalf("capacity learn reason = %q, want healthy_demand_probe_floor", snapshot.CapacityLearnReason)
	}
	if snapshot.CapacityTargetReason != "demand_probe_floor" {
		t.Fatalf("capacity target reason = %q, want demand_probe_floor", snapshot.CapacityTargetReason)
	}
	if snapshot.CapacityLearnedLimit != 4 || snapshot.CapacityTargetLimit != 8 {
		t.Fatalf("capacity learned/target = %d/%d, want 4/8", snapshot.CapacityLearnedLimit, snapshot.CapacityTargetLimit)
	}
	if snapshot.ThroughputLimit != 8 || snapshot.GlobalLimit != 8 {
		t.Fatalf("throughput/global limit = %d/%d, want demand probe floor 8/8", snapshot.ThroughputLimit, snapshot.GlobalLimit)
	}
	if snapshot.FinalLimitReason != "throughput" {
		t.Fatalf("final limit reason = %q, want throughput", snapshot.FinalLimitReason)
	}
}

func TestCleanPipelineTTFTNoSignalHoldsLimitWithoutDemandPressure(t *testing.T) {
	now := time.Unix(441, 0)
	cfg := cleanEvaluateConfig()
	cfg.TTFTEnabled = true

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             1,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       1000,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:               "metrics",
				Updated:              now.Add(-time.Second),
				CapacityTPS:          1000,
				CapacityLearnedLimit: 50,
				CapacityTargetLimit:  50,
				CapacityLimit:        50,
				ThroughputLimit:      50,
				GlobalLimit:          3,
				TTFTSource:           "semantic",
				TTFTLearnedLimit:     3,
				TTFTTargetLimit:      3,
				TTFTLimit:            3,
				AvailabilityLimit:    100,
			},
		},
		GlobalLimit: 100,
	})

	if snapshot.DynamicRejectedDelta != 0 || snapshot.Waiting != 0 {
		t.Fatalf("reject_delta/waiting = %d/%d, want 0/0", snapshot.DynamicRejectedDelta, snapshot.Waiting)
	}
	if snapshot.TTFTLearnState != "no_signal" || snapshot.TTFTLearnReason != "insufficient_latency_signal" {
		t.Fatalf("ttft learn = %s/%s, want no_signal/insufficient_latency_signal", snapshot.TTFTLearnState, snapshot.TTFTLearnReason)
	}
	if snapshot.TTFTLearnedLimit != 3 || snapshot.TTFTLimit != 3 {
		t.Fatalf("ttft learned/limit = %d/%d, want 3/3 without demand pressure", snapshot.TTFTLearnedLimit, snapshot.TTFTLimit)
	}
	if snapshot.GlobalLimit != 3 || snapshot.FinalLimitReason != "ttft" {
		t.Fatalf("global/reason = %d/%s, want 3/ttft", snapshot.GlobalLimit, snapshot.FinalLimitReason)
	}
}

func TestCleanPipelineStaleRejectedTotalDoesNotTriggerDemandProbe(t *testing.T) {
	now := time.Unix(442, 0)
	cfg := cleanEvaluateConfig()
	cfg.Capacity.CapacitySafetyRatio = 0.42

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             1,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       100,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:               "metrics",
				Updated:              now.Add(-time.Second),
				CapacityTPS:          100,
				CapacityLearnedLimit: 4,
				CapacityTargetLimit:  4,
				CapacityLimit:        4,
				ThroughputLimit:      4,
				GlobalLimit:          4,
				DynamicRejected:      32,
			},
		},
		GlobalLimit:     50,
		DynamicRejected: 32,
	})

	if snapshot.DynamicRejectedDelta != 0 {
		t.Fatalf("dynamic rejected delta = %d, want 0 for unchanged cumulative counter", snapshot.DynamicRejectedDelta)
	}
	if snapshot.CapacityLearnState != "converged" {
		t.Fatalf("capacity learn state = %q, want converged", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnReason != "at_or_above_target" {
		t.Fatalf("capacity learn reason = %q, want at_or_above_target", snapshot.CapacityLearnReason)
	}
	if snapshot.CapacityLearnedLimit != 4 || snapshot.CapacityTargetLimit != 1 {
		t.Fatalf("capacity learned/target = %d/%d, want learned hold 4 and observed target 1", snapshot.CapacityLearnedLimit, snapshot.CapacityTargetLimit)
	}
	if snapshot.ThroughputLimit != 4 || snapshot.GlobalLimit != 4 {
		t.Fatalf("throughput/global limit = %d/%d, want unchanged active cap 4/4", snapshot.ThroughputLimit, snapshot.GlobalLimit)
	}
}

func TestCleanPipelineHealthyTTFTRecoversGraduallyFromLowLearnedLimit(t *testing.T) {
	now := time.Unix(445, 0)
	cfg := cleanEvaluateConfig()
	cfg.TTFTEnabled = true

	previousTTFT := telemetry.HistogramSample{
		Count: 10,
		Sum:   3,
		Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 1, Count: 10},
		},
	}
	currentTTFT := telemetry.HistogramSample{
		Count: 12,
		Sum:   4,
		Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 1, Count: 12},
		},
	}
	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             3,
			Waiting:             0,
			KVCacheUsage:        0.07,
			GenerationTPS:       260,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:                 "metrics",
				Updated:                now.Add(-time.Second),
				SemanticTTFTCumulative: previousTTFT,
				TTFTSource:             "semantic",
				TTFTSmoothedAvg:        0.33,
				TTFTSmoothedP95:        0.71,
				TTFTSmoothedP99:        0.71,
				TTFTHealthyCount:       1,
				TTFTLearnedLimit:       4,
				TTFTTargetLimit:        5,
				TTFTLimit:              4,
				CapacityLearnedLimit:   10,
				CapacityTargetLimit:    10,
				CapacityLimit:          10,
				ThroughputLimit:        10,
				GlobalLimit:            4,
				AvailabilityLimit:      100,
			},
		},
		GlobalLimit:  100,
		SemanticTTFT: currentTTFT,
	})

	if snapshot.TTFTLearnState != "ttft_healthy" {
		t.Fatalf("ttft learn state = %q, want ttft_healthy", snapshot.TTFTLearnState)
	}
	if snapshot.TTFTLimit != 4 {
		t.Fatalf("ttft limit = %d, want learned limit 4 while TTFT recovery accumulates", snapshot.TTFTLimit)
	}
	if snapshot.TTFTLearnedLimit != 4 || snapshot.TTFTTargetLimit != 5 {
		t.Fatalf("ttft learned/target = %d/%d, want preserved recovery state 4/5", snapshot.TTFTLearnedLimit, snapshot.TTFTTargetLimit)
	}
	if snapshot.GlobalLimit != 4 {
		t.Fatalf("global limit = %d, want TTFT learned limit 4 while recovery is pending", snapshot.GlobalLimit)
	}
	if snapshot.FinalLimitReason != "ttft" {
		t.Fatalf("final limit reason = %q, want TTFT to remain active during gradual recovery", snapshot.FinalLimitReason)
	}
}

func TestCleanPipelinePrefillLimitExplainsProtectedObservedCap(t *testing.T) {
	now := time.Unix(450, 0)
	snapshot := Evaluate(cleanEvaluateConfig(), Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running: 40,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:               "metrics",
				Updated:              now.Add(-time.Second),
				CapacityLearnedLimit: 30,
				CapacityTargetLimit:  100,
				GlobalLimit:          40,
				CapacityLimit:        100,
			},
		},
		PrefillProtected: 40,
		GlobalLimit:      100,
	})

	if snapshot.PrefillLimit != 40 {
		t.Fatalf("prefill limit = %d, want 40", snapshot.PrefillLimit)
	}
	if snapshot.PrefillReason != "running_at_observed_cap" {
		t.Fatalf("prefill reason = %q, want running_at_observed_cap", snapshot.PrefillReason)
	}
	if snapshot.PrefillTargetReason != "observed_cap" {
		t.Fatalf("prefill target reason = %q, want observed_cap", snapshot.PrefillTargetReason)
	}
}

func TestCleanPipelinePrefillSettlingDoesNotClampDecodeOnlyObservedCap(t *testing.T) {
	now := time.Unix(451, 0)
	cfg := cleanEvaluateConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.UserTPSYellowN = 2
	cfg.UserTPSRedN = 2
	cfg.GlobalGreen = 114
	cfg.GlobalYellow = 114
	cfg.GlobalRed = 114
	cfg.Capacity.UserTPSYellow = 55
	cfg.Capacity.UserTPSRed = 50
	cfg.Capacity.CapacityHealthyMul = 1.20

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             93,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       5000,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:               "metrics",
				Updated:              now.Add(-time.Second),
				PrefillTransition:    true,
				CapacityTPS:          5000,
				CapacityLearnedLimit: 55,
				CapacityTargetLimit:  114,
				CapacityLimit:        114,
				ThroughputLimit:      114,
				GlobalLimit:          114,
				AvailabilityLimit:    114,
				TTFTLearnedLimit:     114,
				TTFTTargetLimit:      114,
				TTFTLimit:            114,
			},
		},
		GlobalLimit: 114,
	})

	if !snapshot.PrefillTransition || !snapshot.PrefillSettling {
		t.Fatalf("prefill transition/settling = %t/%t, want true/true", snapshot.PrefillTransition, snapshot.PrefillSettling)
	}
	if snapshot.PrefillProtected != 0 || snapshot.DecodeRunning != 93 {
		t.Fatalf("prefill/decode running = %d/%d, want 0/93", snapshot.PrefillProtected, snapshot.DecodeRunning)
	}
	if snapshot.PrefillLimit != 114 || snapshot.PrefillReason == "running_at_observed_cap" {
		t.Fatalf("prefill limit/reason = %d/%s, want base limit and not running_at_observed_cap", snapshot.PrefillLimit, snapshot.PrefillReason)
	}
	if snapshot.FinalLimitReason == "prefill" {
		t.Fatalf("final limit reason = %q, want prefill not to win without prefill evidence", snapshot.FinalLimitReason)
	}
}

func TestCleanPipelineMinorPrefillProtectedDoesNotFreezeOrClamp(t *testing.T) {
	now := time.Unix(452, 0)
	cfg := cleanEvaluateConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.GlobalGreen = 112
	cfg.GlobalYellow = 112
	cfg.GlobalRed = 112
	cfg.Capacity.UserTPSYellow = 55
	cfg.Capacity.UserTPSRed = 50
	cfg.Capacity.CapacityHealthyMul = 1.20

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             75,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       5000,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:                    "metrics",
				Updated:                   now.Add(-time.Second),
				PrefillTransition:         true,
				CapacityTPS:               5000,
				CapacityLearnedLimit:      50,
				CapacityTargetLimit:       112,
				CapacityRatioHealthyCount: 1,
				CapacityLimit:             112,
				ThroughputLimit:           112,
				GlobalLimit:               112,
				AvailabilityLimit:         112,
				TTFTLearnedLimit:          112,
				TTFTTargetLimit:           112,
				TTFTLimit:                 112,
			},
		},
		PrefillProtected: 10,
		GlobalLimit:      112,
	})

	if snapshot.PrefillTransition || snapshot.PrefillSettling {
		t.Fatalf("prefill transition/settling = %t/%t, want both false for minor protected share", snapshot.PrefillTransition, snapshot.PrefillSettling)
	}
	if snapshot.PrefillProtected != 10 || snapshot.DecodeRunning != 65 {
		t.Fatalf("prefill/decode running = %d/%d, want 10/65", snapshot.PrefillProtected, snapshot.DecodeRunning)
	}
	if snapshot.CapacityLearnReason == "prefill_transition" || snapshot.CapacityLearnState == "prefill_freeze" {
		t.Fatalf("capacity learn = %s/%s, want no prefill freeze for minor protected share", snapshot.CapacityLearnState, snapshot.CapacityLearnReason)
	}
	if snapshot.PrefillReason == "running_at_observed_cap" || snapshot.FinalLimitReason == "prefill" {
		t.Fatalf("prefill reason/final = %s/%s, want no observed-cap prefill clamp", snapshot.PrefillReason, snapshot.FinalLimitReason)
	}
}

func TestCleanPipelineSparseProbeFloorPreventsLowTrafficStuckLimit(t *testing.T) {
	now := time.Unix(475, 0)
	snapshot := Evaluate(cleanEvaluateConfig(), Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:    1,
			Generation: 100,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:               "metrics",
				Updated:              now.Add(-time.Second),
				Generation:           0,
				CapacityLearnedLimit: 3,
				CapacityTargetLimit:  3,
				GlobalLimit:          3,
				CapacityLimit:        3,
			},
		},
		GlobalLimit: 100,
	})

	if snapshot.CapacityEstimateConfidence != "sparse" {
		t.Fatalf("capacity estimate confidence = %q, want sparse", snapshot.CapacityEstimateConfidence)
	}
	if snapshot.CapacityRepresentativeLoad || snapshot.RepresentativeUserTPSLoad {
		t.Fatalf("representative capacity/user load = %t/%t, want false/false", snapshot.CapacityRepresentativeLoad, snapshot.RepresentativeUserTPSLoad)
	}
	if snapshot.CapacityProjectedLimit != 3 {
		t.Fatalf("capacity projected limit = %d, want low-confidence projection 3", snapshot.CapacityProjectedLimit)
	}
	if snapshot.CapacityLearnedLimit != 3 {
		t.Fatalf("capacity learned limit = %d, want 3", snapshot.CapacityLearnedLimit)
	}
	if snapshot.CapacityTargetLimit != 4 {
		t.Fatalf("capacity target limit = %d, want sparse probe floor 4", snapshot.CapacityTargetLimit)
	}
	if snapshot.CapacityLearnState != "sparse_probe" {
		t.Fatalf("capacity learn state = %q, want sparse_probe", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnReason != "low_traffic_probe_floor" {
		t.Fatalf("capacity learn reason = %q, want low_traffic_probe_floor", snapshot.CapacityLearnReason)
	}
	if snapshot.CapacityTargetReason != "sparse_probe_floor" {
		t.Fatalf("capacity target reason = %q, want sparse_probe_floor", snapshot.CapacityTargetReason)
	}
	if snapshot.ThroughputLimit != 3 || snapshot.GlobalLimit != 3 {
		t.Fatalf("throughput/global limit = %d/%d, want previous active cap 3/3 without demand pressure", snapshot.ThroughputLimit, snapshot.GlobalLimit)
	}
	if snapshot.FinalLimitReason != "throughput" {
		t.Fatalf("final limit reason = %q, want throughput", snapshot.FinalLimitReason)
	}
}

func TestCleanPipelineRecoversSparseLearnedLimitUnderHealthyDemand(t *testing.T) {
	now := time.Unix(500, 0)
	cfg := cleanEvaluateConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.CapacityRatio = 0.42
	cfg.Capacity.UserTPSYellow = 55
	cfg.Capacity.UserTPSRed = 50
	cfg.Capacity.CapacitySafetyRatio = 0.42
	cfg.Capacity.CapacityHealthyMul = 1.20

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             10,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       1158,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:                    "metrics",
				Updated:                   now.Add(-time.Second),
				CapacityTPS:               1158,
				CapacityLearnedLimit:      3,
				CapacityTargetLimit:       16,
				CapacityRatioHealthyCount: 3,
				CapacityLearnState:        "sparse_probe",
				GlobalLimit:               11,
				CapacityLimit:             11,
				ThroughputLimit:           11,
				AvailabilityLimit:         33,
				TTFTLearnedLimit:          33,
				TTFTTargetLimit:           33,
				TTFTLimit:                 33,
			},
		},
		GlobalLimit:     33,
		DynamicRejected: 2053,
	})

	if snapshot.CapacityRawLimit != 21 || snapshot.CapacitySafeLimit != 8 {
		t.Fatalf("capacity estimate raw/safe = %d/%d, want 21/8", snapshot.CapacityRawLimit, snapshot.CapacitySafeLimit)
	}
	if snapshot.CapacityLearnState != "sparse_recovery" {
		t.Fatalf("capacity learn state = %q, want sparse_recovery", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnReason != "representative_sparse_recovery" {
		t.Fatalf("capacity learn reason = %q, want representative_sparse_recovery", snapshot.CapacityLearnReason)
	}
	if snapshot.CapacityTargetReason != "healthy_observed_load" {
		t.Fatalf("capacity target reason = %q, want healthy_observed_load", snapshot.CapacityTargetReason)
	}
	if snapshot.CapacityProjectedLimit != 18 {
		t.Fatalf("capacity projected limit = %d, want healthy observed projection 18", snapshot.CapacityProjectedLimit)
	}
	if snapshot.CapacityLearnedLimit != 18 || snapshot.CapacityTargetLimit != 18 {
		t.Fatalf("capacity learned/target = %d/%d, want 18/18", snapshot.CapacityLearnedLimit, snapshot.CapacityTargetLimit)
	}
	if snapshot.ThroughputLimit != 18 || snapshot.GlobalLimit != 18 {
		t.Fatalf("throughput/global limit = %d/%d, want 18/18", snapshot.ThroughputLimit, snapshot.GlobalLimit)
	}
	if snapshot.FinalLimitReason != "throughput" {
		t.Fatalf("final limit reason = %q, want throughput", snapshot.FinalLimitReason)
	}
}

func TestCleanPipelineHealthyObservedFloorDoesNotMoveActiveLimitWithoutPressure(t *testing.T) {
	now := time.Unix(525, 0)
	cfg := cleanEvaluateConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.CapacityRatio = 0.42
	cfg.Capacity.UserTPSYellow = 55
	cfg.Capacity.UserTPSRed = 50
	cfg.Capacity.CapacitySafetyRatio = 0.42
	cfg.Capacity.CapacityHealthyMul = 1.20
	cfg.Capacity.CapacityHealthyN = 3

	low := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             10,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       440,
			GenerationTPSDirect: true,
		}},
		GlobalLimit: 33,
	})
	if low.CapacityLearnedLimit != 3 || low.GlobalLimit != 3 {
		t.Fatalf("low learned/global = %d/%d, want 3/3", low.CapacityLearnedLimit, low.GlobalLimit)
	}

	previous := low
	for i := 1; i <= 5; i++ {
		previous = Evaluate(cfg, Input{
			Now: now.Add(time.Duration(i) * time.Second),
			Samples: []telemetry.Sample{{
				Running:             10,
				Waiting:             0,
				KVCacheUsage:        0.01,
				GenerationTPS:       1158,
				GenerationTPSDirect: true,
			}},
			Previous: PreviousMetrics{
				Snapshot: previous,
			},
			GlobalLimit: 33,
		})
	}

	if previous.CapacityProjectedLimit != 14 {
		t.Fatalf("capacity projected limit = %d, want healthy observed floor 14", previous.CapacityProjectedLimit)
	}
	if previous.CapacityLearnedLimit != 3 || previous.CapacityTargetLimit != 14 {
		t.Fatalf("capacity learned/target = %d/%d, want learned hold 3 and observed target 14", previous.CapacityLearnedLimit, previous.CapacityTargetLimit)
	}
	if previous.GlobalLimit != 3 {
		t.Fatalf("global limit = %d, want previous active cap 3 without demand pressure", previous.GlobalLimit)
	}
	if previous.CapacityLearnState != "green_hold" {
		t.Fatalf("capacity learn state = %q, want green_hold", previous.CapacityLearnState)
	}
	if previous.CapacityTargetReason != "healthy_observed_load" {
		t.Fatalf("capacity target reason = %q, want healthy_observed_load", previous.CapacityTargetReason)
	}
}

func TestCleanPipelineTierBasicSaturationCountsAsCapacityDemandPressure(t *testing.T) {
	now := time.Unix(535, 0)
	cfg := cleanEvaluateConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.CapacityRatio = 0.42
	cfg.Capacity.UserTPSYellow = 55
	cfg.Capacity.UserTPSRed = 50
	cfg.Capacity.CapacitySafetyRatio = 0.42
	cfg.Capacity.CapacityHealthyMul = 1.20
	cfg.Capacity.CapacityHealthyN = 2

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             49,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       4900,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:                    "metrics",
				Updated:                   now.Add(-time.Second),
				CapacityTPS:               4900,
				CapacityLearnedLimit:      50,
				CapacityTargetLimit:       50,
				CapacityRatioHealthyCount: 1,
				CapacityLimit:             50,
				ThroughputLimit:           50,
				GlobalLimit:               50,
				AvailabilityLimit:         512,
				TTFTLearnedLimit:          512,
				TTFTTargetLimit:           512,
				TTFTLimit:                 512,
			},
		},
		GlobalLimit: 512,
		Tier: tier.Snapshot{
			BasicInflight:   49,
			BasicLimit:      49,
			PremiumInflight: 0,
			PremiumReserved: 1,
		},
	})

	if snapshot.Waiting != 0 || snapshot.DynamicRejectedDelta != 0 {
		t.Fatalf("waiting/reject_delta = %d/%d, want 0/0", snapshot.Waiting, snapshot.DynamicRejectedDelta)
	}
	if !snapshot.TierDemandPressure || !snapshot.CapacityDemandPressure {
		t.Fatalf("tier/capacity demand pressure = %t/%t, want true/true", snapshot.TierDemandPressure, snapshot.CapacityDemandPressure)
	}
	if snapshot.CapacityLearnState != "probe_up" {
		t.Fatalf("capacity learn state = %q, want probe_up", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnReason != "healthy_window_satisfied" {
		t.Fatalf("capacity learn reason = %q, want healthy_window_satisfied", snapshot.CapacityLearnReason)
	}
	if snapshot.CapacityTargetReason != "healthy_observed_load" {
		t.Fatalf("capacity target reason = %q, want healthy_observed_load", snapshot.CapacityTargetReason)
	}
	if snapshot.CapacityLearnedLimit <= 50 {
		t.Fatalf("capacity learned limit = %d, want above 50", snapshot.CapacityLearnedLimit)
	}
	if snapshot.GlobalLimit <= 50 || snapshot.ThroughputLimit <= 50 {
		t.Fatalf("throughput/global limit = %d/%d, want both above 50", snapshot.ThroughputLimit, snapshot.GlobalLimit)
	}
	if snapshot.FinalLimitReason != "throughput" {
		t.Fatalf("final limit reason = %q, want throughput", snapshot.FinalLimitReason)
	}
}

func TestCleanPipelinePremiumReserveObservedFillCountsAsCapacityDemandPressure(t *testing.T) {
	now := time.Unix(536, 0)
	cfg := cleanEvaluateConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.CapacityRatio = 0.42
	cfg.Capacity.UserTPSYellow = 55
	cfg.Capacity.UserTPSRed = 50
	cfg.Capacity.CapacitySafetyRatio = 0.42
	cfg.Capacity.CapacityHealthyMul = 1.20
	cfg.Capacity.CapacityHealthyN = 2

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             49,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       4900,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:                    "metrics",
				Updated:                   now.Add(-time.Second),
				CapacityTPS:               4900,
				CapacityLearnedLimit:      50,
				CapacityTargetLimit:       50,
				CapacityRatioHealthyCount: 1,
				CapacityLimit:             50,
				ThroughputLimit:           50,
				GlobalLimit:               50,
				AvailabilityLimit:         512,
				TTFTLearnedLimit:          512,
				TTFTTargetLimit:           512,
				TTFTLimit:                 512,
			},
		},
		GlobalLimit: 512,
		Tier: tier.Snapshot{
			BasicLimit:      49,
			PremiumInflight: 0,
			PremiumReserved: 1,
		},
	})

	if snapshot.Waiting != 0 || snapshot.DynamicRejectedDelta != 0 {
		t.Fatalf("waiting/reject_delta = %d/%d, want 0/0", snapshot.Waiting, snapshot.DynamicRejectedDelta)
	}
	if !snapshot.TierDemandPressure || !snapshot.CapacityDemandPressure {
		t.Fatalf("tier/capacity demand pressure = %t/%t, want observed reserve fill to count as demand", snapshot.TierDemandPressure, snapshot.CapacityDemandPressure)
	}
	if snapshot.CapacityLearnState != "probe_up" {
		t.Fatalf("capacity learn state = %q, want probe_up", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnedLimit <= 50 || snapshot.GlobalLimit <= 50 {
		t.Fatalf("capacity learned/global = %d/%d, want both above 50", snapshot.CapacityLearnedLimit, snapshot.GlobalLimit)
	}
}

func TestCleanPipelineBasicRejectDeltaAtPremiumReserveCountsAsCapacityDemandPressure(t *testing.T) {
	now := time.Unix(537, 0)
	cfg := cleanEvaluateConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.CapacityRatio = 0.42
	cfg.Capacity.UserTPSYellow = 55
	cfg.Capacity.UserTPSRed = 50
	cfg.Capacity.CapacitySafetyRatio = 0.42
	cfg.Capacity.CapacityHealthyMul = 1.20
	cfg.Capacity.CapacityHealthyN = 2

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             49,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       4900,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:                    "metrics",
				Updated:                   now.Add(-time.Second),
				CapacityTPS:               4900,
				CapacityLearnedLimit:      50,
				CapacityTargetLimit:       50,
				CapacityRatioHealthyCount: 1,
				CapacityLimit:             50,
				ThroughputLimit:           50,
				GlobalLimit:               50,
				AvailabilityLimit:         512,
				TTFTLearnedLimit:          512,
				TTFTTargetLimit:           512,
				TTFTLimit:                 512,
				TierBasicRejected:         100,
				TierPremiumRejected:       7,
			},
		},
		GlobalLimit: 512,
		Tier: tier.Snapshot{
			BasicRejected:   125,
			PremiumRejected: 7,
			BasicLimit:      49,
			PremiumInflight: 0,
			PremiumReserved: 1,
		},
	})

	if snapshot.TierBasicRejectedDelta != 25 || snapshot.TierPremiumRejectedDelta != 0 {
		t.Fatalf("tier reject deltas = %d/%d, want 25/0", snapshot.TierBasicRejectedDelta, snapshot.TierPremiumRejectedDelta)
	}
	if !snapshot.TierDemandPressure || !snapshot.CapacityDemandPressure {
		t.Fatalf("tier/capacity demand pressure = %t/%t, want basic reject delta to count as demand", snapshot.TierDemandPressure, snapshot.CapacityDemandPressure)
	}
	if snapshot.CapacityLearnState != "probe_up" {
		t.Fatalf("capacity learn state = %q, want probe_up", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnedLimit <= 50 || snapshot.GlobalLimit <= 50 {
		t.Fatalf("capacity learned/global = %d/%d, want both above 50", snapshot.CapacityLearnedLimit, snapshot.GlobalLimit)
	}
}

func TestCleanPipelinePremiumWaitingSuppressesBasicReserveDemandProbe(t *testing.T) {
	now := time.Unix(538, 0)
	cfg := cleanEvaluateConfig()
	cfg.UserTPSYellow = 55
	cfg.UserTPSRed = 50
	cfg.CapacityRatio = 0.42
	cfg.Capacity.UserTPSYellow = 55
	cfg.Capacity.UserTPSRed = 50
	cfg.Capacity.CapacitySafetyRatio = 0.42
	cfg.Capacity.CapacityHealthyMul = 1.20
	cfg.Capacity.CapacityHealthyN = 2

	snapshot := Evaluate(cfg, Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:             49,
			Waiting:             0,
			KVCacheUsage:        0.01,
			GenerationTPS:       4900,
			GenerationTPSDirect: true,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:                    "metrics",
				Updated:                   now.Add(-time.Second),
				CapacityTPS:               4900,
				CapacityLearnedLimit:      50,
				CapacityTargetLimit:       50,
				CapacityRatioHealthyCount: 1,
				CapacityLimit:             50,
				ThroughputLimit:           50,
				GlobalLimit:               50,
				AvailabilityLimit:         512,
				TTFTLearnedLimit:          512,
				TTFTTargetLimit:           512,
				TTFTLimit:                 512,
				TierBasicRejected:         100,
			},
		},
		GlobalLimit: 512,
		Tier: tier.Snapshot{
			BasicRejected:   125,
			BasicLimit:      49,
			PremiumInflight: 0,
			PremiumWaiting:  1,
			PremiumReserved: 1,
		},
	})

	if snapshot.TierBasicRejectedDelta != 25 {
		t.Fatalf("tier basic reject delta = %d, want 25", snapshot.TierBasicRejectedDelta)
	}
	if snapshot.TierDemandPressure || snapshot.CapacityDemandPressure {
		t.Fatalf("tier/capacity demand pressure = %t/%t, want false while premium is waiting", snapshot.TierDemandPressure, snapshot.CapacityDemandPressure)
	}
	if snapshot.CapacityLearnState != "green_hold" {
		t.Fatalf("capacity learn state = %q, want green_hold without demand pressure", snapshot.CapacityLearnState)
	}
	if snapshot.GlobalLimit != 50 {
		t.Fatalf("global limit = %d, want active limit held at 50 without basic reserve probe", snapshot.GlobalLimit)
	}
}

func TestCleanIntakeGuardSeparatesWaitingFromAvailability(t *testing.T) {
	stage := evaluateCleanIntakeGuard(
		cleanSignals{
			Waiting:      1,
			BackendCount: 2,
		},
		"green",
		0,
		cleanPressureStage{Limit: 12, Reason: "inactive", TargetReason: "current_limit"},
		cleanPrefillStage{Limit: 12, Reason: "inactive", TargetReason: "current_limit"},
		50,
	)

	if stage.AvailabilityLimit != 50 {
		t.Fatalf("availability limit = %d, want unchanged 50", stage.AvailabilityLimit)
	}
	if stage.FinalLimitReasonOverride != "backend_waiting" {
		t.Fatalf("final override = %q, want backend_waiting", stage.FinalLimitReasonOverride)
	}
	if stage.Pressure.Reason != "backend_waiting" || stage.Prefill.Reason != "backend_waiting" {
		t.Fatalf("pressure/prefill reasons = %q/%q, want backend_waiting/backend_waiting", stage.Pressure.Reason, stage.Prefill.Reason)
	}
	if !containsString(stage.YellowReasons, "backend_waiting_queue") {
		t.Fatalf("yellow reasons = %v, want backend_waiting_queue", stage.YellowReasons)
	}
}

func TestCleanIntakeGuardUnavailableOverridesWaiting(t *testing.T) {
	stage := evaluateCleanIntakeGuard(
		cleanSignals{
			Waiting:      1,
			BackendCount: 2,
		},
		"green",
		2,
		cleanPressureStage{Limit: 12, Reason: "inactive", TargetReason: "current_limit"},
		cleanPrefillStage{Limit: 12, Reason: "inactive", TargetReason: "current_limit"},
		50,
	)

	if stage.AvailabilityLimit != 0 {
		t.Fatalf("availability limit = %d, want 0", stage.AvailabilityLimit)
	}
	if stage.FinalLimitReasonOverride != "backend_unavailable" {
		t.Fatalf("final override = %q, want backend_unavailable", stage.FinalLimitReasonOverride)
	}
	if stage.Pressure.Reason != "backend_unavailable" || stage.Prefill.Reason != "backend_unavailable" {
		t.Fatalf("pressure/prefill reasons = %q/%q, want backend_unavailable/backend_unavailable", stage.Pressure.Reason, stage.Prefill.Reason)
	}
	if !containsString(stage.YellowReasons, "backend_waiting_queue") || !containsString(stage.YellowReasons, "backend_unavailable") {
		t.Fatalf("yellow reasons = %v, want backend_waiting_queue and backend_unavailable", stage.YellowReasons)
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
