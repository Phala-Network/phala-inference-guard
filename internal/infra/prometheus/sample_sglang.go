package prometheus

import "github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"

const sglangRetractionCounter = "sglang:num_retracted_requests_total"
const sglangRealtimeTokenCounter = "sglang:realtime_tokens_total"

var sglangStaticIdentityMetrics = []string{
	"sglang:max_total_num_tokens",
	"sglang:page_size",
	"sglang:num_pages",
}

var sglangAdmissionIdentityMetrics = []string{
	"sglang:max_total_num_tokens",
	"sglang:page_size",
	"sglang:num_pages",
	"sglang:kv_available_tokens",
	"sglang:kv_evictable_tokens",
	"sglang:kv_used_tokens",
	"sglang:num_running_reqs",
	"sglang:num_queue_reqs",
	"sglang:realtime_tokens_total",
	"sglang:num_retracted_requests_total",
}

func parseSGLangSample(metricsText string, index metricIndex) telemetry.Sample {
	runningValue, runningPresent := index.maximum("sglang:num_running_reqs", nil)
	waitingValue, waitingPresent := index.maximum("sglang:num_queue_reqs", nil)
	generationValue, generationPresent := index.maximum(sglangRealtimeTokenCounter, func(labels map[string]string) bool {
		return labels["mode"] == "decode"
	})
	preemptions, preemptionsValid := parseSGLangRetractions(index)
	modelName, staticModelValid := index.requiredUniqueLabel(sglangStaticIdentityMetrics, "model_name")
	observedModel, allModelsValid := index.uniqueLabelAcrossPresent(sglangAdmissionIdentityMetrics, "model_name", false)
	engineType, staticEngineValid := index.requiredUniqueLabel(sglangStaticIdentityMetrics, "engine_type")
	observedEngine, allEnginesValid := index.uniqueLabelAcrossPresent(sglangAdmissionIdentityMetrics, "engine_type", false)
	_, singleDPReplica := index.uniqueLabelAcrossPresent(sglangAdmissionIdentityMetrics, "dp_rank", true)
	modelNameValid := staticModelValid && allModelsValid && modelName == observedModel &&
		staticEngineValid && allEnginesValid && engineType == "unified" && engineType == observedEngine &&
		singleDPReplica
	running, runningValid := exactNonNegativeMetricInt(runningValue, runningPresent)
	waiting, waitingValid := exactNonNegativeMetricInt(waitingValue, waitingPresent)
	if !runningPresent && index.declaredType("sglang:num_running_reqs", "gauge") {
		runningValid = true
	}
	if !waitingPresent && index.declaredType("sglang:num_queue_reqs", "gauge") {
		waitingValid = true
	}
	generation := uint64(0)
	generationValid := index.declaredType(sglangRealtimeTokenCounter, "counter")
	if generationPresent {
		generation, generationValid = exactNonNegativeMetricUint64(generationValue, generationValid)
	}

	sample := telemetry.Sample{
		BackendKind:      "sglang",
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
		TTFT:             ParseFirstHistogram(metricsText, "sglang:time_to_first_token_seconds"),
	}
	adaptSGLangKV(index, &sample)
	return sample
}

func parseSGLangRetractions(index metricIndex) (uint64, bool) {
	if !index.declaredType(sglangRetractionCounter, "counter") {
		return 0, false
	}
	value, present := index.maximum(sglangRetractionCounter, nil)
	if !present {
		// A labeled prometheus_client counter has no sample until labels() is
		// first called. The registered TYPE declaration makes absence an exact
		// zero rather than an unsupported metric.
		return 0, true
	}
	return exactNonNegativeMetricUint64(value, true)
}

func adaptSGLangKV(index metricIndex, sample *telemetry.Sample) {
	if sample == nil {
		return
	}
	capacityValue, capacityPresent := index.uniqueValue("sglang:max_total_num_tokens")
	availableValue, availablePresent := index.uniqueValue("sglang:kv_available_tokens")
	evictableValue, evictablePresent := index.uniqueValue("sglang:kv_evictable_tokens")
	directUsedValue, directUsedPresent := index.uniqueValue("sglang:kv_used_tokens")
	capacity, capacityValid := exactNonNegativeMetricInt64(capacityValue, capacityPresent)
	available, availableValid := exactNonNegativeMetricInt64(availableValue, availablePresent)
	evictable, evictableValid := exactNonNegativeMetricInt64(evictableValue, evictablePresent)
	directUsed, directUsedValid := exactNonNegativeMetricInt64(directUsedValue, directUsedPresent)
	registeredColdKV := !availablePresent && !evictablePresent && !directUsedPresent &&
		index.declaredType("sglang:kv_available_tokens", "gauge") &&
		index.declaredType("sglang:kv_evictable_tokens", "gauge") &&
		index.declaredType("sglang:kv_used_tokens", "gauge")
	if registeredColdKV && capacityValid {
		available = capacity
		availableValid = true
		evictableValid = true
		directUsedValid = true
	}
	if !capacityValid || capacity <= 0 || !availableValid || !evictableValid || !directUsedValid ||
		available > capacity || evictable > capacity-available {
		return
	}
	used := capacity - available - evictable
	if directUsed != used {
		return
	}

	pageValue, pagePresent := index.uniqueValue("sglang:page_size")
	pageCountValue, pageCountPresent := index.uniqueValue("sglang:num_pages")
	pageSize, pageValid := exactNonNegativeMetricInt(pageValue, pagePresent)
	pageCount, pageCountValid := exactNonNegativeMetricInt64(pageCountValue, pageCountPresent)
	geometryPresent := pagePresent || pageCountPresent
	geometryValid := pageValid && pageSize > 0 && pageCountValid && pageCount > 0 &&
		pageCount == capacity/int64(pageSize)
	if geometryPresent && !geometryValid {
		return
	}

	sample.KVCapacityTokens = capacity
	sample.KVUsedTokens = used
	sample.KVAvailableTokens = available
	sample.KVEvictableTokens = evictable
	sample.KVCacheUsage = float64(used) / float64(capacity)
	sample.KVTokenMetricsValid = true
	if geometryValid {
		sample.KVBlockSize = pageSize
		sample.KVBlockSizeValid = true
	}
}
