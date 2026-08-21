package admission

import (
	"math"
	"time"
)

// qosBudgetForecast spends rolling per-sequence TPS surplus only when the
// selected forecast duration fits the remaining budget. Its zero value keeps
// the production complete-declared-lifetime policy.
type qosBudgetForecast struct {
	controlHorizon time.Duration
}

func (f qosBudgetForecast) sequenceLimit(
	state ProjectedState,
	currentSequences int64,
	nonBudgetLimit int64,
	demand tpsAdmissionDemand,
) (int64, bool, TPSDecisionSubreason) {
	snapshot := state.TPS
	baseLimit := rateDerivedBaseSequenceLimit(snapshot)
	if !snapshot.Enabled || !snapshot.Ready || snapshot.Reference <= 0 ||
		state.RawRunning <= 0 || state.RawWaiting > 0 || state.PreemptionDelta > 0 ||
		currentSequences < baseLimit ||
		state.GenerationDelta == 0 || !state.ObservationIntervalValid ||
		snapshot.QualifiedSequenceSeconds <= 0 {
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
	if (!demand.outputLimitKnown && f.controlHorizon == 0) ||
		(demand.outputLimitKnown && demand.outputLimitTokens < 0) {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetOutputUnknown
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

	rollingCost := snapshot.Reference * snapshot.QualifiedSequenceSeconds
	rollingSurplus := snapshot.QualifiedSequenceTokens - rollingCost
	if !finiteNonnegative(rollingCost) || !finiteNonnegative(rollingSurplus) || rollingSurplus <= 0 {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetNoSurplus
	}
	intervalSeconds := state.ObservationInterval.Seconds()
	currentRate := float64(state.GenerationDelta) / intervalSeconds
	conservativeRate := math.Min(currentRate, snapshot.AggregateTPS)
	if !finiteNonnegative(currentRate) ||
		!finiteNonnegative(conservativeRate) || conservativeRate <= 0 {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetInvalidRate
	}
	projectedPerSequence := conservativeRate / float64(postAdmit)
	if !finiteNonnegative(projectedPerSequence) || projectedPerSequence <= 0 {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetInvalidRate
	}
	deficitRate := snapshot.Reference*float64(postAdmit) - conservativeRate
	if deficitRate < 0 {
		deficitRate = 0
	}
	forecastSeconds, forecastSubreason, forecastValid := f.forecastSeconds(
		demand,
		projectedPerSequence,
	)
	if !forecastValid {
		return nonBudgetLimit, false, forecastSubreason
	}
	requiredSurplus := deficitRate * forecastSeconds
	if !finiteNonnegative(deficitRate) || !finiteNonnegative(forecastSeconds) ||
		!finiteNonnegative(requiredSurplus) {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetInvalidRate
	}
	if requiredSurplus > rollingSurplus {
		return nonBudgetLimit, false, TPSDecisionSubreasonQoSBudgetLifetime
	}
	return waveLimit, true, TPSDecisionSubreasonQoSBudgetGranted
}

func (f qosBudgetForecast) forecastSeconds(
	demand tpsAdmissionDemand,
	projectedPerSequence float64,
) (float64, TPSDecisionSubreason, bool) {
	if !finiteNonnegative(projectedPerSequence) || projectedPerSequence <= 0 ||
		f.controlHorizon < 0 {
		return 0, TPSDecisionSubreasonQoSBudgetInvalidRate, false
	}
	if f.controlHorizon == 0 {
		if !demand.outputLimitKnown || demand.outputLimitTokens < 0 {
			return 0, TPSDecisionSubreasonQoSBudgetOutputUnknown, false
		}
		seconds := float64(demand.outputLimitTokens) / projectedPerSequence
		if !finiteNonnegative(seconds) {
			return 0, TPSDecisionSubreasonQoSBudgetInvalidRate, false
		}
		return seconds, TPSDecisionSubreasonQoSBudgetGranted, true
	}

	horizonSeconds := f.controlHorizon.Seconds()
	if !finiteNonnegative(horizonSeconds) || horizonSeconds <= 0 {
		return 0, TPSDecisionSubreasonQoSBudgetInvalidRate, false
	}
	if !demand.outputLimitKnown {
		return horizonSeconds, TPSDecisionSubreasonQoSBudgetGranted, true
	}
	if demand.outputLimitTokens < 0 {
		return 0, TPSDecisionSubreasonQoSBudgetOutputUnknown, false
	}
	declaredSeconds := float64(demand.outputLimitTokens) / projectedPerSequence
	if !finiteNonnegative(declaredSeconds) {
		return 0, TPSDecisionSubreasonQoSBudgetInvalidRate, false
	}
	return math.Min(declaredSeconds, horizonSeconds), TPSDecisionSubreasonQoSBudgetGranted, true
}
