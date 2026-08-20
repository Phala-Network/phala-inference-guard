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
	}

	aggregated := AggregateSamples([]Sample{sample})

	if aggregated.Running != sample.Running ||
		aggregated.Waiting != sample.Waiting ||
		aggregated.KVCacheUsage != sample.KVCacheUsage ||
		aggregated.PreemptionDelta != sample.PreemptionDelta ||
		!aggregated.PreemptionDeltaDirect ||
		aggregated.GenerationTPS != sample.GenerationTPS ||
		!aggregated.GenerationTPSDirect {
		t.Fatalf("single-sample aggregate changed fields: %#v", aggregated)
	}
}
