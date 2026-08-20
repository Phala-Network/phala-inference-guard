package telemetry

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
}

func AggregateSamples(samples []Sample) Sample {
	if len(samples) == 0 {
		return Sample{}
	}
	if len(samples) == 1 {
		return samples[0]
	}
	aggregated := Sample{
		RunningValid:     true,
		WaitingValid:     true,
		PreemptionsValid: true,
		GenerationValid:  true,
		CacheTokensValid: true,
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
		if aggregated.CacheQueryTokens > ^uint64(0)-sample.CacheQueryTokens ||
			aggregated.CacheHitTokens > ^uint64(0)-sample.CacheHitTokens {
			aggregated.CacheTokensValid = false
		} else {
			aggregated.CacheQueryTokens += sample.CacheQueryTokens
			aggregated.CacheHitTokens += sample.CacheHitTokens
		}
		aggregated.CacheTokensValid = aggregated.CacheTokensValid && sample.CacheTokensValid
		if sample.GenerationTPSDirect {
			aggregated.GenerationTPS += sample.GenerationTPS
			aggregated.GenerationTPSDirect = true
		}
	}
	return aggregated
}
