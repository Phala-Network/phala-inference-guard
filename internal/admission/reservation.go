package admission

import "time"

type reservationPhase uint8

const (
	reservationReserved reservationPhase = iota + 1
	reservationForwarded
	reservationActiveDecode
	reservationResidualDebt
)

type reservation struct {
	id                       uint64
	runtimeEpoch             uint64
	demand                   TPSRequestDemand
	phase                    reservationPhase
	pendingFirstByteReleased bool
	forwardedAt              time.Time
	admittedSequence         uint64
	forwardedSequence        uint64
	terminalSequence         uint64
}

func (r reservation) contribution() (reservationOverlay, bool) {
	if r.id == 0 || r.runtimeEpoch == 0 || !r.demand.valid() {
		return reservationOverlay{}, false
	}
	contribution := reservationOverlay{sequenceLiabilities: r.demand.DecodeSequences}
	switch r.phase {
	case reservationReserved, reservationForwarded:
		if !r.pendingFirstByteReleased {
			contribution.unobservedSequences = r.demand.DecodeSequences
		}
		contribution.liveReservations = 1
	case reservationActiveDecode:
		if !r.pendingFirstByteReleased {
			return reservationOverlay{}, false
		}
		contribution.liveReservations = 1
	case reservationResidualDebt:
		contribution.residualDebts = 1
	default:
		return reservationOverlay{}, false
	}
	return contribution, contribution.valid()
}

func (r reservation) effectiveDemand() (TPSRequestDemand, bool) {
	return r.demand, r.demand.valid()
}

func addOverlay(left, right reservationOverlay) (reservationOverlay, bool) {
	var result reservationOverlay
	var ok bool
	if result.unobservedSequences, ok = addNonnegativeInt64(left.unobservedSequences, right.unobservedSequences); !ok {
		return reservationOverlay{}, false
	}
	if result.sequenceLiabilities, ok = addNonnegativeInt64(left.sequenceLiabilities, right.sequenceLiabilities); !ok {
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
	if !left.valid() || !right.valid() ||
		left.unobservedSequences < right.unobservedSequences ||
		left.sequenceLiabilities < right.sequenceLiabilities ||
		left.liveReservations < right.liveReservations ||
		left.residualDebts < right.residualDebts {
		return reservationOverlay{}, false
	}
	result := reservationOverlay{
		unobservedSequences: left.unobservedSequences - right.unobservedSequences,
		sequenceLiabilities: left.sequenceLiabilities - right.sequenceLiabilities,
		liveReservations:    left.liveReservations - right.liveReservations,
		residualDebts:       left.residualDebts - right.residualDebts,
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
	return o.unobservedSequences >= 0 && o.sequenceLiabilities >= 0 &&
		o.unobservedSequences <= o.sequenceLiabilities &&
		o.liveReservations >= 0 && o.residualDebts >= 0
}
