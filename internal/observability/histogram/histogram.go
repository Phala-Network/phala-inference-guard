package histogram

import (
	"fmt"
	"io"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

var DurationBucketsSeconds = []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300, 600, 1800}

type DurationHistogram struct {
	count   atomic.Uint64
	totalNs atomic.Uint64
	buckets []atomic.Uint64
}

func NewDurationHistogram() DurationHistogram {
	return DurationHistogram{
		buckets: make([]atomic.Uint64, len(DurationBucketsSeconds)),
	}
}

func (h *DurationHistogram) Observe(elapsed time.Duration) {
	if elapsed < 0 {
		elapsed = 0
	}
	h.count.Add(1)
	h.totalNs.Add(uint64(elapsed.Nanoseconds()))
	for index, upper := range DurationBucketsSeconds {
		if elapsed.Seconds() <= upper {
			h.buckets[index].Add(1)
		}
	}
}

func (h *DurationHistogram) Sample() telemetry.HistogramSample {
	if h == nil {
		return telemetry.HistogramSample{}
	}
	buckets := make([]telemetry.HistogramBucketSample, 0, len(DurationBucketsSeconds))
	for index, upper := range DurationBucketsSeconds {
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
	for index, upper := range DurationBucketsSeconds {
		fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, strconv.FormatFloat(upper, 'f', -1, 64), h.buckets[index].Load())
	}
	fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, "+Inf", count)
}
