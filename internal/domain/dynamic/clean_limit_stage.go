package dynamic

import "github.com/Phala-Network/phala-inference-guard/internal/domain/decision"

type cleanLimitStage struct {
	Decision          decision.Decision
	HardGlobalLimit   int
	BaseGlobalLimit   int
	StateLimit        int
	QOSLimit          int
	ThroughputLimit   int
	CapacityLimit     int
	PressureLimit     int
	PrefillLimit      int
	AvailabilityLimit int
	FinalLimit        int
	FinalLimitReason  string
}

type cleanLimitEvaluation struct {
	TTFT       cleanTTFTStage
	Throughput cleanThroughputStage
	Pressure   cleanPressureStage
	Prefill    cleanPrefillStage
	Limits     cleanLimitStage
}

func evaluateCleanLimitStage(cfg Config, input Input, signals cleanSignals, ttft cleanTTFTStage) cleanLimitEvaluation {
	signalResult := evaluateCleanSignalState(cfg, signals, ttft)
	builder := decision.NewBuilderFromSignal(signalResult)

	learningBaseLimit := input.GlobalLimit
	stateLimit := recommendedGlobalLimit(cfg, builder.State(), input.GlobalLimit)
	ttft = learnCleanTTFTStage(cfg, input, signals, ttft, learningBaseLimit)

	enforceQOSLimit := cleanEnforceQOSLimit(cfg, signals)
	qosLimit, builder := evaluateCleanQOSLimit(cfg, signals, ttft, builder, stateLimit, enforceQOSLimit)

	throughput := evaluateCleanThroughputStage(cfg, input, signals, ttft, learningBaseLimit, qosLimit)
	capacityLimit := throughput.Limit
	qosLimit, builder = applyCleanThroughputLimit(signals, throughput, builder, qosLimit, capacityLimit)
	qosLimit, stateLimit, builder = enforceCleanUserTPSCapacityLimit(cfg, input, signals, builder, stateLimit, qosLimit, enforceQOSLimit)

	pressure := evaluateCleanPressureStage(cfg, input, signals, qosLimit)
	qosLimit, builder = applyCleanPressureLimit(pressure, builder, qosLimit)

	prefill := evaluateCleanPrefillStage(cfg, input, signals, throughput, qosLimit)

	limits, pressure, prefill := composeCleanFinalLimit(input, signals, ttft, throughput, pressure, prefill, builder, stateLimit, capacityLimit)
	return cleanLimitEvaluation{
		TTFT:       ttft,
		Throughput: throughput,
		Pressure:   pressure,
		Prefill:    prefill,
		Limits:     limits,
	}
}
