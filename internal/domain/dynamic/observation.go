package dynamic

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/latency"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/tier"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func observeGenerationWindow(now time.Time, current telemetry.Sample, previous dynamic.Snapshot) generationObservation {
	observation := generationObservation{}
	if current.PreemptionDeltaDirect {
		observation.PreemptionDelta = current.PreemptionDelta
	} else if previous.Source == "metrics" && current.Preemptions >= previous.Preemptions {
		observation.PreemptionDelta = current.Preemptions - previous.Preemptions
	}
	if current.GenerationTPSDirect {
		observation.GenerationTPS = current.GenerationTPS
		observation.GenerationTPSValid = true
		return observation
	}
	if previous.Source != "metrics" {
		return observation
	}
	if current.Generation >= previous.Generation && !previous.Updated.IsZero() {
		elapsed := now.Sub(previous.Updated).Seconds()
		if elapsed > 0 {
			observation.GenerationTPS = float64(current.Generation-previous.Generation) / elapsed
			observation.GenerationTPSValid = true
		}
	}
	return observation
}

func observeDynamicTTFTWindow(backend, semantic telemetry.HistogramSample, previous dynamic.Snapshot) (latency.Observation, telemetry.HistogramSample, string) {
	useSemantic := semantic.Count > 0 || previous.TTFTSource == "semantic" || previous.SemanticTTFTCumulative.Count > 0
	if useSemantic {
		preserveSmoothing := previous.Source == "metrics" && previous.TTFTSource == "semantic" && previous.SemanticTTFTCumulative.Count >= latency.MinWindowCount
		observation := observeTTFTWindowFromCumulative(semantic, previous.SemanticTTFTCumulative, previous, preserveSmoothing, true)
		if semantic.Count < latency.MinWindowCount {
			observation.SmoothedAvg = 0
			observation.SmoothedP95 = 0
			observation.SmoothedP99 = 0
		}
		return observation, semantic, "semantic"
	}
	preserveSmoothing := previous.Source == "metrics" && (previous.TTFTSource == "" || previous.TTFTSource == "backend")
	return observeTTFTWindowFromCumulative(backend, previous.TTFTCumulative, previous, preserveSmoothing, false), semantic, "backend"
}

func observeTTFTWindowFromCumulative(current, previousCumulative telemetry.HistogramSample, previous dynamic.Snapshot, preserveSmoothing, allowZeroPrevious bool) latency.Observation {
	return latency.ObserveWindow(current, latency.WindowState{
		Source:      previous.Source,
		Cumulative:  previousCumulative,
		SmoothedAvg: previous.TTFTSmoothedAvg,
		SmoothedP95: previous.TTFTSmoothedP95,
		SmoothedP99: previous.TTFTSmoothedP99,
	}, latency.WindowOptions{
		PreserveSmoothing: preserveSmoothing,
		AllowZeroPrevious: allowZeroPrevious,
	})
}

func assessTTFT(policy latency.Policy, previous dynamic.Snapshot, observation latency.Observation, running, waiting int, kvValue float64, preemptionDelta uint64, recoveryLoadLimit int, representativeLoad, demandPressure bool) latency.Assessment {
	return latency.Assess(latency.AssessInput{
		Previous: latency.WindowState{
			HighCount:     previous.TTFTHighCount,
			TailHighCount: previous.TTFTP99HighCount,
			HealthyCount:  previous.TTFTHealthyCount,
		},
		Policy:             policy,
		Running:            running,
		Waiting:            waiting,
		KVCacheUsage:       kvValue,
		Preemptions:        preemptionDelta,
		RecoveryLoadLimit:  recoveryLoadLimit,
		RequireLoadSignal:  true,
		RepresentativeLoad: representativeLoad,
		DemandPressure:     demandPressure,
	}, observation)
}

func ttftRecoveryLoadLimit(previous dynamic.Snapshot, baseLimit, globalLimit int) int {
	learned := previous.TTFTLearnedLimit
	if previous.Source != "metrics" || learned <= 0 {
		learned = baseLimit
	}
	if learned <= 0 {
		learned = globalLimit
	}
	if learned <= 0 {
		return 0
	}
	return tier.BasicLimit(learned)
}

func recommendedGlobalLimit(cfg Config, state string, staticLimit int) int {
	value := cfg.GlobalGreen
	switch state {
	case "yellow":
		value = cfg.GlobalYellow
	case "red":
		value = cfg.GlobalRed
	}
	if value > staticLimit {
		value = staticLimit
	}
	return value
}

func capacityPrevious(snapshot dynamic.Snapshot) capacity.Previous {
	return capacity.Previous{
		Source:                    snapshot.Source,
		CapacityTPS:               snapshot.CapacityTPS,
		CapacityLearnedLimit:      snapshot.CapacityLearnedLimit,
		CapacityTargetLimit:       snapshot.CapacityTargetLimit,
		CapacityRatioHealthyCount: snapshot.CapacityRatioHealthyCount,
		CapacityLearnState:        snapshot.CapacityLearnState,
		PrefillTransition:         snapshot.PrefillTransition,
		PrefillSettling:           snapshot.PrefillSettling,
		GlobalLimit:               snapshot.GlobalLimit,
		CapacityLimit:             snapshot.CapacityLimit,
	}
}
