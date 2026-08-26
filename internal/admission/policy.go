package admission

type admissionPolicy struct {
	tpsGate tpsGate
}

type policyDecision struct {
	action                Action
	reason                Reason
	scope                 ProtectionScope
	tpsSequenceLimit      int64
	tpsCurrentSequences   int64
	tpsPostAdmitSequences int64
	tpsQoSBudgeted        bool
	tpsDecisionResult     TPSDecisionResult
	tpsDecisionSubreason  TPSDecisionSubreason
}

func newAdmissionPolicy() admissionPolicy {
	return admissionPolicy{tpsGate: tpsGate{}}
}

func (p admissionPolicy) evaluateDemand(state ProjectedState, demand TPSRequestDemand) policyDecision {
	tps := p.tpsGate.evaluateAdditional(state, demand.gateDemand())
	decision := policyDecision{
		action:                ActionAdmit,
		reason:                ReasonOpen,
		tpsSequenceLimit:      tps.sequenceLimit,
		tpsCurrentSequences:   tps.currentSequences,
		tpsPostAdmitSequences: tps.postAdmitSequences,
		tpsQoSBudgeted:        tps.qosBudgeted,
		tpsDecisionResult:     tps.result,
		tpsDecisionSubreason:  tps.subreason,
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
	return decision
}
