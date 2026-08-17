package admission

import (
	"math"
	"time"
)

const (
	tpsWindowDuration                   = 60 * time.Second
	tpsWindowBucketCount                = 61
	tpsWindowMinimumQualifiedSamples    = 4
	tpsWindowMinimumQualifiedSeqSeconds = 8.0
)

type tpsSample struct {
	start                        time.Time
	end                          time.Time
	maximumInterval              time.Duration
	generatedTokens              uint64
	previousRunning              int64
	running                      int64
	previousLocalActiveDecode    int64
	localActiveDecode            int64
	forwardedSequenceLiabilities int64
}

type tpsBucket struct {
	second          int64
	valid           bool
	tokens          float64
	activeSeconds   float64
	samples         uint64
	sequenceTokens  float64
	sequenceSeconds float64
	sequenceSamples uint64
}

type tpsWindow struct {
	reference float64
	buckets   [tpsWindowBucketCount]tpsBucket
}

func newTPSWindow(reference float64) tpsWindow {
	return tpsWindow{reference: reference}
}

func (w *tpsWindow) enabled() bool {
	return w != nil && w.reference > 0
}

func (w *tpsWindow) reset() {
	if w == nil {
		return
	}
	clear(w.buckets[:])
}

// observe adds one coherent metrics interval. Invalid or unqualified intervals
// are deliberately ignored; false is reserved for an internal numeric bound
// failure that requires the Controller to fail closed.
func (w *tpsWindow) observe(sample tpsSample) bool {
	if w == nil {
		return false
	}
	if !w.enabled() {
		return true
	}
	w.expire(sample.end)
	if sample.start.IsZero() || sample.end.IsZero() || sample.maximumInterval <= 0 ||
		sample.previousRunning < 0 || sample.running < 0 ||
		sample.previousLocalActiveDecode < 0 || sample.localActiveDecode < 0 {
		return true
	}
	interval := sample.end.Sub(sample.start)
	if interval <= 0 || interval > sample.maximumInterval {
		return true
	}

	activeSequences := maximumInt64(
		sample.previousRunning,
		sample.running,
		sample.previousLocalActiveDecode,
		sample.localActiveDecode,
	)
	sequenceCountReliable := activeSequences > 0
	knownDecode := sample.previousLocalActiveDecode > 0 || sample.localActiveDecode > 0
	if sample.generatedTokens == 0 && !knownDecode {
		return true
	}
	if activeSequences <= 0 && sample.generatedTokens == 0 {
		return true
	}

	intervalSeconds := interval.Seconds()
	if !finiteNonnegative(intervalSeconds) || intervalSeconds == 0 {
		return false
	}
	cutoff := sample.end.Add(-tpsWindowDuration)
	cursor := sample.start
	if cursor.Before(cutoff) {
		cursor = cutoff
	}
	for cursor.Before(sample.end) {
		second := cursor.Unix()
		segmentEnd := time.Unix(second, 0).Add(time.Second)
		if !segmentEnd.After(cursor) {
			return false
		}
		if segmentEnd.After(sample.end) {
			segmentEnd = sample.end
		}
		segmentSeconds := segmentEnd.Sub(cursor).Seconds()
		fraction := segmentSeconds / intervalSeconds
		tokens := float64(sample.generatedTokens) * fraction
		isLast := segmentEnd.Equal(sample.end)
		samples := uint64(0)
		if isLast {
			samples = 1
		}
		sequenceTokens := float64(0)
		sequenceSeconds := float64(0)
		sequenceSamples := uint64(0)
		if sequenceCountReliable {
			sequenceTokens = tokens
			sequenceSeconds = segmentSeconds * float64(activeSequences)
			sequenceSamples = samples
		}
		if !w.add(
			second,
			tokens,
			segmentSeconds,
			samples,
			sequenceTokens,
			sequenceSeconds,
			sequenceSamples,
		) {
			return false
		}
		cursor = segmentEnd
	}
	return true
}

