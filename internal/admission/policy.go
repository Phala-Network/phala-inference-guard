package admission

import predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"

type admissionPolicy struct {
	minimumWork predictive.RequestWork
	contextGate contextGate
	kvGate      kvGate
	prefillGate prefillGate
	tpsGate     tpsGate
}

func (p admissionPolicy) withObservedPrefillCost(
	state ProjectedState,
	work predictive.RequestWork,
) predictive.RequestWork {
	work.PrefillComputeTokens = p.prefillGate.computeTokens(state, work.PrefillInputTokens)
	work.FirstBytePendingPrefillComputeTokens = work.PrefillComputeTokens
	if work.FirstBytePendingPrefillComputeTokens > work.FirstBytePendingPrefillInputTokens {
		work.FirstBytePendingPrefillComputeTokens = work.FirstBytePendingPrefillInputTokens
	}
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
	minimumWork, err := capability.minimumWork(workProfile)
	if err != nil {
		return admissionPolicy{}, err
	}
	return admissionPolicy{
		minimumWork: minimumWork,
		contextGate: contextGate{
			maximumInputTokens: capability.MaximumInputTokens,
			maxModelLenTokens:  capability.MaxModelLenTokens,
		},
		kvGate: kvGate{hardLimitTokens: capability.KVHardLimitTokens},
		prefillGate: prefillGate{
			regularTokens:         capability.PrefillRegularTokens,
			exclusiveTokens:       capability.PrefillExclusiveTokens,
			quiescentTokens:       capability.PrefillQuiescentTokens,
			contendedBudgetTokens: capability.PrefillContendedBudgetTokens,
			aggregateBudgetTokens: capability.PrefillAggregateBudgetTokens,
		},
		tpsGate: tpsGate{qosBudget: qosBudget},
	}, nil
}

func (p admissionPolicy) evaluate(state ProjectedState, work predictive.RequestWork) policyDecision {
	decision := p.evaluateCandidate(state, work)
	if decision.action == ActionAdmit {
		return decision
	}
	if decision.reason == ReasonInputLimit || decision.reason == ReasonInvalidRequest {
		decision.scope = ProtectionRequest
		return decision
	}
	minimum := p.evaluateCandidate(state, p.minimumWork)
	if minimum.action == ActionAdmit {
		decision.scope = ProtectionRequest
	} else {
		decision.scope = ProtectionLoad
	}
	return decision
}

func (p admissionPolicy) evaluateCandidate(state ProjectedState, work predictive.RequestWork) policyDecision {
	kv, postAdmit := p.kvGate.evaluate(state, work)
	prefill, class, postPending := p.prefillGate.evaluate(state, work)
	tps := p.tpsGate.evaluateAdditional(state, tpsAdmissionDemand{
		additionalSequences: work.Estimate.DecodeSequences,
		outputLimitTokens:   work.Estimate.OutputLimitTokens,
		outputLimitKnown:    work.Estimate.OutputLimitKnown,
	})
	decision := policyDecision{
		action:                    ActionProtect,
		reason:                    ReasonInvalidRequest,
		prefillClass:              class,
		postAdmitKVTokens:         postAdmit,
		pendingPrefillTokensAfter: postPending,
		tpsSequenceLimit:          tps.sequenceLimit,
		tpsCurrentSequences:       tps.currentSequences,
		tpsPostAdmitSequences:     tps.postAdmitSequences,
		tpsQoSBudgeted:            tps.qosBudgeted,
		tpsDecisionResult:         tps.result,
		tpsDecisionSubreason:      tps.subreason,
	}
	if context := p.contextGate.evaluate(work); !context.fits {
		decision.reason = context.reason
		return decision
	}
	if !kv.fits {
		decision.reason = kv.reason
		return decision
	}
	if !prefill.fits {
		decision.reason = prefill.reason
		return decision
	}
	if !tps.fits {
		decision.reason = tps.reason
		return decision
	}
	decision.action = ActionAdmit
	decision.reason = ReasonOpen
	return decision
}
