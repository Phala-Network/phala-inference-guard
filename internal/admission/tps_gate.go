package admission

type gateDecision struct {
	fits   bool
	reason Reason
}

type tpsGateDecision struct {
	gateDecision
	result    TPSDecisionResult
	subreason TPSDecisionSubreason
}

type tpsGate struct{}

func (g tpsGate) evaluate(
	state ProjectedState,
	waitingImmediateThreshold int64,
	priority RequestPriority,
) tpsGateDecision {
	decision := tpsGateDecision{
		gateDecision: gateDecision{fits: true, reason: ReasonOpen},
		result:       TPSDecisionResultDisabled,
		subreason:    TPSDecisionSubreasonDisabled,
	}
	confirmedWaiting := priority == RequestPriorityBasic &&
		state.RawWaiting > 0 && state.PreviousRawWaiting > 0 &&
		state.ObservationIntervalValid
	immediateWaiting := priority == RequestPriorityBasic &&
		state.RawWaiting > 0 && waitingImmediateThreshold > 0 &&
		state.RawWaiting >= waitingImmediateThreshold
	if confirmedWaiting || immediateWaiting {
		decision.fits = false
		decision.reason = ReasonTPSReference
		decision.result = TPSDecisionResultProtect
		decision.subreason = TPSDecisionSubreasonWaiting
		return decision
	}
	snapshot := state.TPS
	if !snapshot.Enabled {
		return decision
	}
	decision.result = TPSDecisionResultAdmit
	decision.subreason = TPSDecisionSubreasonWarming
	if !validTPSSnapshot(snapshot) || snapshot.Reference <= 0 {
		decision.fits = false
		decision.reason = ReasonResourceExhausted
		decision.result = TPSDecisionResultInvalid
		decision.subreason = TPSDecisionSubreasonInvalidState
		return decision
	}
	if priority == RequestPriorityBasic && state.PreemptionDelta > 0 {
		decision.fits = false
		decision.reason = ReasonTPSReference
		decision.result = TPSDecisionResultProtect
		decision.subreason = TPSDecisionSubreasonPreemption
		return decision
	}
	if !snapshot.Ready {
		decision.subreason = TPSDecisionSubreasonWarming
		return decision
	}
	if !snapshot.Latest.Qualified {
		decision.subreason = TPSDecisionSubreasonNoCurrentEvidence
		return decision
	}
	if snapshot.MeanActiveTPS >= snapshot.Reference {
		decision.subreason = TPSDecisionSubreasonHealthyWindow
		return decision
	}
	if snapshot.Latest.MeanActiveTPS >= snapshot.Reference {
		decision.subreason = TPSDecisionSubreasonRecoveredCurrent
		return decision
	}
	decision.fits = false
	decision.reason = ReasonTPSReference
	decision.result = TPSDecisionResultProtect
	decision.subreason = TPSDecisionSubreasonBelowReference
	return decision
}
