package telemetry

import "sort"

type HistogramBucketSample struct {
	UpperBound float64
	Count      uint64
}

type HistogramSample struct {
	Count   uint64
	Sum     float64
	Buckets []HistogramBucketSample
}

type Sample struct {
	Running               int
	Waiting               int
	KVCacheUsage          float64
	Preemptions           uint64
	PreemptionDelta       uint64
	PreemptionDeltaDirect bool
	Generation            uint64
	GenerationTPS         float64
	GenerationTPSDirect   bool
	TTFT                  HistogramSample
}

func AggregateSamples(samples []Sample) Sample {
	if len(samples) == 0 {
		return Sample{}
	}
	if len(samples) == 1 {
		aggregated := samples[0]
		aggregated.TTFT = AggregateHistograms(samples)
		return aggregated
	}
	aggregated := Sample{}
	for _, sample := range samples {
		aggregated.Running += sample.Running
		aggregated.Waiting += sample.Waiting
		if sample.KVCacheUsage > aggregated.KVCacheUsage {
			aggregated.KVCacheUsage = sample.KVCacheUsage
		}
		aggregated.Preemptions += sample.Preemptions
		if sample.PreemptionDeltaDirect {
			aggregated.PreemptionDelta += sample.PreemptionDelta
			aggregated.PreemptionDeltaDirect = true
		}
		aggregated.Generation += sample.Generation
		if sample.GenerationTPSDirect {
			aggregated.GenerationTPS += sample.GenerationTPS
			aggregated.GenerationTPSDirect = true
		}
	}
	aggregated.TTFT = AggregateHistograms(samples)
	return aggregated
}

func AggregateHistograms(samples []Sample) HistogramSample {
	if len(samples) == 0 {
		return HistogramSample{}
	}
	if len(samples) == 1 {
		histogram := samples[0].TTFT
		if histogramBucketsSorted(histogram.Buckets) {
			return histogram
		}
	}
	aggregated := HistogramSample{}
	bucketCounts := map[float64]uint64{}
	for _, sample := range samples {
		aggregated.Count += sample.TTFT.Count
		aggregated.Sum += sample.TTFT.Sum
		for _, bucket := range sample.TTFT.Buckets {
			bucketCounts[bucket.UpperBound] += bucket.Count
		}
	}
	if len(bucketCounts) == 0 {
		return aggregated
	}
	aggregated.Buckets = make([]HistogramBucketSample, 0, len(bucketCounts))
	for upperBound, count := range bucketCounts {
		aggregated.Buckets = append(aggregated.Buckets, HistogramBucketSample{UpperBound: upperBound, Count: count})
	}
	sort.Slice(aggregated.Buckets, func(i, j int) bool {
		return aggregated.Buckets[i].UpperBound < aggregated.Buckets[j].UpperBound
	})
	return aggregated
}

func histogramBucketsSorted(buckets []HistogramBucketSample) bool {
	for i := 1; i < len(buckets); i++ {
		if buckets[i].UpperBound < buckets[i-1].UpperBound {
			return false
		}
	}
	return true
}
