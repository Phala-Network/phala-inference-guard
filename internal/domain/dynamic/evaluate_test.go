package dynamic

import (
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func cleanEvaluateConfig() Config {
	cfg := Config{
		Enabled:        true,
		Enforce:        true,
		KVYellow:       0.70,
		KVRed:          0.80,
		WaitingYellow:  1,
		WaitingRed:     2,
		PreemptRed:     1,
		UserTPSEnabled: true,
		UserTPSYellow:  25,
		UserTPSRed:     20,
		UserTPSMinRun:  1,
		UserTPSYellowN: 1,
		UserTPSRedN:    1,
		CapacityRatio:  1,
		CapacityStepUp: 0.10,
		GlobalGreen:    100,
		GlobalYellow:   100,
		GlobalRed:      100,
	}
	cfg.Capacity = cfgCapacity()
	return cfg
}

func cfgCapacity() capacity.Config {
	return capacity.Config{
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

func TestEvaluateWaitingClosesIntakeButPreservesLearnedCapacity(t *testing.T) {
	now := time.Unix(100, 0)
	snapshot := Evaluate(cleanEvaluateConfig(), Input{
		Now: now,
		Samples: []telemetry.Sample{{
			Running:    20,
			Waiting:    1,
			Generation: 1000,
		}},
		Previous: PreviousMetrics{
			Snapshot: runtimedynamic.Snapshot{
				Source:               "metrics",
				Updated:              now.Add(-time.Second),
				Generation:           500,
				CapacityTPS:          500,
				CapacityLearnedLimit: 20,
				CapacityTargetLimit:  30,
				GlobalLimit:          20,
				CapacityLimit:        20,
			},
		},
		GlobalLimit: 100,
	})

	if snapshot.GlobalLimit != 0 {
		t.Fatalf("global limit = %d, want 0", snapshot.GlobalLimit)
	}
	if snapshot.FinalLimitReason != "backend_waiting" {
		t.Fatalf("final limit reason = %q, want backend_waiting", snapshot.FinalLimitReason)
	}
	if snapshot.PressureLimit != 0 || snapshot.PressureReason != "backend_waiting" || snapshot.PressureTargetReason != "backend_waiting" {
		t.Fatalf("pressure limit/reason/target = %d/%s/%s, want 0/backend_waiting/backend_waiting", snapshot.PressureLimit, snapshot.PressureReason, snapshot.PressureTargetReason)
	}
	if snapshot.PrefillLimit != 0 || snapshot.PrefillReason != "backend_waiting" || snapshot.PrefillTargetReason != "backend_waiting" {
		t.Fatalf("prefill limit/reason/target = %d/%s/%s, want 0/backend_waiting/backend_waiting", snapshot.PrefillLimit, snapshot.PrefillReason, snapshot.PrefillTargetReason)
	}
	if snapshot.CapacityLearnedLimit == 0 {
		t.Fatalf("capacity learned limit should not be zeroed by backend waiting")
	}
	if snapshot.CapacityLearnState != "pressure_hold" {
		t.Fatalf("capacity learn state = %q, want pressure_hold", snapshot.CapacityLearnState)
	}
	if snapshot.CapacityLearnReason != "pressure_not_representative" {
		t.Fatalf("capacity learn reason = %q, want pressure_not_representative", snapshot.CapacityLearnReason)
	}
	if snapshot.AvailabilityLimit != 100 {
		t.Fatalf("availability limit = %d, want 100", snapshot.AvailabilityLimit)
	}
	if snapshot.Decision.Limits.Throughput != snapshot.ThroughputLimit {
		t.Fatalf("decision throughput limit = %d, snapshot throughput limit = %d", snapshot.Decision.Limits.Throughput, snapshot.ThroughputLimit)
	}
}
