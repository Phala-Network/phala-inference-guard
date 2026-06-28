package metrics

import (
	"fmt"
	"io"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/latency"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type Int64Loader interface {
	Load() int64
}

type DynamicConfig struct {
	TTFTEnabled               bool
	TTFTPolicy                latency.Policy
	PressureEnabled           bool
	PressureHeadroom          int
	PressureMinLimit          int
	PressureLearnRatio        float64
	PressureLearnMinRunning   int
	UserTPSEnabled            bool
	UserTPSYellow             float64
	UserTPSRed                float64
	UserTPSYellowN            int
	UserTPSRedN               int
	UserTPSGraceMin           time.Duration
	UserTPSGraceMax           time.Duration
	UserTPSGraceBps           float64
	UserTPSGraceMul           float64
	UserTPSCapacityLearn      bool
	UserTPSCapacityRatio      float64
	UserTPSCapacityRatioMax   float64
	UserTPSCapacityStepUp     float64
	UserTPSCapacityHealthyN   int
	UserTPSCapacityHealthyMul float64
	UserTPSCapacitySmoothing  float64
}

func WriteDynamic(w io.Writer, snapshot dynamic.Snapshot, cfg DynamicConfig, pressureCap Int64Loader) {
	ttftPolicy := cfg.TTFTPolicy.Normalize()
	fmt.Fprintf(w, "pig_dynamic_state_info{state=%q,source=%q} 1\n", snapshot.DecisionState(), snapshot.Source)
	fmt.Fprintf(w, "pig_dynamic_last_update_seconds %d\n", snapshot.Updated.Unix())
	if snapshot.Error != "" {
		fmt.Fprintf(w, "pig_dynamic_error_info{message=%q} 1\n", snapshot.Error)
	}
	fmt.Fprintf(w, "pig_dynamic_observed_running %d\n", snapshot.Running)
	fmt.Fprintf(w, "pig_dynamic_observed_waiting %d\n", snapshot.Waiting)
	fmt.Fprintf(w, "pig_dynamic_observed_kv_cache_usage %.6f\n", snapshot.KVCacheUsage)
	fmt.Fprintf(w, "pig_dynamic_observed_preemptions %d\n", snapshot.Preemptions)
	fmt.Fprintf(w, "pig_dynamic_observed_generation_tokens_total %d\n", snapshot.Generation)
	fmt.Fprintf(w, "pig_dynamic_observed_generation_tokens_per_second %.6f\n", snapshot.GenerationTPS)
	fmt.Fprintf(w, "pig_dynamic_observed_generation_tokens_per_second_valid %d\n", num.BoolAsInt(snapshot.GenerationTPSValid))
	fmt.Fprintf(w, "pig_dynamic_observed_ttft_count %d\n", snapshot.TTFTWindowCount)
	fmt.Fprintf(w, "pig_dynamic_observed_ttft_avg_seconds %.6f\n", snapshot.TTFTWindowAvg)
	fmt.Fprintf(w, "pig_dynamic_observed_ttft_p95_seconds %.6f\n", snapshot.TTFTWindowP95)
	fmt.Fprintf(w, "pig_dynamic_observed_ttft_p99_seconds %.6f\n", snapshot.TTFTWindowP99)
	fmt.Fprintf(w, "pig_dynamic_observed_ttft_valid %d\n", num.BoolAsInt(snapshot.TTFTWindowValid))
	if snapshot.TTFTSource != "" {
		fmt.Fprintf(w, "pig_dynamic_observed_ttft_source_info{source=%q} 1\n", snapshot.TTFTSource)
	}
	fmt.Fprintf(w, "pig_dynamic_observed_ttft_smoothed_avg_seconds %.6f\n", snapshot.TTFTSmoothedAvg)
	fmt.Fprintf(w, "pig_dynamic_observed_ttft_smoothed_p95_seconds %.6f\n", snapshot.TTFTSmoothedP95)
	fmt.Fprintf(w, "pig_dynamic_observed_ttft_smoothed_p99_seconds %.6f\n", snapshot.TTFTSmoothedP99)
	fmt.Fprintf(w, "pig_dynamic_ttft_target_seconds %.6f\n", ttftPolicy.TargetSeconds)
	fmt.Fprintf(w, "pig_dynamic_ttft_red_seconds %.6f\n", ttftPolicy.RedSeconds)
	fmt.Fprintf(w, "pig_dynamic_ttft_p99_target_seconds %.6f\n", ttftPolicy.P99TargetSeconds)
	fmt.Fprintf(w, "pig_dynamic_ttft_p99_red_seconds %.6f\n", ttftPolicy.P99RedSeconds)
	fmt.Fprintf(w, "pig_dynamic_ttft_enabled %d\n", num.BoolAsInt(cfg.TTFTEnabled))
	fmt.Fprintf(w, "pig_dynamic_ttft_learned_limit %d\n", snapshot.TTFTLearnedLimit)
	fmt.Fprintf(w, "pig_dynamic_ttft_target_limit %d\n", snapshot.TTFTTargetLimit)
	fmt.Fprintf(w, "pig_dynamic_ttft_limit %d\n", snapshot.TTFTLimit)
	fmt.Fprintf(w, "pig_dynamic_ttft_high_count %d\n", snapshot.TTFTHighCount)
	fmt.Fprintf(w, "pig_dynamic_ttft_p99_high_count %d\n", snapshot.TTFTP99HighCount)
	fmt.Fprintf(w, "pig_dynamic_ttft_healthy_count %d\n", snapshot.TTFTHealthyCount)
	if snapshot.TTFTLearnState != "" {
		fmt.Fprintf(w, "pig_dynamic_ttft_learning_info{state=%q} 1\n", snapshot.TTFTLearnState)
	}
	ttftLearnReason := snapshot.TTFTLearnReason
	if ttftLearnReason == "" {
		ttftLearnReason = "unknown"
	}
	ttftTargetReason := snapshot.TTFTTargetReason
	if ttftTargetReason == "" {
		ttftTargetReason = "unknown"
	}
	fmt.Fprintf(w, "pig_dynamic_ttft_learning_reason_info{state=%q,reason=%q,target_reason=%q} 1\n", snapshot.TTFTLearnState, ttftLearnReason, ttftTargetReason)
	fmt.Fprintf(w, "pig_dynamic_observed_capacity_tokens_per_second %.6f\n", snapshot.CapacityTPS)
	if snapshot.CapacityEstimateConfidence != "" {
		fmt.Fprintf(w, "pig_dynamic_capacity_estimate_info{confidence=%q} 1\n", snapshot.CapacityEstimateConfidence)
	}
	fmt.Fprintf(w, "pig_dynamic_capacity_representative_load %d\n", num.BoolAsInt(snapshot.CapacityRepresentativeLoad))
	fmt.Fprintf(w, "pig_dynamic_capacity_demand_pressure %d\n", num.BoolAsInt(snapshot.CapacityDemandPressure))
	fmt.Fprintf(w, "pig_dynamic_tier_demand_pressure %d\n", num.BoolAsInt(snapshot.TierDemandPressure))
	fmt.Fprintf(w, "pig_dynamic_tier_basic_rejected_delta %d\n", snapshot.TierBasicRejectedDelta)
	fmt.Fprintf(w, "pig_dynamic_tier_premium_rejected_delta %d\n", snapshot.TierPremiumRejectedDelta)
	fmt.Fprintf(w, "pig_dynamic_tier_basic_waiting %d\n", snapshot.TierBasicWaiting)
	fmt.Fprintf(w, "pig_dynamic_tier_premium_waiting %d\n", snapshot.TierPremiumWaiting)
	fmt.Fprintf(w, "pig_dynamic_capacity_raw_limit %d\n", snapshot.CapacityRawLimit)
	fmt.Fprintf(w, "pig_dynamic_capacity_safe_limit %d\n", snapshot.CapacitySafeLimit)
	fmt.Fprintf(w, "pig_dynamic_capacity_low_confidence_limit %d\n", snapshot.CapacityLowConfidenceLimit)
	fmt.Fprintf(w, "pig_dynamic_capacity_learning_info{mode=%q,state=%q} 1\n", snapshot.CapacityLearnMode, snapshot.CapacityLearnState)
	capacityLearnReason := snapshot.CapacityLearnReason
	if capacityLearnReason == "" {
		capacityLearnReason = "unknown"
	}
	capacityTargetReason := snapshot.CapacityTargetReason
	if capacityTargetReason == "" {
		capacityTargetReason = "unknown"
	}
	fmt.Fprintf(w, "pig_dynamic_capacity_learning_reason_info{state=%q,reason=%q,target_reason=%q} 1\n", snapshot.CapacityLearnState, capacityLearnReason, capacityTargetReason)
	fmt.Fprintf(w, "pig_dynamic_capacity_projected_limit %d\n", snapshot.CapacityProjectedLimit)
	fmt.Fprintf(w, "pig_dynamic_capacity_learned_limit %d\n", snapshot.CapacityLearnedLimit)
	fmt.Fprintf(w, "pig_dynamic_capacity_target_limit %d\n", snapshot.CapacityTargetLimit)
	fmt.Fprintf(w, "pig_dynamic_capacity_healthy_count %d\n", snapshot.CapacityRatioHealthyCount)
	fmt.Fprintf(w, "pig_dynamic_observed_single_user_tokens_per_second %.6f\n", snapshot.UserTPS)
	fmt.Fprintf(w, "pig_dynamic_representative_user_tps_load %d\n", num.BoolAsInt(snapshot.RepresentativeUserTPSLoad))
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_yellow_count %d\n", snapshot.UserTPSYellowCount)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_red_count %d\n", snapshot.UserTPSRedCount)
	fmt.Fprintf(w, "pig_dynamic_prefill_protected_running %d\n", snapshot.PrefillProtected)
	if snapshot.PrefillTransition {
		fmt.Fprintf(w, "pig_dynamic_prefill_transition_active 1\n")
	} else {
		fmt.Fprintf(w, "pig_dynamic_prefill_transition_active 0\n")
	}
	fmt.Fprintf(w, "pig_dynamic_prefill_settling_active %d\n", num.BoolAsInt(snapshot.PrefillSettling))
	fmt.Fprintf(w, "pig_dynamic_decode_running %d\n", snapshot.DecodeRunning)
	fmt.Fprintf(w, "pig_dynamic_backend_count %d\n", snapshot.BackendCount)
	fmt.Fprintf(w, "pig_dynamic_backend_failed %d\n", snapshot.BackendFailed)
	fmt.Fprintf(w, "pig_dynamic_rejected_delta %d\n", snapshot.DynamicRejectedDelta)
	fmt.Fprintf(w, "pig_dynamic_hard_global_limit %d\n", snapshot.HardGlobalLimit)
	fmt.Fprintf(w, "pig_dynamic_state_limit %d\n", snapshot.StateLimit)
	fmt.Fprintf(w, "pig_dynamic_throughput_limit %d\n", snapshot.ThroughputLimit)
	fmt.Fprintf(w, "pig_dynamic_availability_limit %d\n", snapshot.AvailabilityLimit)
	finalLimitReason := snapshot.FinalLimitReason
	if finalLimitReason == "" {
		finalLimitReason = "unknown"
	}
	fmt.Fprintf(w, "pig_dynamic_final_limit_info{reason=%q} 1\n", finalLimitReason)
	fmt.Fprintf(w, "pig_dynamic_global_limit %d\n", snapshot.GlobalLimit)
	fmt.Fprintf(w, "pig_dynamic_admission_limit %d\n", snapshot.QOSLimit)
	fmt.Fprintf(w, "pig_dynamic_capacity_limit %d\n", snapshot.CapacityLimit)
	fmt.Fprintf(w, "pig_dynamic_pressure_limit %d\n", snapshot.PressureLimit)
	pressureReason := snapshot.PressureReason
	if pressureReason == "" {
		pressureReason = "unknown"
	}
	pressureTargetReason := snapshot.PressureTargetReason
	if pressureTargetReason == "" {
		pressureTargetReason = "unknown"
	}
	fmt.Fprintf(w, "pig_dynamic_pressure_limit_info{reason=%q,target_reason=%q} 1\n", pressureReason, pressureTargetReason)
	fmt.Fprintf(w, "pig_dynamic_prefill_limit %d\n", snapshot.PrefillLimit)
	prefillReason := snapshot.PrefillReason
	if prefillReason == "" {
		prefillReason = "unknown"
	}
	prefillTargetReason := snapshot.PrefillTargetReason
	if prefillTargetReason == "" {
		prefillTargetReason = "unknown"
	}
	fmt.Fprintf(w, "pig_dynamic_prefill_limit_info{reason=%q,target_reason=%q} 1\n", prefillReason, prefillTargetReason)
	fmt.Fprintf(w, "pig_dynamic_pressure_limit_enabled %d\n", num.BoolAsInt(cfg.PressureEnabled))
	fmt.Fprintf(w, "pig_dynamic_pressure_headroom %d\n", cfg.PressureHeadroom)
	fmt.Fprintf(w, "pig_dynamic_pressure_min_limit %d\n", cfg.PressureMinLimit)
	if pressureCap != nil {
		fmt.Fprintf(w, "pig_dynamic_pressure_learned_cap %d\n", pressureCap.Load())
	} else {
		fmt.Fprintf(w, "pig_dynamic_pressure_learned_cap 0\n")
	}
	fmt.Fprintf(w, "pig_dynamic_pressure_learn_ratio %.6f\n", cfg.PressureLearnRatio)
	fmt.Fprintf(w, "pig_dynamic_pressure_learn_min_running %d\n", cfg.PressureLearnMinRunning)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_enabled %d\n", num.BoolAsInt(cfg.UserTPSEnabled))
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_yellow %.6f\n", cfg.UserTPSYellow)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_red %.6f\n", cfg.UserTPSRed)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_yellow_consecutive %d\n", cfg.UserTPSYellowN)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_red_consecutive %d\n", cfg.UserTPSRedN)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_prefill_grace_min_seconds %.6f\n", cfg.UserTPSGraceMin.Seconds())
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_prefill_grace_max_seconds %.6f\n", cfg.UserTPSGraceMax.Seconds())
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_prefill_grace_body_bytes_per_second %.6f\n", cfg.UserTPSGraceBps)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_prefill_grace_multiplier %.6f\n", cfg.UserTPSGraceMul)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_capacity_learn_enabled %d\n", num.BoolAsInt(cfg.UserTPSCapacityLearn))
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_capacity_ratio %.6f\n", cfg.UserTPSCapacityRatio)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_capacity_ratio_max %.6f\n", cfg.UserTPSCapacityRatioMax)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_capacity_probe_step_ratio %.6f\n", cfg.UserTPSCapacityStepUp)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_capacity_healthy_consecutive %d\n", cfg.UserTPSCapacityHealthyN)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_capacity_healthy_multiplier %.6f\n", cfg.UserTPSCapacityHealthyMul)
	fmt.Fprintf(w, "pig_dynamic_single_user_tps_capacity_smoothing %.6f\n", cfg.UserTPSCapacitySmoothing)
	for _, reason := range snapshot.DecisionYellowReasons() {
		fmt.Fprintf(w, "pig_dynamic_reason_info{state=%q,reason=%q} 1\n", "yellow", reason)
	}
	for _, reason := range snapshot.DecisionRedReasons() {
		fmt.Fprintf(w, "pig_dynamic_reason_info{state=%q,reason=%q} 1\n", "red", reason)
	}
}
