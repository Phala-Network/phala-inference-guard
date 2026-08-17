package admission

import predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"

type reservationPhase uint8

const (
	reservationReserved reservationPhase = iota + 1
	reservationForwardedPrefill
	reservationActiveDecode
	reservationResidualDebt
)

type reservation struct {
	id                uint64
	runtimeEpoch      uint64
	work              predictive.RequestWork
	prefillClass      PrefillClass
	phase             reservationPhase
	inputCovered      bool
	sequenceCovered   bool
	admittedSequence  uint64
	forwardedSequence uint64
	firstByteSequence uint64
	terminalSequence  uint64
	terminalCause     TerminalCause
	cacheCreditTokens int64
	cacheCreditLease  uint64
}

func (r reservation) contribution() (reservationOverlay, bool) {
	if r.id == 0 || r.runtimeEpoch == 0 || r.work.TotalKVTokens <= 0 ||
		r.work.FutureKVTokens < 0 || r.work.Estimate.SelectionInputTokens <= 0 ||
		r.work.PrefillComputeTokens <= 0 || r.work.PrefillComputeTokens > r.work.Estimate.SelectionInputTokens ||
		!r.validCacheCredit() {
		return reservationOverlay{}, false
	}
	switch r.phase {
	case reservationReserved, reservationForwardedPrefill:
		contribution := reservationOverlay{
			kvTokens:                  r.work.TotalKVTokens,
			pendingPrefillInputTokens: r.work.Estimate.SelectionInputTokens,
			pendingPrefillTokens:      r.work.PrefillComputeTokens,
			pendingPrefillSequences:   1,
			liveReservations:          1,
		}
		if !r.sequenceCovered {
			contribution.unobservedSequences = 1
		}
		switch r.prefillClass {
		case PrefillRegular, PrefillWeighted:
		case PrefillExclusive:
			contribution.pendingExclusiveSequences = 1
		case PrefillQuiescent:
			contribution.pendingQuiescentSequences = 1
		default:
			return reservationOverlay{}, false
		}
		return contribution, true
	case reservationActiveDecode:
		kvTokens := r.work.TotalKVTokens
		if r.inputCovered {
			kvTokens = r.work.FutureKVTokens
		}
		contribution := reservationOverlay{
			kvTokens:          kvTokens,
			localActiveDecode: 1,
			liveReservations:  1,
		}
		if !r.sequenceCovered {
			contribution.unobservedSequences = 1
		}
		return contribution, true
	case reservationResidualDebt:
		contribution := reservationOverlay{
			kvTokens:      r.work.TotalKVTokens,
			residualDebts: 1,
		}
		if !r.sequenceCovered && r.terminalCause != TerminalSuccess {
			contribution.unobservedSequences = 1
		}
		return contribution, true
	default:
		return reservationOverlay{}, false
	}
}

func (r reservation) validCacheCredit() bool {
	expected := r.work.Estimate.SelectionInputTokens - r.work.PrefillComputeTokens
	if r.cacheCreditTokens != expected || r.cacheCreditTokens < 0 {
		return false
	}
	if r.cacheCreditTokens == 0 {
		return r.cacheCreditLease == 0
	}
	return r.cacheCreditLease > 0
}

