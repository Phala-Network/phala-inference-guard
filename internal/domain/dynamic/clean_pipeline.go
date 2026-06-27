package dynamic

import (
	"time"
)

type cleanPipelineResult struct {
	Signals    cleanSignals
	TTFT       cleanTTFTStage
	Throughput cleanThroughputStage
	Pressure   cleanPressureStage
	Prefill    cleanPrefillStage
	Limits     cleanLimitStage
}

func evaluateCleanPipeline(cfg Config, input Input, now time.Time) cleanPipelineResult {
	signals := deriveCleanSignals(cfg, input, now)
	ttft := observeCleanTTFTStage(cfg, input, signals)
	evaluation := evaluateCleanLimitStage(cfg, input, signals, ttft)

	return cleanPipelineResult{
		Signals:    signals,
		TTFT:       evaluation.TTFT,
		Throughput: evaluation.Throughput,
		Pressure:   evaluation.Pressure,
		Prefill:    evaluation.Prefill,
		Limits:     evaluation.Limits,
	}
}
