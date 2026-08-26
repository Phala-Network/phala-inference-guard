package admission

type admissionBoundsDecision struct {
	gateDecision
	projectedRunning         int64
	projectedWindowSequences int64
}

type admissionBounds struct {
	windowConcurrency int64
	runningLimit      int64
}

func (b admissionBounds) evaluate(state ProjectedState, demand TPSRequestDemand) admissionBoundsDecision {
	decision := admissionBoundsDecision{gateDecision: gateDecision{fits: true, reason: ReasonOpen}}
	if !demand.valid() || b.windowConcurrency <= 0 || b.runningLimit < 0 {
		decision.fits = false
		decision.reason = ReasonResourceExhausted
		return decision
	}
	raw, ok := addNonnegativeInt64(state.RawRunning, state.RawWaiting)
	if ok {
		raw, ok = addNonnegativeInt64(raw, state.UnobservedSequences)
	}
	if ok {
		decision.projectedRunning, ok = addNonnegativeInt64(raw, demand.DecodeSequences)
	}
	if !ok {
		decision.fits = false
		decision.reason = ReasonResourceExhausted
		return decision
	}
	decision.projectedWindowSequences, ok = addNonnegativeInt64(
		state.UnobservedSequences,
		demand.DecodeSequences,
	)
	if !ok {
		decision.fits = false
		decision.reason = ReasonResourceExhausted
		return decision
	}
	if b.runningLimit > 0 && decision.projectedRunning > b.runningLimit {
		decision.fits = false
		decision.reason = ReasonRunningLimit
		return decision
	}
	if decision.projectedWindowSequences > b.windowConcurrency {
		decision.fits = false
		decision.reason = ReasonWindowConcurrency
	}
	return decision
}
