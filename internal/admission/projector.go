package admission

type observedState struct {
	observation     BackendObservation
	sequence        uint64
	generationDelta uint64
	preemptionDelta uint64
}

type reservationOverlay struct {
	kvTokens                  int64
	pendingPrefillTokens      int64
	pendingPrefillSequences   int64
	pendingExclusiveSequences int64
	pendingQuiescentSequences int64
	localActiveDecode         int64
	liveReservations          int64
	residualDebts             int64
}

type stateProjector struct{}

func (stateProjector) project(observed observedState, overlay reservationOverlay) (ProjectedState, bool) {
	effectiveKV, ok := addNonnegativeInt64(observed.observation.UsedKVTokens, overlay.kvTokens)
	if !ok {
		return ProjectedState{}, false
	}
	state := ProjectedState{
		ObservedKVTokens:          observed.observation.UsedKVTokens,
		ReservationKVTokens:       overlay.kvTokens,
		EffectiveKVTokens:         effectiveKV,
		PendingPrefillTokens:      overlay.pendingPrefillTokens,
		PendingPrefillSequences:   overlay.pendingPrefillSequences,
		PendingExclusiveSequences: overlay.pendingExclusiveSequences,
		PendingQuiescentSequences: overlay.pendingQuiescentSequences,
		LocalActiveDecode:         overlay.localActiveDecode,
		LiveReservations:          overlay.liveReservations,
		ResidualDebts:             overlay.residualDebts,
		RawRunning:                observed.observation.Running,
		RawWaiting:                observed.observation.Waiting,
		GenerationDelta:           observed.generationDelta,
		PreemptionDelta:           observed.preemptionDelta,
	}
	if !validProjectedState(state) {
		return ProjectedState{}, false
	}
	return state, true
}

func validProjectedState(state ProjectedState) bool {
	return state.ObservedKVTokens >= 0 && state.ReservationKVTokens >= 0 &&
		state.EffectiveKVTokens >= state.ObservedKVTokens &&
		state.PendingPrefillTokens >= 0 && state.PendingPrefillSequences >= 0 &&
		state.PendingExclusiveSequences >= 0 && state.PendingQuiescentSequences >= 0 &&
		state.PendingExclusiveSequences <= state.PendingPrefillSequences &&
		state.PendingQuiescentSequences <= state.PendingPrefillSequences &&
		state.LocalActiveDecode >= 0 && state.LiveReservations >= 0 &&
		state.ResidualDebts >= 0 && state.RawRunning >= 0 && state.RawWaiting >= 0
}
