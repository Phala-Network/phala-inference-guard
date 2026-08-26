package admission

import predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"

type admissionPolicy struct {
	minimumWork predictive.RequestWork
	tpsGate     tpsGate
}

func (p admissionPolicy) withObservedPrefillCost(
	state ProjectedState,
	work predictive.RequestWork,
) predictive.RequestWork {
	return work
}

type policyDecision struct {
	action                    Action
	reason                    Reason
	scope                     ProtectionScope
	prefillClass              PrefillClass
	postAdmitKVTokens         int64
	pendingPrefillTokensAfter int64
	tpsSequenceLimit          int64
	tpsCurrentSequences       int64
	tpsPostAdmitSequences     int64
	tpsQoSBudgeted            bool
	tpsDecisionResult         TPSDecisionResult
	tpsDecisionSubreason      TPSDecisionSubreason
}

func newAdmissionPolicy(
	capability Capability,
	workProfile predictive.BackendExecutionProfile,
) (admissionPolicy, error) {
	return newAdmissionPolicyWithQoSBudget(capability, workProfile, defaultQoSBudgetForecast())
}

func newAdmissionPolicyWithQoSBudget(
	capability Capability,
	workProfile predictive.BackendExecutionProfile,
	qosBudget qosBudgetForecast,
) (admissionPolicy, error) {
	return admissionPolicy{
		minimumWork: predictive.RequestWork{},
		tpsGate:     tpsGate{qosBudget: qosBudget},
	}, nil
}

func (p admissionPolicy) evaluate(state ProjectedState, work predictive.RequestWork) policyDecision {
	demand, err := tpsRequestDemandFromEstimate(work.Estimate)
	if err != nil {
		return policyDecision{action: ActionProtect, reason: ReasonInvalidRequest, scope: ProtectionRequest}
	}
	return p.evaluateDemand(state, demand)
}

func (p admissionPolicy) evaluateCandidate(state ProjectedState, work predictive.RequestWork) policyDecision {
	return p.evaluate(state, work)
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