func (w *tpsWindow) snapshot(now time.Time) TPSSnapshot {
	if w == nil {
		return TPSSnapshot{}
	}
	snapshot := TPSSnapshot{Reference: w.reference, Enabled: w.enabled()}
	if !snapshot.Enabled {
		return snapshot
	}
	for _, bucket := range w.buckets {
		if !bucket.valid || !tpsBucketActiveAt(bucket, now) {
			continue
		}
		if math.MaxUint64-snapshot.QualifiedSamples < bucket.samples {
			return TPSSnapshot{Reference: w.reference, Enabled: true}
		}
		if math.MaxUint64-snapshot.QualifiedSequenceSamples < bucket.sequenceSamples {
			return TPSSnapshot{Reference: w.reference, Enabled: true}
		}
		snapshot.QualifiedSamples += bucket.samples
		snapshot.QualifiedTokens += bucket.tokens
		snapshot.QualifiedActiveSeconds += bucket.activeSeconds
		snapshot.QualifiedSequenceSamples += bucket.sequenceSamples
		snapshot.QualifiedSequenceTokens += bucket.sequenceTokens
		snapshot.QualifiedSequenceSeconds += bucket.sequenceSeconds
	}
	if !validTPSSnapshot(snapshot) {
		return TPSSnapshot{Reference: w.reference, Enabled: true}
	}
	if snapshot.QualifiedActiveSeconds > 0 {
		snapshot.AggregateTPS = snapshot.QualifiedTokens / snapshot.QualifiedActiveSeconds
	}
	if snapshot.QualifiedSequenceSeconds > 0 {
		snapshot.MeanActiveTPS = snapshot.QualifiedSequenceTokens / snapshot.QualifiedSequenceSeconds
	}
	if !finiteNonnegative(snapshot.AggregateTPS) || !finiteNonnegative(snapshot.MeanActiveTPS) {
		return TPSSnapshot{Reference: w.reference, Enabled: true}
	}
	snapshot.Ready = snapshot.QualifiedSequenceSamples >= tpsWindowMinimumQualifiedSamples &&
		snapshot.QualifiedSequenceSeconds >= tpsWindowMinimumQualifiedSeqSeconds
	return snapshot
}

func tpsBucketActiveAt(bucket tpsBucket, now time.Time) bool {
	if !bucket.valid || now.IsZero() {
		return bucket.valid
	}
	cutoff := now.Add(-tpsWindowDuration)
	return time.Unix(bucket.second, 0).Add(time.Second).After(cutoff)
}

func (w *tpsWindow) add(
	second int64,
	tokens float64,
	activeSeconds float64,
	samples uint64,
	sequenceTokens float64,
	sequenceSeconds float64,
	sequenceSamples uint64,
) bool {
	if !finiteNonnegative(tokens) || !finiteNonnegative(activeSeconds) ||
		!finiteNonnegative(sequenceTokens) || !finiteNonnegative(sequenceSeconds) {
		return false
	}
	index := second % tpsWindowBucketCount
	if index < 0 {
		index += tpsWindowBucketCount
	}
	bucket := &w.buckets[index]
	if bucket.valid && bucket.second != second {
		return false
	}
	if !bucket.valid {
		*bucket = tpsBucket{second: second, valid: true}
	}
	if math.MaxUint64-bucket.samples < samples {
		return false
	}
	if math.MaxUint64-bucket.sequenceSamples < sequenceSamples {
		return false
	}
	nextTokens := bucket.tokens + tokens
	nextActive := bucket.activeSeconds + activeSeconds
	nextSequenceTokens := bucket.sequenceTokens + sequenceTokens
	nextSequence := bucket.sequenceSeconds + sequenceSeconds
	if !finiteNonnegative(nextTokens) || !finiteNonnegative(nextActive) ||
		!finiteNonnegative(nextSequenceTokens) || !finiteNonnegative(nextSequence) {
		return false
	}
	bucket.tokens = nextTokens
	bucket.activeSeconds = nextActive
	bucket.samples += samples
	bucket.sequenceTokens = nextSequenceTokens
	bucket.sequenceSeconds = nextSequence
	bucket.sequenceSamples += sequenceSamples
	return true
}

func (w *tpsWindow) expire(now time.Time) {
	if w == nil || now.IsZero() {
		return
	}
	cutoff := now.Add(-tpsWindowDuration)
	for index := range w.buckets {
		bucket := &w.buckets[index]
		if bucket.valid && !time.Unix(bucket.second, 0).Add(time.Second).After(cutoff) {
			*bucket = tpsBucket{}
		}
	}
}

func validTPSSnapshot(snapshot TPSSnapshot) bool {
	return finiteNonnegative(snapshot.Reference) &&
		finiteNonnegative(snapshot.QualifiedTokens) &&
		finiteNonnegative(snapshot.QualifiedActiveSeconds) &&
		finiteNonnegative(snapshot.QualifiedSequenceTokens) &&
		finiteNonnegative(snapshot.QualifiedSequenceSeconds) &&
		finiteNonnegative(snapshot.AggregateTPS) && finiteNonnegative(snapshot.MeanActiveTPS)
}

func finiteNonnegative(value float64) bool {
	return value >= 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func maximumInt64(values ...int64) int64 {
	var maximum int64
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
}
