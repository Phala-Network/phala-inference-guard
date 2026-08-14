package admission

import "math"

const (
	tpsHealthyHeadroomRatio  = 1.05
	tpsExplorationFloorRatio = 0.95
	tpsWarmingSequenceLimit  = int64(2)
)

type tpsGateDecision struct {
	gateDecision
	sequenceLimit       int64
	currentSequences    int64
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

func projectedTPSSequences(state ProjectedState) (current, postAdmit int64, valid bool) {
	tracked, ok := addNonnegativeInt64(state.PendingPrefillSequences, state.LocalActiveDecode)
	if !ok {
		return math.MaxInt64, math.MaxInt64, false
	}
	current = state.RawRunning
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
