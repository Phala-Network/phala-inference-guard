package admission

import (
	"math"
	"math/bits"
	"time"
)

type sequenceNanoseconds struct {
	high uint64
	low  uint64
}

func (n sequenceNanoseconds) addDuration(duration time.Duration, sequences int64) (sequenceNanoseconds, bool) {
	if duration < 0 || sequences < 0 {
		return sequenceNanoseconds{}, false
	}
	high, low := bits.Mul64(uint64(duration), uint64(sequences))
	low, carry := bits.Add64(n.low, low, 0)
	high, overflow := bits.Add64(n.high, high, carry)
	if overflow != 0 {
		return sequenceNanoseconds{}, false
	}
	return sequenceNanoseconds{high: high, low: low}, true
}

func (n sequenceNanoseconds) subtract(previous sequenceNanoseconds) (sequenceNanoseconds, bool) {
	low, borrow := bits.Sub64(n.low, previous.low, 0)
	high, underflow := bits.Sub64(n.high, previous.high, borrow)
	if underflow != 0 {
		return sequenceNanoseconds{}, false
	}
	return sequenceNanoseconds{high: high, low: low}, true
}

func (n sequenceNanoseconds) compare(other sequenceNanoseconds) int {
	if n.high < other.high || (n.high == other.high && n.low < other.low) {
		return -1
	}
	if n == other {
		return 0
	}
	return 1
}

func (n sequenceNanoseconds) seconds() float64 {
	nanoseconds := math.Ldexp(float64(n.high), 64) + float64(n.low)
	return nanoseconds / float64(time.Second)
}

type sequenceExposureSnapshot struct {
	forwardedSequenceNanoseconds sequenceNanoseconds
	responseSequenceNanoseconds  sequenceNanoseconds
}

func (s sequenceExposureSnapshot) subtract(previous sequenceExposureSnapshot) (sequenceExposureSnapshot, bool) {
	if !s.valid() || !previous.valid() {
		return sequenceExposureSnapshot{}, false
	}
	forwarded, forwardedOK := s.forwardedSequenceNanoseconds.subtract(previous.forwardedSequenceNanoseconds)
	response, responseOK := s.responseSequenceNanoseconds.subtract(previous.responseSequenceNanoseconds)
	if !forwardedOK || !responseOK {
		return sequenceExposureSnapshot{}, false
	}
	result := sequenceExposureSnapshot{
		forwardedSequenceNanoseconds: forwarded,
		responseSequenceNanoseconds:  response,
	}
	return result, result.valid()
}

func (s sequenceExposureSnapshot) valid() bool {
	return s.responseSequenceNanoseconds.compare(s.forwardedSequenceNanoseconds) <= 0
}

func (s sequenceExposureSnapshot) seconds() (forwarded, response float64, valid bool) {
	if !s.valid() {
		return 0, 0, false
	}
	forwarded = s.forwardedSequenceNanoseconds.seconds()
	response = s.responseSequenceNanoseconds.seconds()
	return forwarded, response, finiteNonnegative(forwarded) &&
		finiteNonnegative(response) && response <= forwarded
}

// sequenceExposureLedger integrates local HTTP lifecycle evidence without a
// reservation scan. Forwarded exposure is an upper bound; response exposure
// only proves that some locally tracked sequences reached a response body.
type sequenceExposureLedger struct {
	initialized              bool
	lastEventAt              time.Time
	activeForwardedSequences int64
	activeResponseSequences  int64
	forwardedSequenceTime    sequenceNanoseconds
	responseSequenceTime     sequenceNanoseconds
}

func (l *sequenceExposureLedger) addForwarded(now time.Time, sequences int64) bool {
	if l == nil || sequences <= 0 || !l.advance(now) ||
		l.activeForwardedSequences > math.MaxInt64-sequences {
		return false
	}
	l.activeForwardedSequences += sequences
	return l.valid()
}

func (l *sequenceExposureLedger) addResponse(now time.Time, sequences int64) bool {
	if l == nil || sequences <= 0 || !l.advance(now) ||
		l.activeResponseSequences > math.MaxInt64-sequences ||
		l.activeResponseSequences+sequences > l.activeForwardedSequences {
		return false
	}
	l.activeResponseSequences += sequences
	return l.valid()
}

func (l *sequenceExposureLedger) remove(now time.Time, forwarded, response int64) bool {
	if l == nil || forwarded <= 0 || response < 0 || response > forwarded ||
		!l.advance(now) || l.activeForwardedSequences < forwarded ||
		l.activeResponseSequences < response {
		return false
	}
	l.activeForwardedSequences -= forwarded
	l.activeResponseSequences -= response
	return l.valid()
}

func (l *sequenceExposureLedger) snapshot(now time.Time) (sequenceExposureSnapshot, bool) {
	if l == nil || !l.advance(now) {
		return sequenceExposureSnapshot{}, false
	}
	snapshot := sequenceExposureSnapshot{
		forwardedSequenceNanoseconds: l.forwardedSequenceTime,
		responseSequenceNanoseconds:  l.responseSequenceTime,
	}
	return snapshot, snapshot.valid()
}

func (l *sequenceExposureLedger) reset() {
	if l == nil {
		return
	}
	*l = sequenceExposureLedger{}
}

func (l *sequenceExposureLedger) advance(now time.Time) bool {
	if l == nil || now.IsZero() {
		return false
	}
	if !l.initialized {
		l.initialized = true
		l.lastEventAt = now
		return l.valid()
	}
	if now.Before(l.lastEventAt) {
		return false
	}
	elapsed := now.Sub(l.lastEventAt)
	if elapsed < 0 {
		return false
	}
	forwarded, forwardedOK := l.forwardedSequenceTime.addDuration(elapsed, l.activeForwardedSequences)
	response, responseOK := l.responseSequenceTime.addDuration(elapsed, l.activeResponseSequences)
	if !forwardedOK || !responseOK || response.compare(forwarded) > 0 {
		return false
	}
	l.forwardedSequenceTime = forwarded
	l.responseSequenceTime = response
	l.lastEventAt = now
	return l.valid()
}

func (l *sequenceExposureLedger) valid() bool {
	if l == nil || l.activeForwardedSequences < 0 || l.activeResponseSequences < 0 ||
		l.activeResponseSequences > l.activeForwardedSequences ||
		l.responseSequenceTime.compare(l.forwardedSequenceTime) > 0 {
		return false
	}
	if !l.initialized {
		return l.lastEventAt.IsZero() && l.activeForwardedSequences == 0 &&
			l.activeResponseSequences == 0 && l.forwardedSequenceTime == (sequenceNanoseconds{}) &&
			l.responseSequenceTime == (sequenceNanoseconds{})
	}
	return !l.lastEventAt.IsZero()
}
