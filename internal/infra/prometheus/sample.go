package prometheus

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func firstGaugeValue(values map[string]float64, metricNames ...string) float64 {
	for _, metricName := range metricNames {
		if value, ok := values[metricName]; ok {
			return value
		}
	}
	return 0
}

func firstGaugeValueOK(values map[string]float64, metricNames ...string) (float64, bool) {
	for _, metricName := range metricNames {
		if value, ok := values[metricName]; ok {
			return value, true
		}
	}
	return 0, false
}

func ParseSample(metricsText string) telemetry.Sample {
	values := ParseGaugeSetWithAggregation(metricsText, map[string]GaugeAggregation{
		"vllm:num_requests_running":        GaugeSum,
		"sglang:num_running_reqs":          GaugeMax,
		"vllm:num_requests_waiting":        GaugeSum,
		"sglang:num_queue_reqs":            GaugeMax,
		"vllm:kv_cache_usage_perc":         GaugeMax,
		"sglang:token_usage":               GaugeMax,
		"vllm:num_preemptions_total":       GaugeSum,
		"sglang:num_retracted_reqs":        GaugeMax,
		"sglang:num_paused_reqs":           GaugeMax,
		"vllm:generation_tokens_total":     GaugeSum,
		"sglang:generation_tokens_total":   GaugeMax,
		"sglang:gen_throughput":            GaugeMax,
		"sglang:max_total_num_tokens":      GaugeMax,
		"sglang:kv_used_tokens":            GaugeMax,
		"sglang:kv_cache_used_tokens":      GaugeMax,
		"sglang:used_tokens":               GaugeMax,
		"sglang:kv_available_tokens":       GaugeMax,
		"sglang:kv_cache_available_tokens": GaugeMax,
		"sglang:available_tokens":          GaugeMax,
		"sglang:kv_evictable_tokens":       GaugeMax,
		"sglang:kv_cache_evictable_tokens": GaugeMax,
		"sglang:evictable_tokens":          GaugeMax,
	})
	ttft := ParseFirstHistogram(metricsText,
		"vllm:time_to_first_token_seconds",
		"vllm:request_time_to_first_token_seconds",
		"sglang:time_to_first_token_seconds",
	)
	runningValue, runningPresent := firstGaugeValueOK(values,
		"vllm:num_requests_running",
		"sglang:num_running_reqs",
	)
	waitingValue, waitingPresent := firstGaugeValueOK(values,
		"vllm:num_requests_waiting",
		"sglang:num_queue_reqs",
	)
	kvValue := firstGaugeValue(values,
		"vllm:kv_cache_usage_perc",
		"sglang:token_usage",
	)
	preemptionValue, preemptionPresent := values["vllm:num_preemptions_total"]
	if !preemptionPresent {
		retracted, hasRetracted := values["sglang:num_retracted_reqs"]
		paused, hasPaused := values["sglang:num_paused_reqs"]
		preemptionValue = retracted + paused
		preemptionPresent = hasRetracted || hasPaused
	}
	running, runningValid := exactNonNegativeMetricInt(runningValue, runningPresent)
	waiting, waitingValid := exactNonNegativeMetricInt(waitingValue, waitingPresent)
	preemptions, preemptionsValid := exactNonNegativeMetricUint64(preemptionValue, preemptionPresent)
	generationValue, generationValid := firstGaugeValueOK(values,
		"vllm:generation_tokens_total",
		"sglang:generation_tokens_total",
	)
	generation, generationValid := exactNonNegativeMetricUint64(generationValue, generationValid)
	modelName, modelNameValid := parseRequiredUniqueMetricLabel(metricsText, []string{
		"vllm:num_requests_running",
		"vllm:num_requests_waiting",
		"vllm:num_preemptions_total",
		"vllm:generation_tokens_total",
	}, "model_name")
	_, hasVLLMGenerationCounter := values["vllm:generation_tokens_total"]
	generationTPSValue, generationTPSDirect := values["sglang:gen_throughput"]
	if hasVLLMGenerationCounter || !generationTPSDirect {
		generationTPSValue = 0
		generationTPSDirect = false
	}

	sample := telemetry.Sample{
		ModelName:           modelName,
		ModelNameValid:      modelNameValid,
		Running:             running,
		RunningValid:        runningValid,
		Waiting:             waiting,
		WaitingValid:        waitingValid,
		KVCacheUsage:        kvValue,
		Preemptions:         preemptions,
		PreemptionsValid:    preemptionsValid,
		Generation:          generation,
		GenerationValid:     generationValid,
		GenerationTPS:       generationTPSValue,
		GenerationTPSDirect: generationTPSDirect,
		TTFT:                ttft,
	}
	adaptKVTokenMetrics(metricsText, values, &sample)
	return sample
}

