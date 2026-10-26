package admission

type admissionPolicy struct {
	tpsGate tpsGate
}

type policyDecision struct {
	action                   Action
	reason                   Reason
	scope                    ProtectionScope
	projectedRunning         int64
	projectedWindowSequences int64
	tpsDecisionResult        TPSDecisionResult
	tpsDecisionSubreason     TPSDecisionSubreason
}

func newAdmissionPolicy(backendTierScheduling bool, warmPromptTokens int64) admissionPolicy {
	return admissionPolicy{tpsGate: tpsGate{
		backendTierScheduling: backendTierScheduling,
		warmPromptTokens:      warmPromptTokens,
	}}
}

func (p admissionPolicy) evaluateDemand(
	state ProjectedState,
	demand TPSRequestDemand,
	bounds admissionBounds,
) policyDecision {
	tps := p.tpsGate.evaluateDemand(state, bounds.waitingAllowance, demand)
	decision := policyDecision{
		action:               ActionAdmit,
		reason:               ReasonOpen,
		tpsDecisionResult:    tps.result,
		tpsDecisionSubreason: tps.subreason,
	}
	if !tps.fits {
		decision.action = ActionProtect
		decision.reason = tps.reason
		if tps.reason == ReasonTPSReference {
			decision.scope = ProtectionLoad
		} else {
			decision.scope = ProtectionAvailability
		}
		return decision
	}
	if !demand.Tier.Sheddable() {
		// Priority is subject to the TPS gate above unless the backend
		// schedules by tier, and non-TPS load protections are intentionally
		// soft for it. The controller
		// continues to create a normal reservation so lifecycle accounting and
		// terminal cleanup remain identical to the other tiers.
		var runningOK, windowOK bool
		decision.projectedRunning, runningOK = addNonnegativeInt64(
			state.RawRunning,
			demand.DecodeSequences,
		)
		decision.projectedWindowSequences, windowOK = addNonnegativeInt64(
			state.UnobservedSequences,
			demand.DecodeSequences,
		)
		if !runningOK || !windowOK {
			decision.action = ActionProtect
			decision.reason = ReasonResourceExhausted
			decision.scope = ProtectionAvailability
		}
		return decision
	}
	bound := bounds.evaluate(state, demand)
	decision.projectedRunning = bound.projectedRunning
	decision.projectedWindowSequences = bound.projectedWindowSequences
	if !bound.fits {
		decision.action = ActionProtect
		decision.reason = bound.reason
		if bound.reason == ReasonRunningLimit || bound.reason == ReasonWindowConcurrency {
			decision.scope = ProtectionLoad
		} else {
			decision.scope = ProtectionAvailability
		}
	}
	return decision
}
