package capacity

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type PrefillInput struct {
	Config           Config
	Previous         Previous
	BaseLimit        int
	GlobalLimit      int
	Running          int
	DecodeRunning    int
	Waiting          int
	PrefillProtected int
	CapacityTPS      float64
}

type PrefillLimitResult struct {
	Limit        int
	Reason       string
	TargetReason string
}

func PrefillLimit(input PrefillInput) int {
	return EvaluatePrefillLimit(input).Limit
}

func EvaluatePrefillLimit(input PrefillInput) PrefillLimitResult {
	cfg := input.Config
	if !cfg.UserTPSEnabled || input.BaseLimit <= 0 || input.Running <= 0 {
		return PrefillLimitResult{Limit: input.BaseLimit, Reason: "disabled", TargetReason: "base_limit"}
	}
	minLimit := num.MaxInt(4, cfg.PressureMinLimit)
	if minLimit > input.BaseLimit {
		minLimit = input.BaseLimit
	}
	limit := input.BaseLimit
	limited := false
	reason := "base_limit"
	targetReason := "base_limit"
	apply := func(target int, candidateReason, candidateTargetReason string) {
		if target < minLimit {
			target = minLimit
		}
		if target < limit {
			limit = target
			limited = true
			reason = candidateReason
			targetReason = candidateTargetReason
		}
	}

	observedCap := 0
	observeCap := func(value int) {
		if value <= 0 || value >= input.BaseLimit {
			return
		}
		if value > observedCap {
			observedCap = value
		}
	}
	if input.Previous.Source == "metrics" {
		observeCap(input.Previous.CapacityLearnedLimit)
		observeCap(input.Previous.CapacityTargetLimit)
		observeCap(input.Previous.GlobalLimit)
	}
	if cfg.UserTPSYellow > 0 && input.CapacityTPS > 0 {
		observeCap(int(math.Floor(input.CapacityTPS / cfg.UserTPSYellow)))
	}
	if observedCap > input.BaseLimit {
		observedCap = input.BaseLimit
	}
	prefillFloor := 0
	if observedCap > 0 {
		prefillFloor = num.ClampInt(int(math.Ceil(float64(observedCap)*prefillPressureFloorRatio)), prefillPressureFloorMin, prefillPressureFloorMax)
		if input.GlobalLimit > 0 && prefillFloor > input.GlobalLimit {
			prefillFloor = input.GlobalLimit
		}
	}
	raiseToPrefillFloor := func() {
		if limited && prefillFloor > 0 && limit < prefillFloor {
			limit = prefillFloor
			reason = "prefill_floor"
			targetReason = "observed_cap_floor"
		}
	}

	threshold := 16
	if observedCap > 0 {
		threshold = num.ClampInt(int(math.Ceil(float64(observedCap)*0.75)), 12, 24)
	}
	if input.Waiting > 0 {
		if observedCap > 0 {
			waitingTarget := observedCap
			if observedCap >= 8 {
				waitingTarget = int(math.Floor(float64(observedCap) * 0.80))
				if waitingTarget < 1 {
					waitingTarget = 1
				}
			}
			apply(waitingTarget, "backend_waiting", "observed_cap")
		}
		if input.Running >= threshold {
			headroom := cfg.PressureHeadroom
			if headroom < 1 {
				headroom = 1
			}
			target := input.Running - headroom
			if input.Waiting > 1 {
				target -= input.Waiting - 1
			}
			apply(target, "backend_waiting", "running_headroom")
		}
		raiseToPrefillFloor()
		return PrefillLimitResult{Limit: limit, Reason: reason, TargetReason: targetReason}
	}

	if observedCap > 0 && input.Running >= observedCap && input.hasRunningObservedCapPrefillEvidence(observedCap, threshold) {
		apply(observedCap, "running_at_observed_cap", "observed_cap")
		raiseToPrefillFloor()
		return PrefillLimitResult{Limit: limit, Reason: reason, TargetReason: targetReason}
	}
	if input.PrefillProtected >= threshold {
		if observedCap > 0 {
			apply(observedCap, "prefill_protected", "observed_cap")
		} else {
			apply(threshold+4, "prefill_protected", "threshold")
		}
	}
	return PrefillLimitResult{Limit: limit, Reason: reason, TargetReason: targetReason}
}

func (input PrefillInput) hasRunningObservedCapPrefillEvidence(observedCap, threshold int) bool {
	return input.hasStrongPrefillEvidence(input.PrefillProtected, observedCap, threshold) ||
		input.hasStrongPrefillEvidence(input.Running-input.DecodeRunning, observedCap, threshold)
}

func (input PrefillInput) hasStrongPrefillEvidence(evidence, observedCap, threshold int) bool {
	if evidence <= 0 || input.Running <= 0 {
		return false
	}
	minEvidence := 4
	if observedCap > 0 {
		minEvidence = num.ClampInt(int(math.Ceil(float64(observedCap)*0.20)), 4, threshold)
	}
	if minEvidence < num.MaxInt(2, input.Config.PressureHeadroom) {
		minEvidence = num.MaxInt(2, input.Config.PressureHeadroom)
	}
	if evidence < minEvidence {
		return false
	}
	if evidence*4 < input.Running {
		return false
	}
	return true
}

// MeaningfulPrefillProtected returns true only when local prefill-grace tracking
// covers enough of the active load to justify freezing throughput learning.
func MeaningfulPrefillProtected(running, protected int) bool {
	if protected <= 0 || running <= 0 {
		return false
	}
	if protected >= running {
		return true
	}
	minEvidence := 2
	if running >= 16 {
		minEvidence = 4
	}
	if running >= 32 {
		minEvidence = 8
	}
	if protected < minEvidence {
		return false
	}
	return protected*4 >= running
}
