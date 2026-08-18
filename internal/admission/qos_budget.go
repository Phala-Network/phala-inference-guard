package admission

import "math"

// qosBudgetForecast spends rolling per-sequence TPS surplus only when a
// request-bounded Decode lifetime fits the remaining budget.
type qosBudgetForecast struct{}

func (qosBudgetForecast) sequenceLimit(
	state ProjectedState,
	currentSequences int64,
	nonBudgetLimit int64,
	demand tpsAdmissionDemand,
) (int64, bool) {
	snapshot := state.TPS
	baseLimit := rateDerivedBaseSequenceLimit(snapshot)
	if !snapshot.Enabled || !snapshot.Ready || snapshot.Reference <= 0 ||
		state.RawRunning <= 0 || state.RawWaiting > 0 || state.PreemptionDelta > 0 ||
		currentSequences < baseLimit || state.UnobservedSequences > 0 || state.QoSBudgetLeases > 0 ||
		state.GenerationDelta == 0 || !state.ObservationIntervalValid ||
		snapshot.QualifiedSequenceSeconds <= 0 || demand.additionalSequences != 1 ||
		!demand.outputLimitKnown || demand.outputLimitTokens < 0 {
		return nonBudgetLimit, false
	}
	postAdmit, ok := addNonnegativeInt64(currentSequences, demand.additionalSequences)
	if !ok || postAdmit <= nonBudgetLimit {
		return nonBudgetLimit, false
	}
	waveLimit, ok := addNonnegativeInt64(baseLimit, 1)
	if !ok {
		waveLimit = math.MaxInt64
	}
	if postAdmit > waveLimit {
		return nonBudgetLimit, false
	}

	rollingCost := snapshot.Reference * snapshot.QualifiedSequenceSeconds
	rollingSurplus := snapshot.QualifiedSequenceTokens - rollingCost
	if !finiteNonnegative(rollingCost) || !finiteNonnegative(rollingSurplus) || rollingSurplus <= 0 {
		return nonBudgetLimit, false
	}
	intervalSeconds := state.ObservationInterval.Seconds()
	currentRate := float64(state.GenerationDelta) / intervalSeconds
	conservativeRate := math.Min(currentRate, snapshot.AggregateTPS)
	if !finiteNonnegative(currentRate) ||
		!finiteNonnegative(conservativeRate) || conservativeRate <= 0 {
		return nonBudgetLimit, false
	}
	projectedPerSequence := conservativeRate / float64(postAdmit)
	if !finiteNonnegative(projectedPerSequence) || projectedPerSequence <= 0 {
		return nonBudgetLimit, false
	}
	deficitRate := snapshot.Reference*float64(postAdmit) - conservativeRate
	if deficitRate < 0 {
		deficitRate = 0
	}
	forecastSeconds := float64(demand.outputLimitTokens) / projectedPerSequence
	requiredSurplus := deficitRate * forecastSeconds
	if !finiteNonnegative(deficitRate) || !finiteNonnegative(forecastSeconds) ||
		!finiteNonnegative(requiredSurplus) || requiredSurplus > rollingSurplus {
		return nonBudgetLimit, false
	}
	return waveLimit, true
}
