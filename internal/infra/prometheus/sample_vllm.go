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

	running, runningValid := exactNonNegativeMetricInt(runningValue, runningPresent)
	waiting, waitingValid := exactNonNegativeMetricInt(waitingValue, waitingPresent)
	preemptions, preemptionsValid := exactNonNegativeMetricUint64(preemptionValue, preemptionPresent)
	generation, generationValid := exactNonNegativeMetricUint64(generationValue, generationPresent)

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
	if !capacityValid || capacity <= 0 || !blockValid || blockSize <= 0 ||
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
