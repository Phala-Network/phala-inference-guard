package histogram

import (
	"fmt"
	"io"
	"math"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

var DurationBucketsSeconds = []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1800}

var predictiveDurationBucketsSeconds = []float64{
	0.00001, 0.000025, 0.00005, 0.0001, 0.00025, 0.0005,
	0.001, 0.002, 0.005, 0.01, 0.025, 0.05, 0.1,
}

type DurationHistogram struct {
	count       atomic.Uint64
	totalNs     atomic.Uint64
	upperBounds []float64
	buckets     []atomic.Uint64
}

func NewDurationHistogram() DurationHistogram {
	return newStaticDurationHistogram(DurationBucketsSeconds)
}

func NewPredictiveDurationHistogram() DurationHistogram {
	return newStaticDurationHistogram(predictiveDurationBucketsSeconds)
}

func NewDurationHistogramWithBuckets(upperBounds []float64) (*DurationHistogram, error) {
	owned, err := validatedDurationHistogramBounds(upperBounds)
	if err != nil {
		return nil, err
	}
	return &DurationHistogram{
		upperBounds: owned,
		buckets:     make([]atomic.Uint64, len(owned)),
	}, nil
}

func validatedDurationHistogramBounds(upperBounds []float64) ([]float64, error) {
	if len(upperBounds) == 0 {
		return nil, fmt.Errorf("duration histogram requires at least one bucket")
	}
	owned := make([]float64, len(upperBounds))
	for index, upper := range upperBounds {
		if upper <= 0 || math.IsNaN(upper) || math.IsInf(upper, 0) {
			return nil, fmt.Errorf("duration histogram bucket %d must be finite and positive", index)
		}
		if index > 0 && upper <= owned[index-1] {
			return nil, fmt.Errorf("duration histogram buckets must be strictly increasing")
		}
		owned[index] = upper
	}
	return owned, nil
}

func newStaticDurationHistogram(upperBounds []float64) DurationHistogram {
	owned, err := validatedDurationHistogramBounds(upperBounds)
	if err != nil {
		panic(err)
	}
	return DurationHistogram{
		upperBounds: owned,
		buckets:     make([]atomic.Uint64, len(owned)),
	}
}

func (h *DurationHistogram) Observe(elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	h.count.Add(1)
	h.totalNs.Add(uint64(elapsed.Nanoseconds()))
	elapsedSeconds := elapsed.Seconds()
	for index, upper := range h.upperBounds {
		if elapsedSeconds <= upper {
			h.buckets[index].Add(1)
		}
	}
}

func (h *DurationHistogram) Sample() telemetry.HistogramSample {
	if h == nil {
		return telemetry.HistogramSample{}
	}
	buckets := make([]telemetry.HistogramBucketSample, 0, len(h.upperBounds))
	for index, upper := range h.upperBounds {
		buckets = append(buckets, telemetry.HistogramBucketSample{
			UpperBound: upper,
			Count:      h.buckets[index].Load(),
		})
	}
	return telemetry.HistogramSample{
		Count:   h.count.Load(),
		Sum:     float64(h.totalNs.Load()) / float64(time.Second),
		Buckets: buckets,
	}
}

func WriteDurationHistogram(w io.Writer, name string, h *DurationHistogram) {
	count := h.count.Load()
	fmt.Fprintf(w, "%s_count %d\n", name, count)
	fmt.Fprintf(w, "%s_sum %.6f\n", name, float64(h.totalNs.Load())/float64(time.Second))
	for index, upper := range h.upperBounds {
		fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, strconv.FormatFloat(upper, 'f', -1, 64), h.buckets[index].Load())
	}
	fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, "+Inf", count)
}
