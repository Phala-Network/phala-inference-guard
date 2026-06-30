package dynamic

import (
	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
)

func applyCleanThroughputLimit(signals cleanSignals, throughput cleanThroughputStage, builder decision.Builder, qosLimit, capacityLimit int) (int, decision.Builder) {
	if capacityLimit >= qosLimit {
		return qosLimit, builder
	}
	if cleanCapacityDownState(throughput.Result.State) {
		builder.AddYellow("single_user_tps_capacity")
	}
	return capacityLimit, builder
}

func applyCleanPressureLimit(cfg Config, signals cleanSignals, pressure cleanPressureStage, builder decision.Builder, qosLimit int) (int, decision.Builder) {
	if pressure.Limit >= qosLimit {
		return qosLimit, builder
	}
	if !cleanPressureReasonActive(cfg, signals, pressure) {
		return pressure.Limit, builder
	}
	if builder.State() == "red" {
		builder.AddRed("scheduler_pressure_capacity")
	} else {
		builder.AddYellow("scheduler_pressure_capacity")
	}
	return pressure.Limit, builder
}

func cleanPressureReasonActive(cfg Config, signals cleanSignals, pressure cleanPressureStage) bool {
	switch pressure.Reason {
	case "severe_pressure", "waiting_pressure", "kv_pressure", "healthy_kv_headroom", "backend_waiting", "backend_unavailable":
		return true
	case "learned_cap":
		return signals.Waiting > 0 ||
			signals.PreemptionDelta > 0 ||
			capacity.KVPressureActive(cfg.Capacity, signals.KVCacheUsage) ||
			(signals.Running >= pressure.Limit && signals.CapacityDemandPressure)
	default:
		return false
	}
}
