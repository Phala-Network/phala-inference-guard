package backend

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type Runtime struct {
	Name                 string
	Running              int
	Waiting              int
	KVCacheUsage         float64
	Preemptions          uint64
	PreemptionDelta      uint64
	PreemptionDeltaValid bool
	Generation           uint64
	GenerationTPS        float64
	GenerationTPSValid   bool
	TTFTCumulative       telemetry.HistogramSample
	TTFTAvg              float64
	TTFTP95              float64
	TTFTP99              float64
	TTFTValid            bool
	Updated              time.Time
	Failed               bool
	Error                string
}

func FromSample(name string, sample telemetry.Sample, previous Runtime, now time.Time) Runtime {
	generationTPS, generationTPSValid := observeGenerationTPS(sample, previous, now)
	preemptionDelta, preemptionDeltaValid := observePreemptionDelta(sample, previous)
	ttftAvg, ttftP95, ttftP99, ttftValid := observeTTFT(sample.TTFT, previous.TTFTCumulative)
	return Runtime{
		Name:                 name,
		Running:              sample.Running,
		Waiting:              sample.Waiting,
		KVCacheUsage:         sample.KVCacheUsage,
		Preemptions:          sample.Preemptions,
		PreemptionDelta:      preemptionDelta,
		PreemptionDeltaValid: preemptionDeltaValid,
		Generation:           sample.Generation,
		GenerationTPS:        generationTPS,
		GenerationTPSValid:   generationTPSValid,
		TTFTCumulative:       sample.TTFT,
		TTFTAvg:              ttftAvg,
		TTFTP95:              ttftP95,
		TTFTP99:              ttftP99,
		TTFTValid:            ttftValid,
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

func observeTTFT(current, previous telemetry.HistogramSample) (avg, p95, p99 float64, valid bool) {
	delta, ok := telemetry.HistogramDelta(current, previous)
	if !ok {
		return 0, 0, 0, false
	}
	avg, avgOK := telemetry.HistogramAverage(delta)
	if !avgOK {
		return 0, 0, 0, false
	}
	p95 = avg
	p99 = avg
	if value, ok := telemetry.HistogramQuantileUpperBound(delta, 0.95); ok {
		p95 = value
	}
	if value, ok := telemetry.HistogramQuantileUpperBound(delta, 0.99); ok {
		p99 = value
	} else {
		p99 = p95
	}
	return avg, p95, p99, true
}
