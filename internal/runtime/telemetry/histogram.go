package telemetry

import (
	"math"
	"sort"
)

func HistogramDelta(current, previous HistogramSample) (HistogramSample, bool) {
	if current.Count == 0 || previous.Count == 0 || current.Count < previous.Count {
		return HistogramSample{}, false
	}
	countDelta := current.Count - previous.Count
	if countDelta == 0 || current.Sum < previous.Sum {
		return HistogramSample{}, false
	}
	previousBuckets := make(map[float64]uint64, len(previous.Buckets))
	for _, bucket := range previous.Buckets {
		previousBuckets[bucket.UpperBound] = bucket.Count
	}
	buckets := make([]HistogramBucketSample, 0, len(current.Buckets))
	for _, bucket := range current.Buckets {
		previousCount := previousBuckets[bucket.UpperBound]
		if bucket.Count < previousCount {
			return HistogramSample{}, false
		}
		buckets = append(buckets, HistogramBucketSample{
			UpperBound: bucket.UpperBound,
			Count:      bucket.Count - previousCount,
		})
	}
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].UpperBound < buckets[j].UpperBound
	})
	return HistogramSample{
		Count:   countDelta,
		Sum:     current.Sum - previous.Sum,
		Buckets: buckets,
	}, true
}

func HistogramAverage(sample HistogramSample) (float64, bool) {
	if sample.Count == 0 {
		return 0, false
	}
	return sample.Sum / float64(sample.Count), true
}

func HistogramQuantileUpperBound(sample HistogramSample, quantile float64) (float64, bool) {
	if sample.Count == 0 || len(sample.Buckets) == 0 {
		return 0, false
	}
	target := uint64(math.Ceil(float64(sample.Count) * quantile))
	if target < 1 {
		target = 1
	}
	lastFinite := 0.0
	for _, bucket := range sample.Buckets {
		if !math.IsInf(bucket.UpperBound, 1) {
			lastFinite = bucket.UpperBound
		}
		if bucket.Count >= target {
			if math.IsInf(bucket.UpperBound, 1) {
				if lastFinite > 0 {
					return lastFinite * 2, true
				}
				return 0, false
			}
			return bucket.UpperBound, true
		}
	}
	return 0, false
}
