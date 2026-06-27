package capacity

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type LearnResult struct {
	LearnedLimit   int
	TargetLimit    int
	State          string
	HealthyCount   int
	Reason         string
	TargetReason   string
	ProjectedLimit int
}

type CleanLearnInput struct {
	Config             Config
	Previous           Previous
	BaseLimit          int
	Estimate           EstimateResult
	GenerationTPSValid bool
	CapacityTPS        float64
	Running            int
	DecodeRunning      int
	Waiting            int
	KVCacheUsage       float64
	PreemptionDelta    uint64
	UserTPS            float64
	QOSYellowReady     bool
	QOSRedReady        bool
	TTFTHealthy        bool
	Freeze             bool
	QueueCurrent       int64
	DynamicRejected    uint64
	DemandPressure     bool
}

func CleanLearnCap(input CleanLearnInput) LearnResult {
	cfg := input.Config
	baseLimit := input.BaseLimit
	if !cfg.CapacityLearn || baseLimit <= 0 {
		return cleanLearnResult(baseLimit, baseLimit, "disabled", "disabled", "base_limit", 0, 0)
	}

	learned := input.Previous.CapacityLearnedLimit
	if input.Previous.Source != "metrics" {
		learned = InitialLimit(baseLimit)
	} else if learned <= 0 {
		learned = baseLimit
	}
	learned = num.ClampInt(learned, 1, baseLimit)

	healthyCount := input.Previous.CapacityRatioHealthyCount
	target := input.Previous.CapacityTargetLimit
	targetReason := "previous_target"
	if target <= 0 {
		target = learned
		targetReason = "learned_limit"
	}
	target = num.ClampInt(target, 1, baseLimit)

	projected := input.Estimate.SafeLimit
	projectedReason := "estimate_safe"
	if projected <= 0 {
		projected = cleanProjectedCapacity(cfg, input.CapacityTPS, baseLimit)
		projectedReason = "smoothed_tps"
	}
	if cleanLowConfidenceEstimate(input.Estimate) && input.Estimate.LowConfidenceLimit > 0 && projected > input.Estimate.LowConfidenceLimit {
		projected = input.Estimate.LowConfidenceLimit
		projectedReason = "low_confidence_bound"
	}
	if projected > target {
		target = projected
		targetReason = projectedReason
	}

	result := func(learnedLimit, targetLimit int, state, reason, reasonForTarget string, count int) LearnResult {
		if reasonForTarget == "" {
			reasonForTarget = targetReason
		}
		return cleanLearnResult(learnedLimit, targetLimit, state, reason, reasonForTarget, projected, count)
	}

	pressureFree := input.Waiting == 0 && input.PreemptionDelta == 0 && !KVPressureActive(cfg, input.KVCacheUsage)
	representativeLoad := input.Estimate.RepresentativeLoad
	if input.Estimate.Confidence == "" {
		representativeLoad = cleanRepresentativeLoad(input, learned)
	}
	hasDecodeSignal := input.GenerationTPSValid && input.DecodeRunning >= cfg.UserTPSMinRun && input.DecodeRunning > 0 && cfg.UserTPSYellow > 0
	healthyTarget := cleanHealthyTarget(cfg)
	healthyRepresentativeLoad := pressureFree && representativeLoad && hasDecodeSignal && input.TTFTHealthy && input.UserTPS >= healthyTarget

	if healthyRepresentativeLoad {
		observedTarget := cleanHealthyObservedTarget(input, baseLimit)
		if observedTarget > projected {
			projected = observedTarget
			projectedReason = "healthy_observed_load"
		}
		if observedTarget > target {
			target = observedTarget
			targetReason = "healthy_observed_load"
		}
	}

	demandPressure := input.QueueCurrent > 0 || input.DynamicRejected > 0 || input.DemandPressure
	if learned < demandProbeFloor && pressureFree && baseLimit >= demandProbeFloor {
		demandHealthy := demandPressure && hasDecodeSignal && input.TTFTHealthy && input.UserTPS >= healthyTarget
		if demandHealthy && target < demandProbeFloor {
			target = demandProbeFloor
			targetReason = "demand_probe_floor"
		}
	}
	if learned < sparseProbeFloor && pressureFree && baseLimit >= sparseProbeFloor {
		sparseHealthyDecode := hasDecodeSignal && input.DecodeRunning <= sparseProbeFloor && input.UserTPS >= cfg.UserTPSYellow
		if demandPressure || sparseHealthyDecode || input.Previous.CapacityLearnState == "sparse_probe" {
			if target < sparseProbeFloor {
				target = sparseProbeFloor
				targetReason = "sparse_probe_floor"
			}
		}
	}

	if input.Freeze {
		return result(learned, target, "prefill_freeze", "prefill_transition", "", healthyCount)
	}

	badPressure := input.Waiting > 0 || input.PreemptionDelta > 0 || KVPressureActive(cfg, input.KVCacheUsage)
	if badPressure {
		return cleanPressureResult(cfg, input, baseLimit, learned, target, targetReason, projected, projectedReason)
	}

	if !hasDecodeSignal {
		if target > learned && learned < sparseProbeFloor && pressureFree {
			return result(learned, target, "sparse_probe", "low_traffic_probe_floor", "", healthyCount)
		}
		return result(learned, learned, "no_signal", "no_decode_signal", "learned_limit", healthyCount)
	}

	badQoS := input.UserTPS < cfg.UserTPSYellow && input.QOSYellowReady
	if badQoS {
		healthyCount = 0
		if !representativeLoad {
			return result(learned, learned, "hold_underutilized", "pig_low_underutilized", "learned_limit", healthyCount)
		}
		if projected > 0 && projected < learned {
			return result(projected, projected, "pig_down", "pig_below_target", projectedReason, healthyCount)
		}
		if projected > target {
			target = projected
			targetReason = projectedReason
		}
		return result(learned, target, "pig_hold", "pig_low_without_lower_target", "", healthyCount)
	}

	if learned < sparseProbeFloor && target > learned && healthyRepresentativeLoad && demandPressure {
		recovered := cleanSparseRecoveryLimit(input, target, baseLimit)
		if recovered > learned {
			return result(recovered, target, "sparse_recovery", "representative_sparse_recovery", "", healthyCount)
		}
	}

	if learned < sparseProbeFloor && target > learned && targetReason == "sparse_probe_floor" && pressureFree {
		return result(learned, target, "sparse_probe", "low_traffic_probe_floor", targetReason, healthyCount)
	}

	if target > learned && targetReason == "demand_probe_floor" && pressureFree {
		return result(learned, target, "demand_probe", "healthy_demand_probe_floor", targetReason, healthyCount)
	}

	if projected > 0 && projected < learned && representativeLoad {
		return result(learned, projected, "green_hold", "lower_estimate_without_pressure", projectedReason, decayHealthyCount(healthyCount))
	}

	if projected <= learned {
		return result(learned, projected, "converged", "at_or_above_target", projectedReason, healthyCount)
	}

	if !representativeLoad {
		return result(learned, projected, "hold_underutilized", "insufficient_representative_load", projectedReason, decayHealthyCount(healthyCount))
	}

	if input.UserTPS < healthyTarget || !input.TTFTHealthy {
		if !input.TTFTHealthy {
			return result(learned, projected, "hold_near_target", "ttft_not_healthy", projectedReason, decayHealthyCount(healthyCount))
		}
		return result(learned, projected, "hold_near_target", "healthy_margin_not_met", projectedReason, decayHealthyCount(healthyCount))
	}

	if !demandPressure {
		return result(learned, projected, "green_hold", "healthy_without_pressure", projectedReason, decayHealthyCount(healthyCount))
	}

	healthyCount++
	if healthyCount < cleanHealthyN(cfg) {
		return result(learned, projected, "probe_wait", "healthy_window_accumulating", projectedReason, healthyCount)
	}
	return result(num.MinInt(projected, learned+cleanStepUp(cfg, learned)), projected, "probe_up", "healthy_window_satisfied", projectedReason, 0)
}

