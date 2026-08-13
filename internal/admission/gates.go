package admission

import (
	"math"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type gateDecision struct {
	fits   bool
	reason Reason
}

type contextGate struct {
	maximumInputTokens int64
	maxModelLenTokens  int64
}

func (g contextGate) evaluate(work predictive.RequestWork) gateDecision {
	if work.Estimate.SelectionInputTokens <= 0 ||
		work.Estimate.SelectionInputTokens > g.maximumInputTokens ||
		work.Estimate.DecodeHorizonTokens > g.maxModelLenTokens-work.Estimate.SelectionInputTokens {
		return gateDecision{reason: ReasonInputLimit}
	}
	return gateDecision{fits: true, reason: ReasonOpen}
}

type kvGate struct {
	hardLimitTokens int64
}

func (g kvGate) evaluate(state ProjectedState, work predictive.RequestWork) (gateDecision, int64) {
	postAdmit, ok := addNonnegativeInt64(state.EffectiveKVTokens, work.TotalKVTokens)
	if !ok {
		return gateDecision{reason: ReasonResourceExhausted}, 0
	}
	if postAdmit > g.hardLimitTokens {
		return gateDecision{reason: ReasonKVCapacity}, postAdmit
	}
	return gateDecision{fits: true, reason: ReasonOpen}, postAdmit
}

type prefillGate struct {
	regularTokens         int64
	exclusiveTokens       int64
	quiescentTokens       int64
	contendedBudgetTokens int64
	aggregateBudgetTokens int64
}

func (g prefillGate) classify(selectionTokens int64) PrefillClass {
	switch {
	case selectionTokens < g.regularTokens:
		return PrefillRegular
	case selectionTokens < g.exclusiveTokens:
		return PrefillWeighted
	case selectionTokens < g.quiescentTokens:
		return PrefillExclusive
	default:
		return PrefillQuiescent
	}
}

func (g prefillGate) evaluate(state ProjectedState, work predictive.RequestWork) (gateDecision, PrefillClass, int64) {
	class := g.classify(work.Estimate.SelectionInputTokens)
	postPending, ok := addNonnegativeInt64(
		state.PendingPrefillTokens,
		work.Estimate.SelectionInputTokens,
	)
	if !ok {
		return gateDecision{reason: ReasonResourceExhausted}, class, 0
	}

	if state.PendingQuiescentSequences > 0 {
		return gateDecision{reason: ReasonPrefillQuiescent}, class, postPending
	}
	if state.PendingExclusiveSequences > 0 {
		return gateDecision{reason: ReasonPrefillExclusive}, class, postPending
	}
	contended := state.LocalActiveDecode > 0 || state.RawRunning > 0 ||
		state.RawWaiting > 0 || state.PreemptionDelta > 0
	if contended {
		if class != PrefillRegular {
			return gateDecision{reason: ReasonPrefillContention}, class, postPending
		}
		if postPending > g.contendedBudgetTokens {
			return gateDecision{reason: ReasonPrefillBudget}, class, postPending
		}
		return gateDecision{fits: true, reason: ReasonOpen}, class, postPending
	}

	switch class {
	case PrefillRegular, PrefillWeighted:
		if postPending > g.aggregateBudgetTokens {
			return gateDecision{reason: ReasonPrefillBudget}, class, postPending
		}
	case PrefillExclusive:
		if state.PendingPrefillSequences > 0 {
			return gateDecision{reason: ReasonPrefillExclusive}, class, postPending
		}
	case PrefillQuiescent:
		if state.PendingPrefillSequences > 0 || state.LocalActiveDecode > 0 ||
			state.RawRunning > 0 || state.RawWaiting > 0 {
			return gateDecision{reason: ReasonPrefillQuiescent}, class, postPending
		}
	default:
		return gateDecision{reason: ReasonInvalidRequest}, class, postPending
	}
	return gateDecision{fits: true, reason: ReasonOpen}, class, postPending
}

func addNonnegativeInt64(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}
