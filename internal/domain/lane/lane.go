package lane

import (
	"sync/atomic"
	"time"
)

type Buckets struct {
	DurationSeconds []float64
	BodyBytes       []int64
}

type Snapshot struct {
	Name            string
	Limit           int
	Accepted        uint64
	Rejected        uint64
	Completed       uint64
	Inflight        int64
	DurationNs      uint64
	DurationBuckets []uint64
	BodyCount       uint64
	BodyBytes       uint64
	BodyBuckets     []uint64
	StatusClasses   [6]uint64
}

type Lane struct {
	name            string
	limit           int
	tokens          chan struct{}
	durationBounds  []float64
	bodyBounds      []int64
	accepted        atomic.Uint64
	rejected        atomic.Uint64
	completed       atomic.Uint64
	inflight        atomic.Int64
	durationNs      atomic.Uint64
	durationBuckets []atomic.Uint64
	bodyCount       atomic.Uint64
	bodyBytes       atomic.Uint64
	bodyBuckets     []atomic.Uint64
	statusClasses   [6]atomic.Uint64
}

func New(name string, limit int, buckets Buckets) *Lane {
	lane := &Lane{
		name:            name,
		limit:           limit,
		durationBounds:  append([]float64(nil), buckets.DurationSeconds...),
		bodyBounds:      append([]int64(nil), buckets.BodyBytes...),
		durationBuckets: make([]atomic.Uint64, len(buckets.DurationSeconds)),
		bodyBuckets:     make([]atomic.Uint64, len(buckets.BodyBytes)),
	}
	if limit > 0 {
		lane.tokens = make(chan struct{}, limit)
	}
	return lane
}

func (l *Lane) Name() string {
	if l == nil {
		return ""
	}
	return l.name
}

func (l *Lane) Limit() int {
	if l == nil {
		return 0
	}
	return l.limit
}

func (l *Lane) Inflight() int64 {
	if l == nil {
		return 0
	}
	return l.inflight.Load()
}

func (l *Lane) TryAcquire() bool {
	if l == nil || l.limit <= 0 {
		return false
	}
	select {
	case l.tokens <- struct{}{}:
		l.inflight.Add(1)
		return true
	default:
		return false
	}
}

func (l *Lane) AcquireUnbounded() {
	if l != nil {
		l.inflight.Add(1)
	}
}

func (l *Lane) ReleaseUnbounded() {
	if l != nil {
		l.inflight.Add(-1)
	}
}

func (l *Lane) Release() {
	if l == nil || l.tokens == nil {
		return
	}
	select {
	case <-l.tokens:
		l.inflight.Add(-1)
	default:
	}
}

func (l *Lane) ObserveAccepted() {
	if l != nil {
		l.accepted.Add(1)
	}
}

func (l *Lane) ObserveRejected() {
	if l != nil {
		l.rejected.Add(1)
	}
}

func (l *Lane) ObserveBody(contentLength int64) {
	if l == nil {
		return
	}
	l.bodyCount.Add(1)
	if contentLength < 0 {
		return
	}
	l.bodyBytes.Add(uint64(contentLength))
	for index, upper := range l.bodyBounds {
		if contentLength <= upper {
			l.bodyBuckets[index].Add(1)
		}
	}
}

func (l *Lane) ObserveComplete(status int, elapsed time.Duration) {
	if l == nil {
		return
	}
	l.completed.Add(1)
	l.durationNs.Add(uint64(elapsed.Nanoseconds()))
	for index, upper := range l.durationBounds {
		if elapsed.Seconds() <= upper {
			l.durationBuckets[index].Add(1)
		}
	}
	class := status / 100
	if class < 1 || class > 5 {
		class = 0
	}
	l.statusClasses[class].Add(1)
}

func (l *Lane) Snapshot() Snapshot {
	if l == nil {
		return Snapshot{}
	}
	snapshot := Snapshot{
		Name:            l.name,
		Limit:           l.limit,
		Accepted:        l.accepted.Load(),
		Rejected:        l.rejected.Load(),
		Completed:       l.completed.Load(),
		Inflight:        l.inflight.Load(),
		DurationNs:      l.durationNs.Load(),
		DurationBuckets: make([]uint64, len(l.durationBuckets)),
		BodyCount:       l.bodyCount.Load(),
		BodyBytes:       l.bodyBytes.Load(),
		BodyBuckets:     make([]uint64, len(l.bodyBuckets)),
	}
	for index := range l.durationBuckets {
		snapshot.DurationBuckets[index] = l.durationBuckets[index].Load()
	}
	for index := range l.bodyBuckets {
		snapshot.BodyBuckets[index] = l.bodyBuckets[index].Load()
	}
	for index := range l.statusClasses {
		snapshot.StatusClasses[index] = l.statusClasses[index].Load()
	}
	return snapshot
}