func cleanLearnResult(learned, target int, state, reason, targetReason string, projected, healthyCount int) LearnResult {
	if reason == "" {
		reason = state
	}
	if targetReason == "" {
		targetReason = "unknown"
	}
	return LearnResult{
		LearnedLimit:   learned,
		TargetLimit:    target,
		State:          state,
		HealthyCount:   healthyCount,
		Reason:         reason,
		TargetReason:   targetReason,
		ProjectedLimit: projected,
	}
}

func cleanProjectedCapacity(cfg Config, capacityTPS float64, baseLimit int) int {
	if cfg.UserTPSYellow <= 0 || capacityTPS <= 0 || baseLimit <= 0 {
		return 0
	}
	safetyRatio := cfg.CapacitySafetyRatio
	if safetyRatio <= 0 {
		safetyRatio = 1
	}
	if safetyRatio > 1 {
		safetyRatio = 1
	}
	projected := int(math.Floor(capacityTPS * safetyRatio / cfg.UserTPSYellow))
	return num.ClampInt(projected, 1, baseLimit)
}

func cleanRepresentativeLoad(input CleanLearnInput, learned int) bool {
	if input.Waiting > 0 || input.QueueCurrent > 0 || input.DynamicRejected > 0 || input.DemandPressure {
		return true
	}
	threshold := 8
	if learned > 0 {
		threshold = int(math.Ceil(float64(learned) * representativeCapacityLoadRatio))
		threshold = num.ClampInt(threshold, 8, learned)
	}
	return input.Running >= threshold || input.DecodeRunning >= threshold || input.Running >= learned || input.DecodeRunning >= learned
}

