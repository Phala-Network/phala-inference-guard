package admission

import "math"

// qosBudgetSequenceLimit permits one marginal sequence when observed rolling
// surplus can cover its projected deficit until the next metrics observation.
// It deliberately ignores declared request lifetime: the next observation and
// the lease lifecycle are the feedback boundary.
func qosBudgetSequenceLimit(
	state ProjectedState,
	currentSequences int64,
	nonBudgetLimit int64,
	demand tpsAdmissionDemand,
) (int64, bool, TPSDecisionSubreason) {
	snapshot := state.TPS
	baseLimit := rateDerivedBaseSequenceLimit(snapshot)
	if !snapshot.Enabled || !snapshot.Ready || snapshot.Reference <= 0 ||
		state.RawRunning <= 0 || state.RawWaiting > 0 || state.PreemptionDelta > 0 ||
		currentSequences < baseLimit || state.GenerationDelta == 0 ||
		!state.ObservationIntervalValid || snapshot.QualifiedSequenceSeconds <= 0 {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetIneligible
	}
	if state.UnobservedSequences > 0 {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetUnobserved
	}
	if state.QoSBudgetLeases > 0 {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetActiveLease
	}
	if demand.additionalSequences != 1 {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetMultiSequence
	}
	postAdmit, ok := addNonnegativeInt64(currentSequences, demand.additionalSequences)
	if !ok {
		return nonBudgetLimit, false, TPSDecisionSubreasonInvalidState
	}
	if postAdmit <= nonBudgetLimit {
		return nonBudgetLimit, false, TPSDecisionSubreasonBaseRate
	}
	waveLimit, ok := addNonnegativeInt64(baseLimit, 1)
	if !ok {
		waveLimit = math.MaxInt64
	}
	if postAdmit > waveLimit {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetWaveLimit
	}

	intervalSeconds := state.ObservationInterval.Seconds()
	currentRate := float64(state.GenerationDelta) / intervalSeconds
	currentMeanActiveTPS := currentRate / float64(state.RawRunning)
	if !finiteNonnegative(intervalSeconds) || intervalSeconds <= 0 ||
		!finiteNonnegative(currentRate) || !finiteNonnegative(currentMeanActiveTPS) ||
		currentMeanActiveTPS < snapshot.Reference {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetCurrentRate
	}

	rollingCost := snapshot.Reference * snapshot.QualifiedSequenceSeconds
	rollingSurplus := snapshot.QualifiedSequenceTokens - rollingCost
	if !finiteNonnegative(rollingCost) || !finiteNonnegative(rollingSurplus) || rollingSurplus <= 0 {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetNoSurplus
	}
	conservativeRate := math.Min(currentRate, snapshot.AggregateTPS)
	deficitRate := snapshot.Reference*float64(postAdmit) - conservativeRate
	if deficitRate < 0 {
		deficitRate = 0
	}
	requiredUntilNextObservation := deficitRate * intervalSeconds
	if !finiteNonnegative(conservativeRate) || conservativeRate <= 0 ||
		!finiteNonnegative(deficitRate) ||
		!finiteNonnegative(requiredUntilNextObservation) ||
		requiredUntilNextObservation > rollingSurplus {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetNoSurplus
	}
	return waveLimit, true, TPSDecisionSubreasonQoSBudgetGranted
}
