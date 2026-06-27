package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
)

func TestWriteDynamicExposesCleanLearningReasons(t *testing.T) {
	var out bytes.Buffer
	WriteDynamic(&out, runtimedynamic.Snapshot{
		State:                      "red",
		Source:                     "metrics",
		Updated:                    time.Unix(100, 0),
		FinalLimitReason:           "backend_waiting",
		CapacityRepresentativeLoad: true,
		CapacityLearnState:         "pig_down",
		CapacityLearnReason:        "pig_below_target",
		CapacityTargetReason:       "estimate_safe",
		CapacityProjectedLimit:     12,
		TTFTLearnState:             "ttft_down",
		TTFTLearnReason:            "ttft_above_target",
		TTFTTargetReason:           "p95_latency",
		PressureReason:             "backend_waiting",
		PressureTargetReason:       "backend_waiting",
		PrefillReason:              "backend_waiting",
		PrefillTargetReason:        "backend_waiting",
		RepresentativeUserTPSLoad:  true,
		DynamicRejectedDelta:       2,
		TierBasicRejectedDelta:     11,
		TierPremiumRejectedDelta:   1,
		TierBasicWaiting:           3,
		TierPremiumWaiting:         0,
		TierDemandPressure:         true,
		CapacityDemandPressure:     true,
	}, DynamicConfig{
		UserTPSCapacityRatio:    0.42,
		UserTPSCapacityRatioMax: 0.85,
	}, nil)

	got := out.String()
	assertContains(t, got, `pig_dynamic_final_limit_info{reason="backend_waiting"} 1`)
	assertContains(t, got, `pig_dynamic_capacity_learning_reason_info{state="pig_down",reason="pig_below_target",target_reason="estimate_safe"} 1`)
	assertContains(t, got, `pig_dynamic_capacity_representative_load 1`)
	assertContains(t, got, `pig_dynamic_capacity_demand_pressure 1`)
	assertContains(t, got, `pig_dynamic_tier_demand_pressure 1`)
	assertContains(t, got, `pig_dynamic_tier_basic_rejected_delta 11`)
	assertContains(t, got, `pig_dynamic_tier_premium_rejected_delta 1`)
	assertContains(t, got, `pig_dynamic_tier_basic_waiting 3`)
	assertContains(t, got, `pig_dynamic_tier_premium_waiting 0`)
	assertContains(t, got, `pig_dynamic_capacity_projected_limit 12`)
	assertContains(t, got, `pig_dynamic_representative_user_tps_load 1`)
	assertContains(t, got, `pig_dynamic_rejected_delta 2`)
	assertContains(t, got, `pig_dynamic_ttft_learning_reason_info{state="ttft_down",reason="ttft_above_target",target_reason="p95_latency"} 1`)
	assertContains(t, got, `pig_dynamic_pressure_limit_info{reason="backend_waiting",target_reason="backend_waiting"} 1`)
	assertContains(t, got, `pig_dynamic_prefill_limit_info{reason="backend_waiting",target_reason="backend_waiting"} 1`)
	assertContains(t, got, `pig_dynamic_single_user_tps_capacity_ratio 0.420000`)
	assertContains(t, got, `pig_dynamic_single_user_tps_capacity_ratio_max 0.850000`)
}

func TestWriteDynamicUsesUnknownForMissingCleanLearningReasons(t *testing.T) {
	var out bytes.Buffer
	WriteDynamic(&out, runtimedynamic.Snapshot{
		State:   "green",
		Source:  "metrics",
		Updated: time.Unix(200, 0),
	}, DynamicConfig{}, nil)

	got := out.String()
	assertContains(t, got, `pig_dynamic_final_limit_info{reason="unknown"} 1`)
	assertContains(t, got, `pig_dynamic_capacity_learning_reason_info{state="",reason="unknown",target_reason="unknown"} 1`)
	assertContains(t, got, `pig_dynamic_ttft_learning_reason_info{state="",reason="unknown",target_reason="unknown"} 1`)
	assertContains(t, got, `pig_dynamic_pressure_limit_info{reason="unknown",target_reason="unknown"} 1`)
	assertContains(t, got, `pig_dynamic_prefill_limit_info{reason="unknown",target_reason="unknown"} 1`)
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("output missing %q\noutput:\n%s", want, got)
	}
}
