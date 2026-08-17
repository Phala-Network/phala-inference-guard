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
	BackendKind           string
	ModelName             string
	ModelNameValid        bool
	KVCapacityTokens      int64
	KVBlockSize           int
	KVBlockSizeValid      bool
	KVUsedTokens          int64
	KVAvailableTokens     int64
	KVEvictableTokens     int64
	KVTokenMetricsValid   bool
	Running               int
	RunningValid          bool
	Waiting               int
	WaitingValid          bool
	KVCacheUsage          float64
	Preemptions           uint64
	PreemptionsValid      bool
	PreemptionDelta       uint64
	PreemptionDeltaDirect bool
	Generation            uint64
	GenerationValid       bool
	GenerationTPS         float64
	GenerationTPSDirect   bool
	CacheQueryTokens      uint64
	CacheHitTokens        uint64
	CacheTokensValid      bool
	RuntimeStartTime      float64
	RuntimeStartTimeValid bool
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
	aggregated := Sample{
		RunningValid:     true,
		WaitingValid:     true,
		PreemptionsValid: true,
		GenerationValid:  true,
	}
	for _, sample := range samples {
		aggregated.Running += sample.Running
		aggregated.RunningValid = aggregated.RunningValid && sample.RunningValid
		aggregated.Waiting += sample.Waiting
		aggregated.WaitingValid = aggregated.WaitingValid && sample.WaitingValid
		if sample.KVCacheUsage > aggregated.KVCacheUsage {
			aggregated.KVCacheUsage = sample.KVCacheUsage
		}
		aggregated.Preemptions += sample.Preemptions
		aggregated.PreemptionsValid = aggregated.PreemptionsValid && sample.PreemptionsValid
		if sample.PreemptionDeltaDirect {
			aggregated.PreemptionDelta += sample.PreemptionDelta
			aggregated.PreemptionDeltaDirect = true
		}
		aggregated.Generation += sample.Generation
		aggregated.GenerationValid = aggregated.GenerationValid && sample.GenerationValid
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
