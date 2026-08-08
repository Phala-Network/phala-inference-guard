package requestaware

import "math"

func (r *scenarioRunner) decideV0122(
	request requestSpec,
	effectiveKV int64,
	hardFit bool,
	metricsFresh bool,
	preemptionCooldown bool,
) (bool, bool) {
	if !metricsFresh || preemptionCooldown || !hardFit {
		return false, true
	}
	pendingSequences, pendingTokens, pendingLong, pendingQuiescent := r.pendingPrefillSummary()
	estimatedPrefill := request.estimatedPrefill
	if estimatedPrefill <= 0 {
		estimatedPrefill = request.selectionInput
	}
	class := r.prefillClass(estimatedPrefill)
	postAdmitPrefill := pendingTokens + estimatedPrefill
	if pendingQuiescent > 0 {
		return false, false
	}
	switch class {
	case prefillRegular:
		if pendingLong == 0 && postAdmitPrefill > r.profile.PrefillAggregateBudgetTokens {
			return false, false
		}
	case prefillWeighted:
		if postAdmitPrefill > r.profile.PrefillAggregateBudgetTokens {
			return false, false
		}
	case prefillExclusive:
		if pendingLong > 0 {
			return false, false
		}
	case prefillQuiescent:
		if r.observed.running > 0 || r.observed.waiting > 0 ||
			r.observed.running+r.unabsorbedSequenceCount() > 0 || pendingSequences > 0 {
			return false, false
		}
	default:
		return false, true
	}

	soft := r.profile.KVCapacityTokens * 7 / 10
	soft -= soft % r.profile.KVBlockSize
	hard := r.profile.KVHardLimitTokens
	selectiveWindow := hard - soft
	if selectiveWindow <= 0 {
		return false, true
	}
	kvPressure := simulationClampUnit(float64(effectiveKV-soft) / float64(selectiveWindow))
	waitingPressure := float64(r.observed.waiting) /
		(float64(r.observed.running+r.observed.waiting) + 1)
	effectiveSequences := r.observed.running + r.unabsorbedSequenceCount()
	tpsPressure := 0.0
	if r.observed.tpsValid && effectiveSequences > 0 {
		projectedTPS := r.observed.tps
		if r.observed.tps < simulationTPSTarget || r.observed.waiting > 0 || effectiveSequences > r.observed.running {
			projectedTPS = r.observed.aggregateTPS / float64(effectiveSequences+1)
		}
		if projectedTPS <= 0 || math.IsNaN(projectedTPS) || math.IsInf(projectedTPS, 0) {
			return false, true
		}
		if projectedTPS < simulationTPSTarget {
			tpsPressure = (simulationTPSTarget - projectedTPS) / (simulationTPSTarget - simulationTPSFloor)
		}
	}
	pressure := math.Max(kvPressure, math.Max(waitingPressure, tpsPressure))
	pressure = simulationClampUnit(pressure)
	if pressure == 0 {
		return true, false
	}
	allowance := int64(math.Floor(float64(selectiveWindow) * (1 - pressure)))
	remaining := hard - effectiveKV
	if allowance < 0 {
		allowance = 0
	}
	if allowance > remaining {
		allowance = remaining
	}
	return request.selectionInput <= allowance, false
}

type simulationPrefillClass uint8

const (
	prefillInvalid simulationPrefillClass = iota
	prefillRegular
	prefillWeighted
	prefillExclusive
	prefillQuiescent
)

func (r *scenarioRunner) prefillClass(tokens int64) simulationPrefillClass {
	switch {
	case tokens <= 0:
		return prefillInvalid
	case tokens < r.profile.PrefillRegularTokens:
		return prefillRegular
	case tokens < r.profile.PrefillExclusiveTokens:
		return prefillWeighted
	case tokens < r.profile.PrefillQuiescentTokens:
		return prefillExclusive
	default:
		return prefillQuiescent
	}
}

func simulationClampUnit(value float64) float64 {
	if value <= 0 {
		return 0
	}
	if value >= 1 {
		return 1
	}
	return value
}
