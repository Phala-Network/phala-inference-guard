package kvadmission

import (
	"math"
	"time"
)

func Evaluate(now time.Time, cost Cost, backends []BackendSnapshot, reservations map[string]int64, policy Policy) Decision {
	base := Decision{
		Reason:              ReasonCapacityUnknown,
		EstimatedInputLow:   cost.EstimatedInputLow,
		EstimatedInputHigh:  cost.EstimatedInputHigh,
		BoundedDecodeTokens: cost.BoundedDecodeTokens,
		DecodeDriftTokens:   policy.DecodeDriftTokens,
	}
	if !cost.Supported {
		base.Reason = ReasonUnsupportedRequest
		return base
	}

	var best *Decision
	seen := make(map[Reason]bool)
	failures := make(map[Reason]Decision)
	for _, snapshot := range backends {
		candidate, reason := evaluateBackend(now, cost, snapshot, reservations[snapshot.Name], policy)
		candidate.Reason = reason
		seen[reason] = true
		if reason != ReasonFit {
			if current, ok := failures[reason]; !ok || failureCandidateBefore(candidate, current) {
				failures[reason] = candidate
			}
			continue
		}
		if best == nil || betterCandidate(candidate, *best) {
			copy := candidate
			best = &copy
		}
	}
	if best != nil {
		return *best
	}
	base.Reason = aggregateFailureReason(seen)
	if failure, ok := failures[base.Reason]; ok {
		return failure
	}
	return base
}

func evaluateBackend(now time.Time, cost Cost, snapshot BackendSnapshot, reserved int64, policy Policy) (Decision, Reason) {
	decision := Decision{
		Backend:                  snapshot.Name,
		BackendKind:              snapshot.Kind,
		EstimatedInputLow:        cost.EstimatedInputLow,
		EstimatedInputHigh:       cost.EstimatedInputHigh,
		BoundedDecodeTokens:      cost.BoundedDecodeTokens,
		ObservedTokens:           snapshot.UsedTokens,
		UnabsorbedReservedTokens: reserved,
		DecodeDriftTokens:        policy.DecodeDriftTokens,
		CapacityTokens:           snapshot.CapacityTokens,
	}
	if !snapshot.Updated.IsZero() {
		decision.SampleAge = now.Sub(snapshot.Updated)
		if decision.SampleAge < 0 {
			decision.SampleAge = 0
		}
	}
	if snapshot.Failed || snapshot.Updated.IsZero() || !snapshot.TokenMetricsValid || snapshot.CapacityTokens <= 0 || snapshot.UsedTokens < 0 {
		return decision, ReasonCapacityUnknown
	}
	if policy.MaxMetricsAge > 0 && decision.SampleAge > policy.MaxMetricsAge {
		return decision, ReasonStaleMetrics
	}
	budget, ok := policy.BudgetFor(snapshot.Kind)
	if !ok || !validBudget(budget) {
		return decision, ReasonCapacityUnknown
	}
	decision.TargetTokens = ratioTokens(snapshot.CapacityTokens, budget.TargetRatio)
	decision.HardBudgetTokens = ratioTokens(snapshot.CapacityTokens, budget.HardRatio)
	decision.EmergencyTokens = ratioTokens(snapshot.CapacityTokens, budget.EmergencyRatio)
	if snapshot.UsedTokens >= decision.EmergencyTokens {
		return decision, ReasonEmergencyRed
	}
	if snapshot.Waiting > 0 {
		return decision, ReasonBackendWaiting
	}
	if snapshot.PreemptionCooldown {
		return decision, ReasonPreemptionCooldown
	}
	decision.ProjectedHighTokens = snapshot.UsedTokens + reserved + policy.DecodeDriftTokens + cost.ProjectedHigh()
	decision.ProjectedRatio = float64(decision.ProjectedHighTokens) / float64(snapshot.CapacityTokens)
	if decision.ProjectedHighTokens > decision.HardBudgetTokens {
		return decision, ReasonOverBudget
	}
	decision.Reason = ReasonFit
	decision.SpillHeadroom = decision.ProjectedHighTokens > decision.TargetTokens
	return decision, ReasonFit
}

func betterCandidate(candidate, current Decision) bool {
	if candidate.SpillHeadroom != current.SpillHeadroom {
		return !candidate.SpillHeadroom
	}
	if candidate.ProjectedRatio != current.ProjectedRatio {
		return candidate.ProjectedRatio < current.ProjectedRatio
	}
	return candidate.Backend < current.Backend
}

func failureCandidateBefore(candidate, current Decision) bool {
	if candidate.Backend == current.Backend {
		return false
	}
	if current.Backend == "" {
		return true
	}
	if candidate.Backend == "" {
		return false
	}
	return candidate.Backend < current.Backend
}

func aggregateFailureReason(seen map[Reason]bool) Reason {
	for _, reason := range []Reason{
		ReasonEmergencyRed,
		ReasonBackendWaiting,
		ReasonPreemptionCooldown,
		ReasonOverBudget,
		ReasonStaleMetrics,
		ReasonCapacityUnknown,
	} {
		if seen[reason] {
			return reason
		}
	}
	return ReasonCapacityUnknown
}

func validBudget(budget Budget) bool {
	return budget.TargetRatio > 0 &&
		budget.TargetRatio <= budget.HardRatio &&
		budget.HardRatio < budget.EmergencyRatio &&
		budget.EmergencyRatio <= 1
}

func ratioTokens(capacity int64, ratio float64) int64 {
	return int64(math.Floor(float64(capacity) * ratio))
}
