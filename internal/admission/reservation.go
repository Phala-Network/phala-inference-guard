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
	qosBudgeted       bool
}

func (r reservation) contribution() (reservationOverlay, bool) {
	if r.id == 0 || r.runtimeEpoch == 0 || r.work.Validate() != nil ||
		!r.validCacheCredit() {
		return reservationOverlay{}, false
	}
	switch r.phase {
	case reservationReserved, reservationForwardedPrefill:
		decodeSequences := r.work.Estimate.DecodeSequences
		contribution := reservationOverlay{
			kvTokens:                  r.work.TotalKVTokens,
			pendingPrefillInputTokens: r.work.PrefillInputTokens,
			pendingPrefillTokens:      r.work.PrefillComputeTokens,
			pendingPrefillSequences:   decodeSequences,
			sequenceLiabilities:       decodeSequences,
			qosBudgetLeases:           r.qosBudgetLease(),
			liveReservations:          1,
		}
		if !r.sequenceCovered {
			contribution.unobservedSequences = decodeSequences
		}
		if !applyPendingPrefillClass(&contribution, r.prefillClass) {
			return reservationOverlay{}, false
		}
		return contribution, true
	case reservationActiveDecode:
		decodeSequences := r.work.Estimate.DecodeSequences
		kvTokens := r.work.TotalKVTokens
		if r.inputCovered {
			var ok bool
			kvTokens, ok = addNonnegativeInt64(
				r.work.FirstBytePendingInputKVTokens,
				r.work.FutureKVTokens,
			)
			if !ok {
				return reservationOverlay{}, false
			}
		}
		activeDecode := decodeSequences - r.work.FirstBytePendingPrefillSequences
		contribution := reservationOverlay{
			kvTokens:                  kvTokens,
			pendingPrefillInputTokens: r.work.FirstBytePendingPrefillInputTokens,
			pendingPrefillTokens:      r.work.FirstBytePendingPrefillComputeTokens,
			pendingPrefillSequences:   r.work.FirstBytePendingPrefillSequences,
			localActiveDecode:         activeDecode,
			sequenceLiabilities:       decodeSequences,
			qosBudgetLeases:           r.qosBudgetLease(),
			liveReservations:          1,
		}
		if !applyPendingPrefillClass(&contribution, r.prefillClass) {
			return reservationOverlay{}, false
		}
		if !r.sequenceCovered {
			contribution.unobservedSequences = decodeSequences
		}
		return contribution, true
	case reservationResidualDebt:
		decodeSequences := r.work.Estimate.DecodeSequences
		kvTokens := r.work.TotalKVTokens
		if r.inputCovered {
			var ok bool
			kvTokens, ok = addNonnegativeInt64(
				r.work.FirstBytePendingInputKVTokens,
				r.work.FutureKVTokens,
			)
			if !ok {
				return reservationOverlay{}, false
			}
		}
		contribution := reservationOverlay{
			kvTokens:            kvTokens,
			sequenceLiabilities: decodeSequences,
			qosBudgetLeases:     r.qosBudgetLease(),
			residualDebts:       1,
		}
		if r.terminalCause != TerminalSuccess {
			if r.firstByteSequence > 0 {
				contribution.pendingPrefillInputTokens = r.work.FirstBytePendingPrefillInputTokens
				contribution.pendingPrefillTokens = r.work.FirstBytePendingPrefillComputeTokens
				contribution.pendingPrefillSequences = r.work.FirstBytePendingPrefillSequences
				contribution.localActiveDecode = decodeSequences - r.work.FirstBytePendingPrefillSequences
			} else {
				contribution.pendingPrefillInputTokens = r.work.PrefillInputTokens
				contribution.pendingPrefillTokens = r.work.PrefillComputeTokens
				contribution.pendingPrefillSequences = decodeSequences
			}
			if !applyPendingPrefillClass(&contribution, r.prefillClass) {
				return reservationOverlay{}, false
			}
		}
		if !r.sequenceCovered && r.terminalCause != TerminalSuccess {
			contribution.unobservedSequences = decodeSequences
		}
		return contribution, true
	default:
		return reservationOverlay{}, false
	}
}

func (r reservation) qosBudgetLease() int64 {
	if r.qosBudgeted {
		return 1
	}
	return 0
}

func applyPendingPrefillClass(contribution *reservationOverlay, class PrefillClass) bool {
	if contribution == nil || contribution.pendingPrefillSequences < 0 {
		return false
	}
	if contribution.pendingPrefillSequences == 0 {
		return true
	}
	switch class {
	case PrefillRegular, PrefillWeighted:
	case PrefillExclusive:
		contribution.pendingExclusiveSequences = 1
	case PrefillQuiescent:
		contribution.pendingQuiescentSequences = 1
	default:
		return false
	}
	return true
}

func (r reservation) validCacheCredit() bool {
	expected := r.work.PrefillInputTokens - r.work.PrefillComputeTokens
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
	if result.sequenceLiabilities, ok = addNonnegativeInt64(left.sequenceLiabilities, right.sequenceLiabilities); !ok {
		return reservationOverlay{}, false
	}
	if result.qosBudgetLeases, ok = addNonnegativeInt64(left.qosBudgetLeases, right.qosBudgetLeases); !ok {
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
		left.sequenceLiabilities < right.sequenceLiabilities ||
		left.qosBudgetLeases < right.qosBudgetLeases ||
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
		sequenceLiabilities:       left.sequenceLiabilities - right.sequenceLiabilities,
		qosBudgetLeases:           left.qosBudgetLeases - right.qosBudgetLeases,
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
	leaseCapacity, leaseCapacityValid := addNonnegativeInt64(o.liveReservations, o.residualDebts)
	return o.kvTokens >= 0 && o.pendingPrefillInputTokens >= o.pendingPrefillTokens &&
		o.pendingPrefillTokens >= 0 &&
		o.pendingPrefillSequences >= 0 && o.pendingExclusiveSequences >= 0 &&
		o.pendingQuiescentSequences >= 0 &&
		o.pendingExclusiveSequences <= o.pendingPrefillSequences &&
		o.pendingQuiescentSequences <= o.pendingPrefillSequences &&
		o.localActiveDecode >= 0 && o.unobservedSequences >= 0 &&
		o.sequenceLiabilities >= 0 && o.unobservedSequences <= o.sequenceLiabilities &&
		leaseCapacityValid && o.qosBudgetLeases >= 0 && o.qosBudgetLeases <= leaseCapacity &&
		o.liveReservations >= 0 && o.residualDebts >= 0
}
