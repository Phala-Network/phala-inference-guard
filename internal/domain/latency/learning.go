package latency

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type LearnInput struct {
	PreviousSource       string
	PreviousLearnedLimit int
	PreviousTargetLimit  int
	BaseLimit            int
	Running              int
	StepUpRatio          float64
	Observation          Observation
	Assessment           Assessment
	RecoveryLoadLimit    int
	DemandPressure       bool
	RequireLoadSignal    bool
	RepresentativeLoad   bool
}

type LearnResult struct {
	LearnedLimit  int
	TargetLimit   int
	State         string
	Reason        string
	TargetReason  string
	Limit         int
	HighCount     int
	TailHighCount int
	HealthyCount  int
}

func LearnCap(input LearnInput) LearnResult {
	assessment := input.Assessment
	result := LearnResult{
		LearnedLimit:  input.BaseLimit,
		TargetLimit:   input.BaseLimit,
		State:         "disabled",
		Reason:        "disabled",
		TargetReason:  "base_limit",
		Limit:         0,
		HighCount:     assessment.HighCount,
		TailHighCount: assessment.TailHighCount,
		HealthyCount:  assessment.HealthyCount,
	}
	if input.BaseLimit <= 0 {
		return result
	}

	learned := input.PreviousLearnedLimit
	if input.PreviousSource != "metrics" || learned <= 0 {
		learned = input.BaseLimit
	}
	learned = num.ClampInt(learned, MinLimit, input.BaseLimit)
	recoveryLoadLimit := input.RecoveryLoadLimit
	if recoveryLoadLimit <= 0 || recoveryLoadLimit > learned {
		recoveryLoadLimit = learned
	}
	recoveryLoadLimit = num.ClampInt(recoveryLoadLimit, MinLimit, learned)
	minRunning := effectiveMinRunning(recoveryLoadLimit)

	target := input.PreviousTargetLimit
	targetReason := "previous_target"
	if target <= 0 {
		target = learned
		targetReason = "learned_limit"
	}
	target = num.ClampInt(target, MinLimit, input.BaseLimit)

	limit := learned
	state := "no_signal"
	stepRatio := input.StepUpRatio
	if stepRatio <= 0 {
		stepRatio = 0.02
	}
	recoveryStep := func(value int, ratio float64) int {
		if ratio < stepRatio {
			ratio = stepRatio
		}
		step := int(math.Ceil(float64(value) * ratio))
		if step < 1 {
			step = 1
		}
		return step
	}
	nextProbeTarget := func(value int) (int, string) {
		if value >= input.BaseLimit {
			return input.BaseLimit, "base_limit"
		}
		ratio := RecoveryStepRatio
		reason := "recovery_probe"
		if input.Observation.SmoothedP95 > 0 && input.Observation.SmoothedP95 <= TargetSeconds*FastRecoverySignalRatio {
			ratio = FastRecoveryStepRatio
			reason = "fast_recovery_probe"
		}
		return num.MinInt(input.BaseLimit, value+recoveryStep(value, ratio)), reason
	}

	hasLatencySignal := assessment.High || assessment.TailHigh
	latencySignalQualified := !input.RequireLoadSignal || input.RepresentativeLoad || input.DemandPressure
	if (input.Running < minRunning && !assessment.Healthy && !hasLatencySignal) || ((!input.Observation.Valid || input.Observation.Count < MinWindowCount) && !assessment.Healthy && !hasLatencySignal) {
		if target > learned {
			target = learned
			targetReason = "learned_limit"
		}
		limit := learned
		if input.DemandPressure {
			limit = input.BaseLimit
		}
		return newLearnResult(learned, target, state, "insufficient_latency_signal", targetReason, limit, assessment)
	}
	if hasLatencySignal && !latencySignalQualified {
		if target > learned {
			target = learned
			targetReason = "learned_limit"
		}
		return newLearnResult(learned, target, "ttft_hold", "latency_signal_underutilized", targetReason, learned, assessment)
	}
	if assessment.High || assessment.TailHigh {
		state = "ttft_wait"
		reason := "ttft_high_pending"
		if assessment.YellowReady {
			reason = "ttft_above_target"
			if assessment.RedReady {
				reason = "ttft_red_latency"
			}
			targetReason = ttftLatencyTargetReason(assessment)
			ratio := downRatio(assessment.Signal)
			learningLoad := input.Running
			if learningLoad < minRunning {
				learningLoad = minRunning
			}
			target = int(math.Floor(float64(learningLoad) * ratio))
			target = num.ClampInt(target, MinLimit, learned)
			if target < learned {
				learned = target
				state = "ttft_down"
			} else {
				state = "ttft_hold_high"
				reason = "ttft_no_lower_target"
			}
		}
		return newLearnResult(learned, target, state, reason, targetReason, learned, assessment)
	}
	if assessment.Healthy {
		state = "ttft_healthy"
		target = learned
		targetReason = "learned_limit"
		reason := "ttft_healthy"
		if learned < input.BaseLimit {
			if input.Running < minRunning {
				target, targetReason = nextProbeTarget(learned)
				reason = "healthy_low_load_probe"
			} else {
				probeLimit := num.ClampInt(recoveryLoadLimit, minRunning, learned)
				probeThreshold := int(math.Ceil(float64(probeLimit) * ProbeLoadRatio))
				probeThreshold = num.ClampInt(probeThreshold, minRunning, probeLimit)
				if input.Running < probeThreshold {
					assessment.HealthyCount = 0
					return newLearnResult(learned, target, state, "healthy_load_below_probe_threshold", targetReason, learned, assessment)
				}
				target, targetReason = nextProbeTarget(learned)
				reason = "healthy_probe"
			}
		}
		if target > learned && assessment.HealthyCount >= HealthyConsecutive {
			learned = target
			assessment.HealthyCount = 0
			state = "ttft_probe_up"
			reason = "healthy_window_satisfied"
		} else if target > learned {
			reason = "healthy_window_accumulating"
		}
		return newLearnResult(learned, target, state, reason, targetReason, learned, assessment)
	}
	if target > learned {
		target = learned
		targetReason = "learned_limit"
	}
	return newLearnResult(learned, target, "ttft_hold", "ttft_not_healthy", targetReason, limit, assessment)
}

func newLearnResult(learned, target int, state, reason, targetReason string, limit int, assessment Assessment) LearnResult {
	if reason == "" {
		reason = state
	}
	if targetReason == "" {
		targetReason = "unknown"
	}
	return LearnResult{
		LearnedLimit:  learned,
		TargetLimit:   target,
		State:         state,
		Reason:        reason,
		TargetReason:  targetReason,
		Limit:         limit,
		HighCount:     assessment.HighCount,
		TailHighCount: assessment.TailHighCount,
		HealthyCount:  assessment.HealthyCount,
	}
}

func ttftLatencyTargetReason(assessment Assessment) string {
	tailSignal := assessment.P99Signal * P99SignalWeight
	if assessment.RedReady && assessment.TailHigh && assessment.P99Signal >= P99RedSeconds {
		tailSignal = assessment.P99Signal
	}
	switch {
	case assessment.TailHigh && (!assessment.High || tailSignal >= assessment.P95Signal):
		return "p99_latency"
	case assessment.High:
		return "p95_latency"
	case assessment.TailHigh:
		return "p99_latency"
	default:
		return "latency_signal"
	}
}

func downRatio(signal float64) float64 {
	switch {
	case signal >= 8*TargetSeconds:
		return 0.70
	case signal >= 4*TargetSeconds:
		return 0.78
	case signal >= 2*TargetSeconds:
		return 0.86
	default:
		return 0.92
	}
}
