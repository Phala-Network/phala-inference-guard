package backend

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type Runtime struct {
	Name                 string
	BackendKind          string
	KVCapacityTokens     int64
	KVUsedTokens         int64
	KVAvailableTokens    int64
	KVEvictableTokens    int64
	KVTokenMetricsValid  bool
	Running              int
	Waiting              int
	KVCacheUsage         float64
	Preemptions          uint64
	PreemptionDelta      uint64
	PreemptionDeltaValid bool
	Generation           uint64
	GenerationTPS        float64
	GenerationTPSValid   bool
	Updated              time.Time
	Failed               bool
	Error                string
}

func FromSample(name string, sample telemetry.Sample, previous Runtime, now time.Time) Runtime {
	generationTPS, generationTPSValid := observeGenerationTPS(sample, previous, now)
	preemptionDelta, preemptionDeltaValid := observePreemptionDelta(sample, previous)
	return Runtime{
		Name:                 name,
		BackendKind:          sample.BackendKind,
		KVCapacityTokens:     sample.KVCapacityTokens,
		KVUsedTokens:         sample.KVUsedTokens,
		KVAvailableTokens:    sample.KVAvailableTokens,
		KVEvictableTokens:    sample.KVEvictableTokens,
		KVTokenMetricsValid:  sample.KVTokenMetricsValid,
		Running:              sample.Running,
		Waiting:              sample.Waiting,
		KVCacheUsage:         sample.KVCacheUsage,
		Preemptions:          sample.Preemptions,
		PreemptionDelta:      preemptionDelta,
		PreemptionDeltaValid: preemptionDeltaValid,
		Generation:           sample.Generation,
		GenerationTPS:        generationTPS,
		GenerationTPSValid:   generationTPSValid,
		Updated:              now,
	}
}

func NormalizeSample(sample telemetry.Sample, status Runtime) telemetry.Sample {
	normalized := sample
	if status.GenerationTPSValid {
		normalized.GenerationTPS = status.GenerationTPS
		normalized.GenerationTPSDirect = true
	} else {
		normalized.GenerationTPS = 0
		normalized.GenerationTPSDirect = false
	}
	if status.PreemptionDeltaValid {
		normalized.PreemptionDelta = status.PreemptionDelta
		normalized.PreemptionDeltaDirect = true
	} else {
		normalized.PreemptionDelta = 0
		normalized.PreemptionDeltaDirect = false
	}
	return normalized
}

func observeGenerationTPS(sample telemetry.Sample, previous Runtime, now time.Time) (float64, bool) {
	if sample.GenerationTPSDirect {
		return sample.GenerationTPS, true
	}
	if previous.Failed || previous.Generation > sample.Generation || previous.Updated.IsZero() {
		return 0, false
	}
	elapsed := now.Sub(previous.Updated).Seconds()
	if elapsed <= 0 {
		return 0, false
	}
	return float64(sample.Generation-previous.Generation) / elapsed, true
}

func observePreemptionDelta(sample telemetry.Sample, previous Runtime) (uint64, bool) {
	if sample.PreemptionDeltaDirect {
		return sample.PreemptionDelta, true
	}
	if previous.Failed || previous.Updated.IsZero() || sample.Preemptions < previous.Preemptions {
		return 0, false
	}
	return sample.Preemptions - previous.Preemptions, true
}