func addOverlay(left, right reservationOverlay) (reservationOverlay, bool) {
	var result reservationOverlay
	var ok bool
	if result.kvTokens, ok = addNonnegativeInt64(left.kvTokens, right.kvTokens); !ok {
		return reservationOverlay{}, false
	}
	if result.pendingPrefillInputTokens, ok = addNonnegativeInt64(left.pendingPrefillInputTokens, right.pendingPrefillInputTokens); !ok {
		return reservationOverlay{}, false
	}
	if result.pendingPrefillTokens, ok = addNonnegativeInt64(left.pendingPrefillTokens, right.pendingPrefillTokens); !ok {
		return reservationOverlay{}, false
	}
	if result.pendingPrefillSequences, ok = addNonnegativeInt64(left.pendingPrefillSequences, right.pendingPrefillSequences); !ok {
		return reservationOverlay{}, false
	}
	if result.pendingExclusiveSequences, ok = addNonnegativeInt64(left.pendingExclusiveSequences, right.pendingExclusiveSequences); !ok {
		return reservationOverlay{}, false
	}
	if result.pendingQuiescentSequences, ok = addNonnegativeInt64(left.pendingQuiescentSequences, right.pendingQuiescentSequences); !ok {
		return reservationOverlay{}, false
	}
	if result.localActiveDecode, ok = addNonnegativeInt64(left.localActiveDecode, right.localActiveDecode); !ok {
		return reservationOverlay{}, false
	}
	if result.unobservedSequences, ok = addNonnegativeInt64(left.unobservedSequences, right.unobservedSequences); !ok {
		return reservationOverlay{}, false
	}
	if result.liveReservations, ok = addNonnegativeInt64(left.liveReservations, right.liveReservations); !ok {
		return reservationOverlay{}, false
	}
	if result.residualDebts, ok = addNonnegativeInt64(left.residualDebts, right.residualDebts); !ok {
		return reservationOverlay{}, false
	}
	return result, result.valid()
}

func subtractOverlay(left, right reservationOverlay) (reservationOverlay, bool) {
	if !left.valid() || !right.valid() || left.kvTokens < right.kvTokens ||
		left.pendingPrefillInputTokens < right.pendingPrefillInputTokens ||
		left.pendingPrefillTokens < right.pendingPrefillTokens ||
		left.pendingPrefillSequences < right.pendingPrefillSequences ||
		left.pendingExclusiveSequences < right.pendingExclusiveSequences ||
		left.pendingQuiescentSequences < right.pendingQuiescentSequences ||
		left.localActiveDecode < right.localActiveDecode ||
		left.unobservedSequences < right.unobservedSequences ||
		left.liveReservations < right.liveReservations ||
		left.residualDebts < right.residualDebts {
		return reservationOverlay{}, false
	}
	result := reservationOverlay{
		kvTokens:                  left.kvTokens - right.kvTokens,
		pendingPrefillInputTokens: left.pendingPrefillInputTokens - right.pendingPrefillInputTokens,
		pendingPrefillTokens:      left.pendingPrefillTokens - right.pendingPrefillTokens,
		pendingPrefillSequences:   left.pendingPrefillSequences - right.pendingPrefillSequences,
		pendingExclusiveSequences: left.pendingExclusiveSequences - right.pendingExclusiveSequences,
		pendingQuiescentSequences: left.pendingQuiescentSequences - right.pendingQuiescentSequences,
		localActiveDecode:         left.localActiveDecode - right.localActiveDecode,
		unobservedSequences:       left.unobservedSequences - right.unobservedSequences,
		liveReservations:          left.liveReservations - right.liveReservations,
		residualDebts:             left.residualDebts - right.residualDebts,
	}
	return result, result.valid()
}

func replaceOverlay(current, oldContribution, newContribution reservationOverlay) (reservationOverlay, bool) {
	withoutOld, ok := subtractOverlay(current, oldContribution)
	if !ok {
		return reservationOverlay{}, false
	}
	return addOverlay(withoutOld, newContribution)
}

func (o reservationOverlay) valid() bool {
	demandCapacity, ok := addNonnegativeInt64(o.liveReservations, o.residualDebts)
	if !ok {
		return false
	}
	return o.kvTokens >= 0 && o.pendingPrefillInputTokens >= o.pendingPrefillTokens &&
		o.pendingPrefillTokens >= 0 &&
		o.pendingPrefillSequences >= 0 && o.pendingExclusiveSequences >= 0 &&
		o.pendingQuiescentSequences >= 0 &&
		o.pendingExclusiveSequences <= o.pendingPrefillSequences &&
		o.pendingQuiescentSequences <= o.pendingPrefillSequences &&
		o.localActiveDecode >= 0 && o.unobservedSequences >= 0 &&
		o.unobservedSequences <= demandCapacity && o.liveReservations >= 0 && o.residualDebts >= 0
}
