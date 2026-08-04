package server

import (
	"math"
	"sort"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

var predictiveUserTPSBuckets = []float64{5, 10, 20, 40, 80, 160, 320, 640}
var predictiveTPOTBucketsSeconds = []float64{0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1}

// predictiveQoSHistogram stores non-cumulative fixed buckets under the
// adapter lock. Request completion is O(log buckets), while metrics scrapes
// materialize the cumulative Prometheus view outside the hot request path.
type predictiveQoSHistogram struct {
	count uint64
	sum   float64
	bins  [11]uint64
}

func (h *predictiveQoSHistogram) Observe(value float64, upperBounds []float64) {
	if h == nil || value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) || len(upperBounds) >= len(h.bins) {
		return
	}
	index := sort.SearchFloat64s(upperBounds, value)
	h.count++
	h.sum += value
	h.bins[index]++
}

func (h predictiveQoSHistogram) Snapshot(upperBounds []float64) telemetry.HistogramSample {
	buckets := make([]telemetry.HistogramBucketSample, 0, len(upperBounds))
	cumulative := uint64(0)
	for index, upper := range upperBounds {
		cumulative += h.bins[index]
		buckets = append(buckets, telemetry.HistogramBucketSample{UpperBound: upper, Count: cumulative})
	}
	return telemetry.HistogramSample{Count: h.count, Sum: h.sum, Buckets: buckets}
}

func observePredictiveQualifiedQoS(tpsHistogram, tpotHistogram *predictiveQoSHistogram, tps float64, tpot time.Duration) {
	if tpot <= 0 {
		return
	}
	tpsHistogram.Observe(tps, predictiveUserTPSBuckets)
	tpotHistogram.Observe(tpot.Seconds(), predictiveTPOTBucketsSeconds)
}
