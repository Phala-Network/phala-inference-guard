package prometheus

import "github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"

func firstGaugeValue(values map[string]float64, metricNames ...string) float64 {
	for _, metricName := range metricNames {
		if value, ok := values[metricName]; ok {
			return value
		}
	}
	return 0
}

func ParseSample(metricsText string) telemetry.Sample {
	values := ParseGaugeSetWithAggregation(metricsText, map[string]GaugeAggregation{
		"vllm:num_requests_running":      GaugeSum,
		"sglang:num_running_reqs":        GaugeMax,
		"vllm:num_requests_waiting":      GaugeSum,
		"sglang:num_queue_reqs":          GaugeMax,
		"vllm:kv_cache_usage_perc":       GaugeMax,
		"sglang:token_usage":             GaugeMax,
		"vllm:num_preemptions_total":     GaugeSum,
		"sglang:num_retracted_reqs":      GaugeMax,
		"sglang:num_paused_reqs":         GaugeMax,
		"vllm:generation_tokens_total":   GaugeSum,
		"sglang:generation_tokens_total": GaugeMax,
		"sglang:gen_throughput":          GaugeMax,
	})
	ttft := ParseFirstHistogram(metricsText,
		"vllm:time_to_first_token_seconds",
		"vllm:request_time_to_first_token_seconds",
		"sglang:time_to_first_token_seconds",
	)
	runningValue := firstGaugeValue(values,
		"vllm:num_requests_running",
		"sglang:num_running_reqs",
	)
	waitingValue := firstGaugeValue(values,
		"vllm:num_requests_waiting",
		"sglang:num_queue_reqs",
	)
	kvValue := firstGaugeValue(values,
		"vllm:kv_cache_usage_perc",
		"sglang:token_usage",
	)
	preemptionValue := values["vllm:num_preemptions_total"] + values["sglang:num_retracted_reqs"] + values["sglang:num_paused_reqs"]
	generationValue := firstGaugeValue(values,
		"vllm:generation_tokens_total",
		"sglang:generation_tokens_total",
	)
	_, hasVLLMGenerationCounter := values["vllm:generation_tokens_total"]
	generationTPSValue, generationTPSDirect := values["sglang:gen_throughput"]
	if hasVLLMGenerationCounter || !generationTPSDirect {
		generationTPSValue = 0
		generationTPSDirect = false
	}

	return telemetry.Sample{
		Running:             int(runningValue),
		Waiting:             int(waitingValue),
		KVCacheUsage:        kvValue,
		Preemptions:         uint64(preemptionValue),
		Generation:          uint64(generationValue),
		GenerationTPS:       generationTPSValue,
		GenerationTPSDirect: generationTPSDirect,
		TTFT:                ttft,
	}
}
