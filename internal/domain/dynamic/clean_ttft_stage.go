package dynamic

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/latency"
	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type cleanTTFTStage struct {
	Observation        latency.Observation
	Assessment         latency.Assessment
	SemanticCumulative telemetry.HistogramSample
	Source             string
	PreviousSnapshot   runtimedynamic.Snapshot
	RecoveryLimit      int
	LearnedLimit       int
	TargetLimit        int
	Limit              int
	State              string
	Reason             string
	TargetReason       string
	RepresentativeLoad bool
	DemandPressure     bool
}

func observeCleanTTFTStage(cfg Config, input Input, signals cleanSignals) cleanTTFTStage {
	stage := cleanTTFTStage{
		SemanticCumulative: input.SemanticTTFT,
		Source:             "disabled",
		PreviousSnapshot:   signals.PreviousSnapshot,
	}
	if !cfg.TTFTEnabled {
		return stage
	}
	stage.Observation, stage.SemanticCumulative, stage.Source = observeDynamicTTFTWindow(signals.Aggregated.TTFT, input.SemanticTTFT, signals.PreviousSnapshot)
	if signals.PreviousSnapshot.Source == "metrics" && signals.PreviousSnapshot.TTFTSource != "" && signals.PreviousSnapshot.TTFTSource != stage.Source {
		stage.PreviousSnapshot.TTFTHighCount = 0
		stage.PreviousSnapshot.TTFTP99HighCount = 0
		stage.PreviousSnapshot.TTFTHealthyCount = 0
	}
	stage.RecoveryLimit = ttftRecoveryLoadLimit(stage.PreviousSnapshot, input.GlobalLimit, input.GlobalLimit)
	stage.DemandPressure = ttftDemandPressure(input, signals)
	stage.RepresentativeLoad = ttftRepresentativeLoad(stage.PreviousSnapshot, input.GlobalLimit, signals.Running, signals.DecodeRunning, signals.PrefillFreeze)
	stage.Assessment = assessTTFT(stage.PreviousSnapshot, stage.Observation, signals.Running, signals.Waiting, signals.KVCacheUsage, signals.PreemptionDelta, stage.RecoveryLimit, stage.RepresentativeLoad, stage.DemandPressure)
	return stage
}

func learnCleanTTFTStage(cfg Config, input Input, signals cleanSignals, stage cleanTTFTStage, baseGlobalLimit int) cleanTTFTStage {
	stage.LearnedLimit = baseGlobalLimit
	stage.TargetLimit = baseGlobalLimit
	stage.Limit = baseGlobalLimit
	stage.State = "disabled"
	stage.Reason = "disabled"
	stage.TargetReason = "base_limit"
	if !cfg.TTFTEnabled {
		return stage
	}
	stage.RecoveryLimit = ttftRecoveryLoadLimit(stage.PreviousSnapshot, baseGlobalLimit, input.GlobalLimit)
	result := latency.LearnCap(latency.LearnInput{
		PreviousSource:       stage.PreviousSnapshot.Source,
		PreviousLearnedLimit: stage.PreviousSnapshot.TTFTLearnedLimit,
		PreviousTargetLimit:  stage.PreviousSnapshot.TTFTTargetLimit,
		BaseLimit:            baseGlobalLimit,
		Running:              signals.Running,
		StepUpRatio:          cfg.CapacityStepUp,
		Observation:          stage.Observation,
		Assessment:           stage.Assessment,
		RecoveryLoadLimit:    stage.RecoveryLimit,
		DemandPressure:       stage.DemandPressure,
		RequireLoadSignal:    true,
		RepresentativeLoad:   stage.RepresentativeLoad,
	})
	stage.LearnedLimit = result.LearnedLimit
	stage.TargetLimit = result.TargetLimit
	stage.State = result.State
	stage.Reason = result.Reason
	stage.TargetReason = result.TargetReason
	stage.Limit = result.Limit
	stage.Assessment.HighCount = result.HighCount
	stage.Assessment.TailHighCount = result.TailHighCount
	stage.Assessment.HealthyCount = result.HealthyCount
	return stage
}

func ttftDemandPressure(input Input, signals cleanSignals) bool {
	return input.QueueCurrent > 0 || signals.DynamicRejectedDelta > 0 || signals.TierDemandPressure || signals.Waiting > 0 || signals.PreemptionDelta > 0
}

func ttftRepresentativeLoad(previous runtimedynamic.Snapshot, globalLimit, running, decodeRunning int, prefillTransition bool) bool {
	if prefillTransition {
		return false
	}
	basis := previous.GlobalLimit
	if basis <= 0 || (globalLimit > 0 && basis > globalLimit) {
		basis = globalLimit
	}
	if previous.TTFTLimit > 0 && (basis <= 0 || previous.TTFTLimit < basis) {
		basis = previous.TTFTLimit
	}
	if basis <= 0 {
		return running >= latency.MinRunning || decodeRunning >= latency.MinRunning
	}
	threshold := int(math.Ceil(float64(basis) * 0.65))
	if threshold < latency.MinRunning {
		threshold = latency.MinRunning
	}
	if threshold > basis {
		threshold = basis
	}
	return running >= threshold || decodeRunning >= threshold
}
