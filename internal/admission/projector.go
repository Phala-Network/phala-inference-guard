package admission

import "time"

type observedState struct {
	observation       BackendObservation
	sequence          uint64
	generationDelta   uint64
	preemptionDelta   uint64
	interval          time.Duration
	previousRunning   int64
	localActiveDecode int64
	cache             cachePrefillObservation
}

type reservationOverlay struct {
	kvTokens                  int64
	pendingPrefillTokens      int64
	pendingPrefillSequences   int64
	pendingExclusiveSequences int64
	pendingQuiescentSequences int64
	localActiveDecode         int64
	unobservedSequences       int64
	liveReservations          int64
	residualDebts             int64
}

type stateProjector struct{}

func (stateProjector) project(observed observedState, overlay reservationOverlay) (ProjectedState, bool) {
	if !validCachePrefillObservation(observed.cache) {
		return ProjectedState{}, false
	}
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
		UnobservedSequences:       overlay.unobservedSequences,
		LiveReservations:          overlay.liveReservations,
		ResidualDebts:             overlay.residualDebts,
		RawRunning:                observed.observation.Running,
		RawWaiting:                observed.observation.Waiting,
		PreviousRawRunning:        observed.previousRunning,
		GenerationDelta:           observed.generationDelta,
		PreemptionDelta:           observed.preemptionDelta,
		ObservationInterval:       observed.interval,
		CacheObservationValid:     observed.cache.valid,
		CacheHitFraction:          observed.cache.hitFraction,
		CacheCreditFraction:       observed.cache.creditFraction,
		CacheEvidenceTokens:       observed.cache.evidenceTokens,
	}
	if !validProjectedState(state) {
		return ProjectedState{}, false
	}
	return state, true
}

func validProjectedState(state ProjectedState) bool {
	demandCapacity, ok := addNonnegativeInt64(state.LiveReservations, state.ResidualDebts)
	if !ok {
		return false
	}
	return state.ObservedKVTokens >= 0 && state.ReservationKVTokens >= 0 &&
		state.EffectiveKVTokens >= state.ObservedKVTokens &&
		state.PendingPrefillTokens >= 0 && state.PendingPrefillSequences >= 0 &&
		state.PendingExclusiveSequences >= 0 && state.PendingQuiescentSequences >= 0 &&
		state.PendingExclusiveSequences <= state.PendingPrefillSequences &&
		state.PendingQuiescentSequences <= state.PendingPrefillSequences &&
		state.LocalActiveDecode >= 0 && state.UnobservedSequences >= 0 &&
		state.UnobservedSequences <= demandCapacity && state.LiveReservations >= 0 &&
		state.ResidualDebts >= 0 && state.RawRunning >= 0 && state.RawWaiting >= 0 &&
		state.PreviousRawRunning >= 0 && state.ObservationInterval >= 0 &&
		finiteNonnegative(state.CacheHitFraction) && state.CacheHitFraction <= 1 &&
		finiteNonnegative(state.CacheCreditFraction) &&
		state.CacheCreditFraction <= cachePrefillMaximumHitCredit &&
		state.CacheCreditFraction <= state.CacheHitFraction &&
		(!state.CacheObservationValid || state.CacheEvidenceTokens >= cachePrefillMinimumEvidenceTokens) &&
		(state.CacheObservationValid || (state.CacheHitFraction == 0 && state.CacheCreditFraction == 0 && state.CacheEvidenceTokens == 0)) &&
		validTPSSnapshot(state.TPS)
}
