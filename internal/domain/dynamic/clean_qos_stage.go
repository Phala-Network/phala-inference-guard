package dynamic

import (
	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

func evaluateCleanQOSLimit(cfg Config, signals cleanSignals, ttft cleanTTFTStage, builder decision.Builder, baseGlobalLimit int, enforceQOSLimit bool) (int, decision.Builder) {
	qosLimit := capacity.QoSGlobalLimit(cfg.Capacity, baseGlobalLimit, signals.DecodeRunning, signals.QOSTPS, signals.QOSTPSValid, enforceQOSLimit)
	if cfg.TTFTEnabled && ttft.Limit > 0 && ttft.Limit < qosLimit {
		qosLimit = ttft.Limit
		if ttft.Assessment.RedReady {
			builder.AddRedOnce("ttft_latency_capacity")
		} else if ttft.Assessment.YellowReady {
			builder.AddYellowOnce("ttft_latency_capacity")
		}
	}
	return qosLimit, builder
}

func enforceCleanUserTPSCapacityLimit(cfg Config, input Input, signals cleanSignals, builder decision.Builder, baseGlobalLimit, qosLimit int, enforceQOSLimit bool) (int, int, decision.Builder) {
	if !enforceQOSLimit || !cfg.UserTPSEnabled || !signals.QOSTPSValid || signals.DecodeRunning < cfg.UserTPSMinRun || signals.DecodeRunning <= 0 || qosLimit >= baseGlobalLimit {
		return qosLimit, baseGlobalLimit, builder
	}
	if signals.UserTPS < cfg.UserTPSRed {
		builder.AddRed("single_user_tps_capacity")
		baseGlobalLimit = recommendedGlobalLimit(cfg, builder.State(), input.GlobalLimit)
		qosLimit = num.MinInt(qosLimit, capacity.QoSGlobalLimit(cfg.Capacity, baseGlobalLimit, signals.DecodeRunning, signals.QOSTPS, signals.QOSTPSValid, enforceQOSLimit))
		return qosLimit, baseGlobalLimit, builder
	}
	if signals.UserTPS < cfg.UserTPSYellow {
		previousState := builder.State()
		builder.AddYellow("single_user_tps_capacity")
		if previousState == "green" {
			baseGlobalLimit = recommendedGlobalLimit(cfg, builder.State(), input.GlobalLimit)
			qosLimit = num.MinInt(qosLimit, capacity.QoSGlobalLimit(cfg.Capacity, baseGlobalLimit, signals.DecodeRunning, signals.QOSTPS, signals.QOSTPSValid, enforceQOSLimit))
		}
	}
	return qosLimit, baseGlobalLimit, builder
}
