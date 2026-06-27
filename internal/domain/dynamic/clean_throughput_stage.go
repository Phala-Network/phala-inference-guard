package dynamic

import (
	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
)

type cleanThroughputStage struct {
	Result capacity.LearnResult
	Limit  int
}

func evaluateCleanThroughputStage(cfg Config, input Input, signals cleanSignals, ttft cleanTTFTStage, baseGlobalLimit, currentLimit int) cleanThroughputStage {
	stage := cleanThroughputStage{
		Result: capacity.LearnResult{
			LearnedLimit: baseGlobalLimit,
			TargetLimit:  baseGlobalLimit,
			State:        "disabled",
			Reason:       "disabled",
			TargetReason: "base_limit",
		},
		Limit: currentLimit,
	}
	if !cfg.UserTPSEnabled {
		return stage
	}
	ttftHealthyForThroughput := !cfg.TTFTEnabled || ttft.Assessment.Healthy || (!ttft.Assessment.YellowReady && !ttft.Assessment.RedReady && !ttft.Observation.Valid)
	stage.Result = capacity.CleanLearnCap(capacity.CleanLearnInput{
		Config:             cfg.Capacity,
		Previous:           signals.CapacityPrevious,
		BaseLimit:          baseGlobalLimit,
		Estimate:           signals.CapacityEstimate,
		GenerationTPSValid: signals.QOSTPSValid,
		CapacityTPS:        signals.CapacityTPS,
		Running:            signals.Running,
		DecodeRunning:      signals.DecodeRunning,
		Waiting:            signals.Waiting,
		KVCacheUsage:       signals.KVCacheUsage,
		PreemptionDelta:    signals.PreemptionDelta,
		UserTPS:            signals.UserTPS,
		QOSYellowReady:     signals.UserTPSYellowReady,
		QOSRedReady:        signals.UserTPSRedReady,
		TTFTHealthy:        ttftHealthyForThroughput,
		Freeze:             signals.PrefillFreeze,
		QueueCurrent:       input.QueueCurrent,
		DynamicRejected:    signals.DynamicRejectedDelta,
		DemandPressure:     signals.CapacityDemandPressure,
	})
	stage.Limit = decision.ApplyCapacityLimit(decision.CapacityLimitInput{
		CurrentLimit:           currentLimit,
		PreviousLimit:          signals.CapacityPrevious.CapacityLimit,
		LearnedLimit:           stage.Result.LearnedLimit,
		TargetLimit:            stage.Result.TargetLimit,
		LearnState:             stage.Result.State,
		DemandPressure:         signals.CapacityDemandPressure,
		PrefillTransition:      signals.PrefillFreeze,
		ProvisionalGrowthRatio: capacity.ProvisionalGrowthRatio,
	})
	return stage
}

func cleanCapacityDownState(state string) bool {
	switch state {
	case "learn_down", "pig_down", "pressure_down":
		return true
	default:
		return false
	}
}
