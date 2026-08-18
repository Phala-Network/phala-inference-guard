package admission

import "math"

const (
	tpsHealthyHeadroomRatio = 1.05
	tpsNearReferenceRatio   = 0.95
	tpsWarmingSequenceLimit = int64(2)
)

type tpsGateDecision struct {
	gateDecision
	sequenceLimit      int64
	currentSequences   int64
	postAdmitSequences int64
	qosBudgeted        bool
}

type tpsGate struct{}

type tpsAdmissionDemand struct {
	additionalSequences int64
	outputLimitTokens   int64
	outputLimitKnown    bool
}

func (tpsGate) evaluate(state ProjectedState) tpsGateDecision {
	return (tpsGate{}).evaluateAdditional(state, tpsAdmissionDemand{additionalSequences: 1})
}

func (tpsGate) evaluateAdditional(state ProjectedState, demand tpsAdmissionDemand) tpsGateDecision {
	decision := tpsGateDecision{
		gateDecision: gateDecision{fits: true, reason: ReasonOpen},
	}
	snapshot := state.TPS
	if !snapshot.Enabled {
		return decision
	}
	current, postAdmit, valid := projectedTPSSequences(state, demand.additionalSequences)
	decision.currentSequences = current
	decision.postAdmitSequences = postAdmit
	if !valid || !validTPSSnapshot(snapshot) || snapshot.Reference <= 0 {
		decision.fits = false
		decision.reason = ReasonResourceExhausted
		return decision
	}
	if state.RawWaiting > 0 || state.PreemptionDelta > 0 {
		decision.sequenceLimit = current
		decision.fits = false
		decision.reason = ReasonTPSReference
		return decision
	}
	if !snapshot.Ready {
		decision.sequenceLimit = state.RawRunning
		if decision.sequenceLimit < tpsWarmingSequenceLimit {
			decision.sequenceLimit = tpsWarmingSequenceLimit
		}
		if snapshot.QualifiedSamples == 0 && snapshot.QualifiedSequenceSeconds == 0 &&
			state.RawRunning == tpsWarmingSequenceLimit && state.RawWaiting == 0 &&
			state.PreemptionDelta == 0 {
			decision.sequenceLimit = tpsWarmingSequenceLimit + 1
		}
		if currentRateLimit := tpsQualifiedCurrentRateSequenceLimit(state, snapshot); currentRateLimit > decision.sequenceLimit {
			decision.sequenceLimit = currentRateLimit
		}
		if postAdmit <= decision.sequenceLimit {
			return decision
		}
		decision.fits = false
		decision.reason = ReasonTPSReference
		return decision
	}
	decision.sequenceLimit = rateDerivedBaseSequenceLimit(snapshot)
	if state.RawRunning == 0 && state.GenerationDelta == 0 {
		if decision.sequenceLimit > tpsWarmingSequenceLimit {
			decision.sequenceLimit = tpsWarmingSequenceLimit
		}
	} else {
		if currentRateLimit := tpsQualifiedCurrentRateSequenceLimit(state, snapshot); currentRateLimit > decision.sequenceLimit {
			decision.sequenceLimit = currentRateLimit
		}
		if budgetLimit, budgeted := (qosBudgetForecast{}).sequenceLimit(state, current, decision.sequenceLimit, demand); budgetLimit > decision.sequenceLimit {
			decision.sequenceLimit = budgetLimit
			decision.qosBudgeted = budgeted && postAdmit <= budgetLimit
		}
	}
	if current == 0 || postAdmit <= decision.sequenceLimit {
		return decision
	}
	decision.fits = false
	decision.reason = ReasonTPSReference
	return decision
}

// tpsQualifiedCurrentRateSequenceLimit lets a healthy current observation
// recover one sequence beyond the running count when the long window still
// reflects earlier contention. A current sample never lowers healthy
// long-window capacity; waiting and preemption are handled before this path.
func tpsQualifiedCurrentRateSequenceLimit(state ProjectedState, snapshot TPSSnapshot) int64 {
	if state.RawRunning <= 0 || state.RawWaiting > 0 || state.PreemptionDelta > 0 ||
		state.GenerationDelta == 0 || !state.ObservationIntervalValid ||
		snapshot.QualifiedSamples == 0 || snapshot.QualifiedSequenceSeconds <= 0 {
		return 0
	}
	intervalSeconds := state.ObservationInterval.Seconds()
	currentAggregateTPS := float64(state.GenerationDelta) / intervalSeconds
	if !finiteNonnegative(currentAggregateTPS) || currentAggregateTPS <= 0 {
		return 0
	}
	currentMeanActiveTPS := currentAggregateTPS / float64(state.RawRunning)
	if !finiteNonnegative(currentMeanActiveTPS) ||
		currentMeanActiveTPS < snapshot.Reference*tpsHealthyHeadroomRatio {
		return 0
	}
	waveLimit, ok := addNonnegativeInt64(state.RawRunning, 1)
	if !ok {
		return 0
	}
	// One active sequence cannot reveal whether aggregate throughput scales with
	// concurrency, so permit exactly one low-flow probe. At higher concurrency,
	// below-reference expansion must use a request-bounded QoS budget lease.
	if state.RawRunning > 1 {
		projectedCurrentTPS := currentAggregateTPS / float64(waveLimit)
		if !finiteNonnegative(projectedCurrentTPS) ||
			projectedCurrentTPS < snapshot.Reference*tpsNearReferenceRatio {
			return 0
		}
	}
	return waveLimit
}

func projectedTPSSequences(state ProjectedState, additionalSequences int64) (current, postAdmit int64, valid bool) {
	if additionalSequences <= 0 {
		return math.MaxInt64, math.MaxInt64, false
	}
	tracked, ok := addNonnegativeInt64(state.PendingPrefillSequences, state.LocalActiveDecode)
	if !ok {
		return math.MaxInt64, math.MaxInt64, false
	}
	rawDemand, ok := addNonnegativeInt64(state.RawRunning, state.RawWaiting)
	if !ok {
		return math.MaxInt64, math.MaxInt64, false
	}
	current, ok = addNonnegativeInt64(rawDemand, state.UnobservedSequences)
	if !ok {
		return math.MaxInt64, math.MaxInt64, false
	}
	if tracked > current {
		current = tracked
	}
	postAdmit, ok = addNonnegativeInt64(current, additionalSequences)
	if !ok {
		return current, math.MaxInt64, false
	}
	return current, postAdmit, true
}

func rateDerivedBaseSequenceLimit(snapshot TPSSnapshot) int64 {
	quotient := snapshot.AggregateTPS / snapshot.Reference
	if quotient >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	if quotient >= 1 {
		return int64(math.Floor(quotient))
	}
	return 1
}
