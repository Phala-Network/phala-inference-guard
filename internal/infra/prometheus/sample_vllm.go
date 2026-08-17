package prometheus

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

var vllmModelIdentityMetrics = []string{
	"vllm:num_requests_running",
	"vllm:num_requests_waiting",
	"vllm:num_preemptions_total",
	"vllm:generation_tokens_total",
}

func parseVLLMSample(index metricIndex) telemetry.Sample {
	runningValue, runningPresent := index.sum("vllm:num_requests_running", nil)
	waitingValue, waitingPresent := index.sum("vllm:num_requests_waiting", nil)
	preemptionValue, preemptionPresent := index.sum("vllm:num_preemptions_total", nil)
	generationValue, generationPresent := index.sum("vllm:generation_tokens_total", nil)
	usage, usagePresent := index.maximum("vllm:kv_cache_usage_perc", nil)
	modelName, modelNameValid := index.requiredUniqueLabel(vllmModelIdentityMetrics, "model_name")

	running, runningValid := exactNonNegativeMetricInt(
		runningValue,
		runningPresent && index.declaredType("vllm:num_requests_running", "gauge"),
	)
	waiting, waitingValid := exactNonNegativeMetricInt(
		waitingValue,
		waitingPresent && index.declaredType("vllm:num_requests_waiting", "gauge"),
	)
	preemptions, preemptionsValid := exactNonNegativeMetricUint64(
		preemptionValue,
		preemptionPresent && index.declaredType("vllm:num_preemptions_total", "counter"),
	)
	generation, generationValid := exactNonNegativeMetricUint64(
		generationValue,
		generationPresent && index.declaredType("vllm:generation_tokens_total", "counter"),
	)

	sample := telemetry.Sample{
		BackendKind:      "vllm",
		ModelName:        modelName,
		ModelNameValid:   modelNameValid,
		Running:          running,
		RunningValid:     runningValid,
		Waiting:          waiting,
		WaitingValid:     waitingValid,
		Preemptions:      preemptions,
		PreemptionsValid: preemptionsValid,
		Generation:       generation,
		GenerationValid:  generationValid,
	}
	adaptVLLMKV(index, usage, usagePresent, &sample)
	adaptVLLMCache(index, modelName, &sample)
	return sample
}

func adaptVLLMKV(index metricIndex, usage float64, usagePresent bool, sample *telemetry.Sample) {
	if sample == nil {
		return
	}
	capacityValue, capacityPresent := index.uniqueFloatLabel(
		"vllm:cache_config_info",
		"kv_cache_size_tokens",
		"kv_cache_size",
	)
	blockValue, blockPresent := index.uniqueFloatLabel("vllm:cache_config_info", "block_size")
	capacity, capacityValid := exactNonNegativeMetricInt64(capacityValue, capacityPresent)
	blockSize, blockValid := exactNonNegativeMetricInt(blockValue, blockPresent)
	if !index.declaredType("vllm:cache_config_info", "gauge") ||
		!index.declaredType("vllm:kv_cache_usage_perc", "gauge") ||
		!capacityValid || capacity <= 0 || !blockValid || blockSize <= 0 ||
		!usagePresent || !finiteNonNegative(usage) || usage > 1 {
		return
	}
	used := clampTokenValue(int64(math.Round(float64(capacity)*usage)), capacity)
	sample.KVCapacityTokens = capacity
	sample.KVBlockSize = blockSize
	sample.KVBlockSizeValid = true
	sample.KVUsedTokens = used
	sample.KVAvailableTokens = capacity - used
	sample.KVCacheUsage = usage
	sample.KVTokenMetricsValid = true
}

func adaptVLLMCache(index metricIndex, modelName string, sample *telemetry.Sample) {
	if sample == nil || modelName == "" ||
		!index.declaredType("vllm:prefix_cache_queries_total", "counter") ||
		!index.declaredType("vllm:prefix_cache_hits_total", "counter") {
		return
	}
	queries, queriesValid := vllmCacheCountersByEngine(index, "vllm:prefix_cache_queries_total", modelName)
	hits, hitsValid := vllmCacheCountersByEngine(index, "vllm:prefix_cache_hits_total", modelName)
	if !queriesValid || !hitsValid || len(queries) == 0 || len(queries) != len(hits) {
		return
	}
	var queryTotal, hitTotal uint64
	for engine, query := range queries {
		hit, ok := hits[engine]
		if !ok || hit > query || queryTotal > (1<<53)-query || hitTotal > (1<<53)-hit {
			return
		}
		queryTotal += query
		hitTotal += hit
	}
	sample.CacheQueryTokens = queryTotal
	sample.CacheHitTokens = hitTotal
	sample.CacheTokensValid = true
}

func vllmCacheCountersByEngine(index metricIndex, metricName, modelName string) (map[string]uint64, bool) {
	values := make(map[string]uint64)
	for _, item := range index.samples[metricName] {
		if !item.labelsValid || item.labels["model_name"] != modelName {
			return nil, false
		}
		engine := item.labels["engine"]
		if engine == "" {
			return nil, false
		}
		value, valid := exactNonNegativeMetricUint64(item.value, item.valueValid)
		if !valid {
			return nil, false
		}
		if _, duplicate := values[engine]; duplicate {
			return nil, false
		}
		values[engine] = value
	}
	return values, true
}
