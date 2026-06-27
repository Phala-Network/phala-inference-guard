package dynamic

import (
	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type cleanPrefillStage struct {
	Limit        int
	Reason       string
	TargetReason string
}

func evaluateCleanPrefillStage(cfg Config, input Input, signals cleanSignals, throughput cleanThroughputStage, currentLimit int) cleanPrefillStage {
	stage := cleanPrefillStage{Limit: currentLimit, Reason: "inactive", TargetReason: "current_limit"}
	if !signals.PrefillTransition {
		return stage
	}
	result := capacity.EvaluatePrefillLimit(capacity.PrefillInput{
		Config:           cfg.Capacity,
		Previous:         signals.CapacityPrevious,
		BaseLimit:        currentLimit,
		GlobalLimit:      input.GlobalLimit,
		Running:          signals.Running,
		DecodeRunning:    signals.DecodeRunning,
		Waiting:          signals.Waiting,
		PrefillProtected: signals.PrefillProtected,
		CapacityTPS:      signals.CapacityTPS,
	})
	stage.Limit = result.Limit
	stage.Reason = result.Reason
	stage.TargetReason = result.TargetReason
	pressureFree := signals.Waiting == 0 && signals.PreemptionDelta == 0 && !capacity.KVPressureActive(cfg.Capacity, signals.KVCacheUsage)
	if pressureFree && !cleanCapacityDownState(throughput.Result.State) && throughput.Result.LearnedLimit > 0 && stage.Limit < throughput.Result.LearnedLimit {
		stage.Limit = num.MinInt(throughput.Result.LearnedLimit, currentLimit)
		stage.Reason = "throughput_floor"
		stage.TargetReason = "learned_capacity"
	}
	return stage
}
