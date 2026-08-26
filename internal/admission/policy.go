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

func newAdmissionPolicy() admissionPolicy {
	return admissionPolicy{tpsGate: tpsGate{}}
}

func (p admissionPolicy) evaluateDemand(
	state ProjectedState,
	demand TPSRequestDemand,
	bounds admissionBounds,
) policyDecision {
	tps := p.tpsGate.evaluate(state)
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
