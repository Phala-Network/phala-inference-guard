package telemetry

import "testing"

func TestAggregateSamplesSingleSampleFastPathPreservesFields(t *testing.T) {
	sample := Sample{
		Running:               7,
		Waiting:               1,
		KVCacheUsage:          0.42,
		Preemptions:           3,
		PreemptionDelta:       2,
		PreemptionDeltaDirect: true,
		Generation:            100,
		GenerationTPS:         55.5,
		GenerationTPSDirect:   true,
		TTFT: HistogramSample{
			Count: 2,
			Sum:   1.5,
			Buckets: []HistogramBucketSample{
				{UpperBound: 1, Count: 1},
				{UpperBound: 2, Count: 2},
			},
		},
	}

	aggregated := AggregateSamples([]Sample{sample})

	if aggregated.Running != sample.Running ||
		aggregated.Waiting != sample.Waiting ||
		aggregated.KVCacheUsage != sample.KVCacheUsage ||
		aggregated.PreemptionDelta != sample.PreemptionDelta ||
		!aggregated.PreemptionDeltaDirect ||
		aggregated.GenerationTPS != sample.GenerationTPS ||
		!aggregated.GenerationTPSDirect ||
		aggregated.TTFT.Count != sample.TTFT.Count ||
		len(aggregated.TTFT.Buckets) != len(sample.TTFT.Buckets) {
		t.Fatalf("single-sample aggregate changed fields: %#v", aggregated)
	}
}

func TestAggregateSamplesSingleSampleKeepsHistogramBucketsSorted(t *testing.T) {
	aggregated := AggregateSamples([]Sample{{
		TTFT: HistogramSample{
			Count: 2,
			Sum:   1.5,
			Buckets: []HistogramBucketSample{
				{UpperBound: 2, Count: 2},
				{UpperBound: 1, Count: 1},
			},
		},
	}})

	if len(aggregated.TTFT.Buckets) != 2 {
		t.Fatalf("bucket count = %d, want 2", len(aggregated.TTFT.Buckets))
	}
	if aggregated.TTFT.Buckets[0].UpperBound != 1 || aggregated.TTFT.Buckets[1].UpperBound != 2 {
		t.Fatalf("buckets = %#v, want sorted by upper bound", aggregated.TTFT.Buckets)
	}
}

func TestAggregateHistogramsDoesNotAllocateBucketsWhenInputsHaveNone(t *testing.T) {
	aggregated := AggregateHistograms([]Sample{
		{TTFT: HistogramSample{Count: 2, Sum: 1.2}},
		{TTFT: HistogramSample{Count: 3, Sum: 2.1}},
	})

	if aggregated.Count != 5 || aggregated.Sum != 3.3 {
		t.Fatalf("histogram count/sum = %d/%f, want 5/3.3", aggregated.Count, aggregated.Sum)
	}
	if aggregated.Buckets != nil {
		t.Fatalf("buckets = %#v, want nil without input buckets", aggregated.Buckets)
	}
}