func cleanLowConfidenceEstimate(estimate EstimateResult) bool {
	switch estimate.Confidence {
	case "stale", "low", "sparse":
		return true
	default:
		return false
	}
}

func cleanPressureResult(cfg Config, input CleanLearnInput, baseLimit, learned, target int, targetReason string, projected int, projectedReason string) LearnResult {
	severe := SeverePressure(cfg, input.Waiting, input.KVCacheUsage, input.PreemptionDelta) &&
		(input.PreemptionDelta > 0 || input.Running >= cfg.PressureLearnMinRun)
	if !severe {
		return cleanLearnResult(learned, target, "pressure_hold", "pressure_not_representative", targetReason, projected, 0)
	}
	pressureTarget, limited := OverloadPressureTarget(cfg, baseLimit, input.Running, input.DecodeRunning, input.Waiting, input.KVCacheUsage, input.PreemptionDelta, input.UserTPS, input.QOSRedReady)
	if !limited {
		pressureTarget = target
		projectedReason = targetReason
	} else {
		projectedReason = "pressure_target"
	}
	pressureTarget = num.ClampInt(pressureTarget, 1, baseLimit)
	if pressureTarget >= learned {
		return cleanLearnResult(learned, learned, "pressure_hold", "pressure_no_lower_target", "learned_limit", projected, 0)
	}
	return cleanLearnResult(pressureTarget, pressureTarget, "pressure_down", "severe_pressure", projectedReason, projected, 0)
}

func cleanStepUp(cfg Config, value int) int {
	stepRatio := cfg.CapacityStepUp
	if stepRatio <= 0 {
		stepRatio = 0.02
	}
	step := int(math.Ceil(float64(value) * stepRatio))
	if step < 1 {
		step = 1
	}
	return step
}

func cleanHealthyN(cfg Config) int {
	if cfg.CapacityHealthyN < 1 {
		return 1
	}
	return cfg.CapacityHealthyN
}

func decayHealthyCount(count int) int {
	if count > 0 {
		return count - 1
	}
	return 0
}

func cleanHealthyTarget(cfg Config) float64 {
	healthyTarget := cfg.UserTPSYellow * cfg.CapacityHealthyMul
	if healthyTarget <= 0 {
		healthyTarget = cfg.UserTPSYellow
	}
	return healthyTarget
}

func cleanHealthyObservedTarget(input CleanLearnInput, baseLimit int) int {
	observed := num.MaxInt(input.Running, input.DecodeRunning)
	if observed <= 0 || baseLimit <= 0 {
		return 0
	}
	headroom := int(math.Ceil(float64(observed) * ProvisionalGrowthRatio))
	minHeadroom := 4
	if input.QueueCurrent > 0 || input.DynamicRejected > 0 || input.DemandPressure {
		minHeadroom = 8
	}
	if headroom < minHeadroom {
		headroom = minHeadroom
	}
	target := observed + headroom
	if input.Estimate.RawLimit > 0 {
		target = num.MinInt(target, input.Estimate.RawLimit)
	}
	return num.ClampInt(target, 1, baseLimit)
}

func cleanSparseRecoveryLimit(input CleanLearnInput, target, baseLimit int) int {
	if target <= 0 || baseLimit <= 0 {
		return 0
	}
	observed := num.MaxInt(input.Running, input.DecodeRunning)
	if observed < sparseProbeFloor {
		observed = sparseProbeFloor
	}
	headroom := int(math.Ceil(float64(observed) * ProvisionalGrowthRatio))
	if headroom < 8 {
		headroom = 8
	}
	recovered := observed + headroom
	recovered = num.MinInt(recovered, target)
	return num.ClampInt(recovered, 1, baseLimit)
}