func adaptKVTokenMetrics(metricsText string, values map[string]float64, sample *telemetry.Sample) {
	vllmCapacity, vllmCapacityOK := ParseInfoLabelFloat(metricsText, "vllm:cache_config_info", "kv_cache_size_tokens", "kv_cache_size")
	vllmBlockSize, vllmBlockSizeOK := ParseInfoLabelFloat(metricsText, "vllm:cache_config_info", "block_size")
	blockSize, blockSizeValid := exactNonNegativeMetricInt(vllmBlockSize, vllmBlockSizeOK)
	blockSizeValid = blockSizeValid && blockSize > 0
	vllmUsage, vllmUsageOK := values["vllm:kv_cache_usage_perc"]
	if vllmCapacityOK || vllmUsageOK {
		sample.BackendKind = "vllm"
	}
	if vllmCapacityOK && finitePositive(vllmCapacity) && vllmUsageOK && finiteNonNegative(vllmUsage) {
		capacity := int64(math.Round(vllmCapacity))
		used := clampTokenValue(int64(math.Round(float64(capacity)*vllmUsage)), capacity)
		sample.KVCapacityTokens = capacity
		sample.KVBlockSize = blockSize
		sample.KVBlockSizeValid = blockSizeValid
		sample.KVUsedTokens = used
		sample.KVAvailableTokens = capacity - used
		sample.KVTokenMetricsValid = true
		return
	}

	sglangCapacity, sglangCapacityOK := values["sglang:max_total_num_tokens"]
	_, sglangUsageOK := values["sglang:token_usage"]
	usedValue, usedOK := firstGaugeValueOK(values,
		"sglang:kv_used_tokens",
		"sglang:kv_cache_used_tokens",
		"sglang:used_tokens",
	)
	availableValue, availableOK := firstGaugeValueOK(values,
		"sglang:kv_available_tokens",
		"sglang:kv_cache_available_tokens",
		"sglang:available_tokens",
	)
	evictableValue, evictableOK := firstGaugeValueOK(values,
		"sglang:kv_evictable_tokens",
		"sglang:kv_cache_evictable_tokens",
		"sglang:evictable_tokens",
	)
	if sglangCapacityOK || sglangUsageOK || usedOK || availableOK || evictableOK {
		sample.BackendKind = "sglang"
	}
	if !sglangCapacityOK || !finitePositive(sglangCapacity) {
		return
	}
	usedOK = usedOK && finiteNonNegative(usedValue)
	availableOK = availableOK && finiteNonNegative(availableValue)
	evictableOK = evictableOK && finiteNonNegative(evictableValue)
	capacity := int64(math.Round(sglangCapacity))
	available := tokenGaugeValue(availableValue, availableOK, capacity)
	evictable := tokenGaugeValue(evictableValue, evictableOK, capacity)
	directUsed := tokenGaugeValue(usedValue, usedOK, capacity)
	identityValid := availableOK && evictableOK && available+evictable <= capacity
	identityUsed := capacity - available - evictable

	used := directUsed
	valid := usedOK
	if identityValid {
		capacityMinusAvailable := capacity - available
		tolerance := int64(math.Max(32, float64(capacity)*0.001))
		if !usedOK || absInt64(directUsed-capacityMinusAvailable) <= tolerance || directUsed > identityUsed+tolerance {
			used = identityUsed
		}
		valid = true
	}
	if !valid {
		return
	}
	sample.KVCapacityTokens = capacity
	sample.KVUsedTokens = clampTokenValue(used, capacity)
	if availableOK {
		sample.KVAvailableTokens = available
	} else {
		sample.KVAvailableTokens = capacity - sample.KVUsedTokens
	}
	if evictableOK {
		sample.KVEvictableTokens = evictable
	}
	sample.KVCacheUsage = float64(sample.KVUsedTokens) / float64(capacity)
	sample.KVTokenMetricsValid = true
}

func clampTokenValue(value, capacity int64) int64 {
	if value < 0 {
		return 0
	}
	if value > capacity {
		return capacity
	}
	return value
}

func tokenGaugeValue(value float64, valid bool, capacity int64) int64 {
	if !valid {
		return 0
	}
	return clampTokenValue(int64(math.Round(value)), capacity)
}

func absInt64(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func finiteNonNegative(value float64) bool {
	return value >= 0 && !math.IsInf(value, 0) && !math.IsNaN(value)
}

func exactNonNegativeMetricInt(value float64, present bool) (int, bool) {
	maximum := int(^uint(0) >> 1)
	if !present || !finiteNonNegative(value) || math.Trunc(value) != value || value > float64(maximum) {
		return 0, false
	}
	return int(value), true
}

func exactNonNegativeMetricUint64(value float64, present bool) (uint64, bool) {
	const maximumExactFloatInteger = float64(1 << 53)
	if !present || !finiteNonNegative(value) || math.Trunc(value) != value || value > maximumExactFloatInteger {
		return 0, false
	}
	return uint64(value), true
}
