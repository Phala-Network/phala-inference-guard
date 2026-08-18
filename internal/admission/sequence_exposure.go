package admission

import (
	"math"
	"time"
)

type sequenceExposureSnapshot struct {
	forwardedSequenceSeconds float64
	responseSequenceSeconds  float64
}

func (s sequenceExposureSnapshot) subtract(previous sequenceExposureSnapshot) (sequenceExposureSnapshot, bool) {
	if !s.valid() || !previous.valid() ||
		s.forwardedSequenceSeconds < previous.forwardedSequenceSeconds ||
		s.responseSequenceSeconds < previous.responseSequenceSeconds {
		return sequenceExposureSnapshot{}, false
	}
	result := sequenceExposureSnapshot{
		forwardedSequenceSeconds: s.forwardedSequenceSeconds - previous.forwardedSequenceSeconds,
		responseSequenceSeconds:  s.responseSequenceSeconds - previous.responseSequenceSeconds,
	}
	return result, result.valid()
}

func (s sequenceExposureSnapshot) valid() bool {
	return finiteNonnegative(s.forwardedSequenceSeconds) &&
		finiteNonnegative(s.responseSequenceSeconds) &&
		s.responseSequenceSeconds <= s.forwardedSequenceSeconds
}

// sequenceExposureLedger integrates local HTTP lifecycle evidence without a
// reservation scan. Forwarded exposure is an upper bound; response exposure
// only proves that some locally tracked sequences reached a response body.
type sequenceExposureLedger struct {
	initialized              bool
	lastEventAt              time.Time
	activeForwardedSequences int64
	activeResponseSequences  int64
	forwardedSequenceSeconds float64
	responseSequenceSeconds  float64
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
		forwardedSequenceSeconds: l.forwardedSequenceSeconds,
		responseSequenceSeconds:  l.responseSequenceSeconds,
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
	elapsed := now.Sub(l.lastEventAt).Seconds()
	if !finiteNonnegative(elapsed) {
		return false
	}
	forwarded := l.forwardedSequenceSeconds + elapsed*float64(l.activeForwardedSequences)
	response := l.responseSequenceSeconds + elapsed*float64(l.activeResponseSequences)
	if !finiteNonnegative(forwarded) || !finiteNonnegative(response) || response > forwarded {
		return false
	}
	l.forwardedSequenceSeconds = forwarded
	l.responseSequenceSeconds = response
	l.lastEventAt = now
	return l.valid()
}

func (l *sequenceExposureLedger) valid() bool {
	if l == nil || l.activeForwardedSequences < 0 || l.activeResponseSequences < 0 ||
		l.activeResponseSequences > l.activeForwardedSequences ||
		!finiteNonnegative(l.forwardedSequenceSeconds) ||
		!finiteNonnegative(l.responseSequenceSeconds) ||
		l.responseSequenceSeconds > l.forwardedSequenceSeconds {
		return false
	}
	if !l.initialized {
		return l.lastEventAt.IsZero() && l.activeForwardedSequences == 0 &&
			l.activeResponseSequences == 0 && l.forwardedSequenceSeconds == 0 &&
			l.responseSequenceSeconds == 0
	}
	return !l.lastEventAt.IsZero()
}
