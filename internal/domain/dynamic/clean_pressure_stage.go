package dynamic

import "github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"

type cleanPressureStage struct {
	Limit        int
	Reason       string
	TargetReason string
}

func evaluateCleanPressureStage(cfg Config, input Input, signals cleanSignals, currentLimit int) cleanPressureStage {
	pressureCap := input.PressureCap
	if pressureCap == nil {
		pressureCap = &capacity.PressureCap{}
	}
	capacity.RecoverPressureCap(pressureCap, cfg.Capacity, currentLimit, signals.Running, signals.Waiting, signals.DecodeRunning, signals.GenerationTPS, signals.GenerationTPSValid, signals.CapacityDemandPressure)
	result := capacity.EvaluatePressureLimit(pressureCap, cfg.Capacity, currentLimit, signals.Running, signals.Waiting, signals.DecodeRunning, signals.KVCacheUsage, signals.PreemptionDelta, signals.UserTPS, signals.QOSHealthy, signals.UserTPSRedReady, signals.PrefillFreeze)
	return cleanPressureStage{Limit: result.Limit, Reason: result.Reason, TargetReason: result.TargetReason}
}
