package admission

import "math"

// qosBudgetForecast converts positive rolling per-sequence TPS surplus into a
// bounded marginal sequence wave. It never raises the strict rate-derived
// baseline by more than one sequence per coherent observation.
type qosBudgetForecast struct{}

func (qosBudgetForecast) sequenceLimit(state ProjectedState, currentSequences int64) int64 {
	snapshot := state.TPS
	baseLimit := rateDerivedBaseSequenceLimit(snapshot)
	if !snapshot.Enabled || !snapshot.Ready || snapshot.Reference <= 0 ||
		state.RawRunning <= 0 || state.RawWaiting > 0 || state.PreemptionDelta > 0 ||
		currentSequences < baseLimit || state.UnobservedSequences > 0 ||
		state.GenerationDelta == 0 || !state.ObservationIntervalValid ||
		snapshot.QualifiedActiveSeconds <= 0 || snapshot.QualifiedSequenceSeconds <= 0 {
		return baseLimit
	}

	rollingCost := snapshot.Reference * snapshot.QualifiedSequenceSeconds
	rollingSurplus := snapshot.QualifiedSequenceTokens - rollingCost
	if !finiteNonnegative(rollingCost) || !finiteNonnegative(rollingSurplus) || rollingSurplus <= 0 {
		return baseLimit
	}
	surplusRate := rollingSurplus / snapshot.QualifiedActiveSeconds
	intervalSeconds := state.ObservationInterval.Seconds()
	currentRate := float64(state.GenerationDelta) / intervalSeconds
	conservativeRate := math.Min(currentRate, snapshot.AggregateTPS)
	if !finiteNonnegative(surplusRate) || !finiteNonnegative(currentRate) ||
		!finiteNonnegative(conservativeRate) || conservativeRate <= 0 {
		return baseLimit
	}

	quotient := (conservativeRate + surplusRate) / snapshot.Reference
	if !finiteNonnegative(quotient) || quotient < 1 {
		return baseLimit
	}
	var budgetLimit int64
	if quotient >= float64(math.MaxInt64) {
		budgetLimit = math.MaxInt64
	} else {
		budgetLimit = int64(math.Floor(quotient))
	}
	waveLimit, ok := addNonnegativeInt64(baseLimit, 1)
	if !ok {
		waveLimit = math.MaxInt64
	}
	if budgetLimit > waveLimit {
		budgetLimit = waveLimit
	}
	if budgetLimit < baseLimit {
		return baseLimit
	}
	return budgetLimit
}
