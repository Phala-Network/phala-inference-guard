package status

import (
	"strings"
	"testing"

	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
)

func TestFormatExposesCleanLearningReasons(t *testing.T) {
	got := Format(Input{
		Version: "test",
		Snapshot: runtimedynamic.Snapshot{
			State:                      "red",
			BackendCount:               2,
			BackendFailed:              0,
			Running:                    10,
			Waiting:                    1,
			GlobalLimit:                0,
			FinalLimitReason:           "backend_waiting",
			StateLimit:                 100,
			ThroughputLimit:            24,
			CapacityEstimateConfidence: "representative",
			CapacitySafeLimit:          24,
			CapacityProjectedLimit:     24,
			QOSLimit:                   0,
			CapacityLimit:              24,
			CapacityLearnedLimit:       24,
			CapacityTargetLimit:        24,
			CapacityLearnState:         "pig_down",
			CapacityLearnReason:        "pig_below_target",
			CapacityTargetReason:       "estimate_safe",
			TTFTLimit:                  17,
			TTFTLearnedLimit:           17,
			TTFTTargetLimit:            17,
			TTFTLearnState:             "ttft_down",
			TTFTLearnReason:            "ttft_above_target",
			TTFTTargetReason:           "p95_latency",
			PressureLimit:              0,
			PressureReason:             "backend_waiting",
			PressureTargetReason:       "backend_waiting",
			PrefillLimit:               0,
			PrefillReason:              "backend_waiting",
			PrefillTargetReason:        "backend_waiting",
			AvailabilityLimit:          100,
			DynamicRejectedDelta:       1,
			TierBasicRejectedDelta:     9,
			TierPremiumRejectedDelta:   0,
			TierDemandPressure:         true,
			CapacityDemandPressure:     true,
		},
		QueueCurrent:       1,
		DynamicRejected:    2,
		BackendUnavailable: 3,
		Tier: TierSnapshot{
			BasicInflight:   4,
			BasicWaiting:    2,
			BasicLimit:      8,
			PremiumInflight: 1,
			PremiumWaiting:  0,
			PremiumReserved: 2,
		},
	})

	assertStatusContains(t, got, `winner=backend_waiting`)
	assertStatusContains(t, got, `estimate=representative/24`)
	assertStatusContains(t, got, `projected=24`)
	assertStatusContains(t, got, `target_reason=estimate_safe`)
	assertStatusContains(t, got, `pressure=0/backend_waiting/backend_waiting`)
	assertStatusContains(t, got, `prefill_limit=0/backend_waiting/backend_waiting`)
	assertStatusContains(t, got, `reject_delta=1`)
	assertStatusContains(t, got, `demand=1 tier_demand=1`)
	assertStatusContains(t, got, `tier_reject_delta=9/0`)
	assertStatusContains(t, got, `tier_waiting=2/0`)
	assertStatusContains(t, got, `learn=pig_down/pig_below_target`)
	assertStatusContains(t, got, `ttft_learn=ttft_down/ttft_above_target/p95_latency`)
}

func TestFormatUsesUnknownForMissingCleanLearningReasons(t *testing.T) {
	got := Format(Input{
		Version: "test",
		Snapshot: runtimedynamic.Snapshot{
			State: "green",
		},
	})

	assertStatusContains(t, got, `winner=unknown`)
	assertStatusContains(t, got, `target_reason=unknown`)
	assertStatusContains(t, got, `pressure=0/unknown/unknown`)
	assertStatusContains(t, got, `prefill_limit=0/unknown/unknown`)
	assertStatusContains(t, got, `learn=/unknown`)
	assertStatusContains(t, got, `ttft_learn=/unknown/unknown`)
}

func assertStatusContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("status line missing %q\nstatus line:\n%s", want, got)
	}
}
