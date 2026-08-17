package admission

import "math"

const (
	tpsHealthyHeadroomRatio  = 1.05
	tpsExplorationFloorRatio = 0.95
	tpsWarmingSequenceLimit  = int64(2)
)

type tpsGateDecision struct {
	gateDecision
	sequenceLimit      int64
	currentSequences   int64
	postAdmitSequences int64
}

type tpsGate struct{}

func (tpsGate) evaluate(state ProjectedState) tpsGateDecision {
	decision := tpsGateDecision{
		gateDecision: gateDecision{fits: true, reason: ReasonOpen},
	}
	snapshot := state.TPS
	if !snapshot.Enabled {
		return decision
	}
	current, postAdmit, valid := projectedTPSSequences(state)
	decision.currentSequences = current
	decision.postAdmitSequences = postAdmit
	if !valid || !validTPSSnapshot(snapshot) || snapshot.Reference <= 0 {
		decision.fits = false
		decision.reason = ReasonResourceExhausted
		return decision
	}
	if !snapshot.Ready {
		decision.sequenceLimit = state.RawRunning
		if decision.sequenceLimit < tpsWarmingSequenceLimit {
			decision.sequenceLimit = tpsWarmingSequenceLimit
		}
		if qualifiedLimit := tpsQualifiedWarmingSequenceLimit(state, snapshot); qualifiedLimit > decision.sequenceLimit {
			decision.sequenceLimit = qualifiedLimit
		}
		if postAdmit <= decision.sequenceLimit {
			return decision
		}
		decision.fits = false
		decision.reason = ReasonTPSReference
		return decision
	}
	decision.sequenceLimit = rateDerivedSequenceLimit(snapshot)
	if current == 0 || postAdmit <= decision.sequenceLimit {
		return decision
	}
	decision.fits = false
	decision.reason = ReasonTPSReference
	return decision
}

// tpsQualifiedWarmingSequenceLimit lets a cold window ramp only to capacity
// already demonstrated by the current coherent output sample. It never spends
// historical surplus or predicts throughput growth beyond observed aggregate
// TPS; reservations still consume the limit atomically until the next poll.
func tpsQualifiedWarmingSequenceLimit(state ProjectedState, snapshot TPSSnapshot) int64 {
	if state.RawRunning <= 0 || state.RawWaiting > 0 || state.PreemptionDelta > 0 ||
		state.GenerationDelta == 0 || !state.ObservationIntervalValid ||
		snapshot.QualifiedSamples == 0 || snapshot.QualifiedSequenceSeconds <= 0 {
		return 0
	}
	return rateDerivedSequenceLimit(snapshot)
}

func projectedTPSSequences(state ProjectedState) (current, postAdmit int64, valid bool) {
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
	postAdmit, ok = addNonnegativeInt64(current, 1)
	if !ok {
		return current, math.MaxInt64, false
	}
	return current, postAdmit, true
}

func rateDerivedSequenceLimit(snapshot TPSSnapshot) int64 {
	quotient := snapshot.AggregateTPS / snapshot.Reference
	limit := int64(1)
	if quotient >= float64(math.MaxInt64) {
		limit = math.MaxInt64
	} else if quotient >= 1 {
		limit = int64(math.Floor(quotient))
	}
	if limit < math.MaxInt64 && snapshot.MeanActiveTPS >= snapshot.Reference*tpsHealthyHeadroomRatio {
		exploration := limit + 1
		projectedTPS := snapshot.AggregateTPS / float64(exploration)
		if projectedTPS >= snapshot.Reference*tpsExplorationFloorRatio {
			limit = exploration
		}
	}
	return limit
}
