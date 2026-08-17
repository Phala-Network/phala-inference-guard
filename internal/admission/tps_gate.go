package admission

import "math"

const (
	tpsHealthyHeadroomRatio  = 1.05
	tpsExplorationFloorRatio = 0.95
	tpsWarmingSequenceLimit  = int64(2)
	tpsHeadroomWaveLimit     = int64(4)
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
		if headroomLimit := tpsHeadroomSequenceLimit(state, snapshot); headroomLimit > decision.sequenceLimit {
			decision.sequenceLimit = headroomLimit
		}
		if postAdmit <= decision.sequenceLimit {
			return decision
		}
		decision.fits = false
		decision.reason = ReasonTPSReference
		return decision
	}
	decision.sequenceLimit = rateDerivedSequenceLimit(snapshot)
	if headroomLimit := tpsHeadroomSequenceLimit(state, snapshot); headroomLimit > decision.sequenceLimit {
		decision.sequenceLimit = headroomLimit
	}
	if current == 0 || postAdmit <= decision.sequenceLimit {
		return decision
	}
	decision.fits = false
	decision.reason = ReasonTPSReference
	return decision
}

// tpsHeadroomSequenceLimit spends only QoS surplus already earned by the
// long-window mean. Reservations consume the returned per-observation wave
// atomically through projectedTPSSequences; the next metrics sample either
// replenishes the surplus with real output or stops further expansion.
func tpsHeadroomSequenceLimit(state ProjectedState, snapshot TPSSnapshot) int64 {
	if state.RawRunning <= 0 || state.RawWaiting > 0 || state.PreemptionDelta > 0 ||
		state.GenerationDelta == 0 || !state.ObservationIntervalValid ||
		snapshot.Reference <= 0 || snapshot.QualifiedSequenceSeconds <= 0 {
		return 0
	}
	intervalSeconds := state.ObservationInterval.Seconds()
	requiredTokens := snapshot.Reference * snapshot.QualifiedSequenceSeconds
	surplusTokens := snapshot.QualifiedTokens - requiredTokens
	costPerSequence := snapshot.Reference * intervalSeconds
	if !finiteNonnegative(requiredTokens) || !finiteNonnegative(surplusTokens) ||
		!finiteNonnegative(costPerSequence) || surplusTokens <= 0 || costPerSequence <= 0 {
		return 0
	}
	slotBudget := surplusTokens / costPerSequence
	if slotBudget < 1 {
		return 0
	}
	slots := tpsHeadroomWaveLimit
	if slotBudget < float64(slots) {
		slots = int64(math.Floor(slotBudget))
	}
	limit, ok := addNonnegativeInt64(state.RawRunning, slots)
	if !ok {
		return 0
	}
	return limit
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
