package dynamic

import "github.com/Phala-Network/phala-inference-guard/internal/domain/decision"

func composeCleanFinalLimit(input Input, signals cleanSignals, ttft cleanTTFTStage, throughput cleanThroughputStage, pressure cleanPressureStage, prefill cleanPrefillStage, builder decision.Builder, stateLimit, capacityLimit int) (cleanLimitStage, cleanPressureStage, cleanPrefillStage) {
	hardGlobalLimit := input.GlobalLimit
	intake := evaluateCleanIntakeGuard(signals, builder.State(), input.BackendFailed, pressure, prefill, stateLimit)
	pressure = intake.Pressure
	prefill = intake.Prefill
	availabilityLimit := intake.AvailabilityLimit
	for _, reason := range intake.YellowReasons {
		builder.AddYellowOnce(reason)
	}

	components := [...]decision.LimitComponent{
		{Reason: "hard_global", Limit: hardGlobalLimit},
		{Reason: "state", Limit: stateLimit},
		{Reason: "ttft", Limit: ttft.Limit},
		{Reason: "throughput", Limit: throughput.Limit},
		{Reason: "pressure", Limit: pressure.Limit},
		{Reason: "prefill", Limit: prefill.Limit},
		{Reason: "availability", Limit: availabilityLimit},
	}
	finalLimit := decision.EnforceFinalLimitComponents(intake.FinalLimitReasonOverride, components[:])
	qosLimit := finalLimit.Limit

	limits := cleanLimitStage{
		HardGlobalLimit:   hardGlobalLimit,
		BaseGlobalLimit:   hardGlobalLimit,
		StateLimit:        stateLimit,
		QOSLimit:          qosLimit,
		ThroughputLimit:   throughput.Limit,
		CapacityLimit:     capacityLimit,
		PressureLimit:     pressure.Limit,
		PrefillLimit:      prefill.Limit,
		AvailabilityLimit: availabilityLimit,
		FinalLimit:        qosLimit,
		FinalLimitReason:  finalLimit.Reason,
	}
	limits.Decision = builder.Build(decision.Limits{
		HardGlobal:   limits.HardGlobalLimit,
		BaseGlobal:   limits.BaseGlobalLimit,
		State:        limits.StateLimit,
		QOS:          limits.QOSLimit,
		Throughput:   limits.ThroughputLimit,
		Capacity:     limits.CapacityLimit,
		TTFT:         ttft.Limit,
		Pressure:     limits.PressureLimit,
		Prefill:      limits.PrefillLimit,
		Availability: limits.AvailabilityLimit,
		Final:        limits.FinalLimit,
	})

	return limits, pressure, prefill
}
