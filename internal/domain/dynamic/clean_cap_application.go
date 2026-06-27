package dynamic

import "github.com/Phala-Network/phala-inference-guard/internal/domain/decision"

func applyCleanThroughputLimit(signals cleanSignals, throughput cleanThroughputStage, builder decision.Builder, qosLimit, capacityLimit int) (int, decision.Builder) {
	if capacityLimit >= qosLimit {
		return qosLimit, builder
	}
	if cleanCapacityDownState(throughput.Result.State) {
		builder.AddYellow("single_user_tps_capacity")
	}
	return capacityLimit, builder
}

func applyCleanPressureLimit(pressure cleanPressureStage, builder decision.Builder, qosLimit int) (int, decision.Builder) {
	if pressure.Limit >= qosLimit {
		return qosLimit, builder
	}
	if builder.State() == "red" {
		builder.AddRed("scheduler_pressure_capacity")
	} else {
		builder.AddYellow("scheduler_pressure_capacity")
	}
	return pressure.Limit, builder
}

func applyCleanPrefillLimit(prefill cleanPrefillStage, builder decision.Builder, qosLimit int) (int, decision.Builder) {
	if prefill.Limit >= qosLimit {
		return qosLimit, builder
	}
	return prefill.Limit, builder
}
