package admission

import "math"

var windowConcurrencyHistogramBounds = [...]int64{
	0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16,
	20, 24, 28, 32, 36, 40, 44, 48, 52, 56, 60, 63,
}

type WindowConcurrencyHistogramBucket struct {
	UpperBound      int64
	CumulativeCount uint64
}

type WindowConcurrencyHistogramSnapshot struct {
	Count   uint64
	Sum     uint64
	Buckets [len(windowConcurrencyHistogramBounds)]WindowConcurrencyHistogramBucket
}

type windowConcurrencyHistogram struct {
	count   uint64
	sum     uint64
	buckets [len(windowConcurrencyHistogramBounds)]uint64
}

func (h *windowConcurrencyHistogram) observe(value int64) {
	if h == nil || value < 0 {
		return
	}
	if h.count < math.MaxUint64 {
		h.count++
	}
	increment := uint64(value)
	if math.MaxUint64-h.sum >= increment {
		h.sum += increment
	} else {
		h.sum = math.MaxUint64
	}
	for index, upperBound := range windowConcurrencyHistogramBounds {
		if value <= upperBound && h.buckets[index] < math.MaxUint64 {
			h.buckets[index]++
		}
	}
}

func (h windowConcurrencyHistogram) snapshot() WindowConcurrencyHistogramSnapshot {
	snapshot := WindowConcurrencyHistogramSnapshot{Count: h.count, Sum: h.sum}
	for index, upperBound := range windowConcurrencyHistogramBounds {
		snapshot.Buckets[index] = WindowConcurrencyHistogramBucket{
			UpperBound:      upperBound,
			CumulativeCount: h.buckets[index],
		}
	}
	return snapshot
}
