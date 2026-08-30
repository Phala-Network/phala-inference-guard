package admission

import "time"

type observedState struct {
	observation     BackendObservation
	sequence        uint64
	generationDelta uint64
	preemptionDelta uint64
	interval        time.Duration
	previousRunning int64
	previousWaiting int64
}

type reservationOverlay struct {
	unobservedSequences int64
	sequenceLiabilities int64
	liveReservations    int64
	residualDebts       int64
}

type stateProjector struct{}

func (stateProjector) project(observed observedState, overlay reservationOverlay) (ProjectedState, bool) {
	state := ProjectedState{
		UnobservedSequences:      overlay.unobservedSequences,
		SequenceLiabilities:      overlay.sequenceLiabilities,
		LiveReservations:         overlay.liveReservations,
		ResidualDebts:            overlay.residualDebts,
		RawRunning:               observed.observation.Running,
		RawWaiting:               observed.observation.Waiting,
		PreviousRawRunning:       observed.previousRunning,
		PreviousRawWaiting:       observed.previousWaiting,
		GenerationDelta:          observed.generationDelta,
		PreemptionDelta:          observed.preemptionDelta,
		ObservationInterval:      observed.interval,
		ObservationIntervalValid: observed.interval > 0 && observed.interval <= observed.observation.MaximumAge,
	}
	if !validProjectedState(state) {
		return ProjectedState{}, false
	}
	return state, true
}

func validProjectedState(state ProjectedState) bool {
	return state.UnobservedSequences >= 0 &&
		state.SequenceLiabilities >= 0 && state.UnobservedSequences <= state.SequenceLiabilities &&
		state.LiveReservations >= 0 && state.ResidualDebts >= 0 &&
		state.RawRunning >= 0 && state.RawWaiting >= 0 && state.PreviousRawRunning >= 0 &&
		state.PreviousRawWaiting >= 0 &&
		state.ObservationInterval >= 0 &&
		(!state.ObservationIntervalValid || state.ObservationInterval > 0) &&
		validTPSSnapshot(state.TPS)
}
