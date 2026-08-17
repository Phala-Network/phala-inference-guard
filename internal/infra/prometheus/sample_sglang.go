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

func parseSGLangSample(index metricIndex) telemetry.Sample {
	totalPriority := func(labels map[string]string) bool {
		return labels["priority"] == ""
	}
	runningValue, runningPresent := index.maximum("sglang:num_running_reqs", totalPriority)
	waitingValue, waitingPresent := index.maximum("sglang:num_queue_reqs", totalPriority)
	generationValue, generationPresent := index.maximum(sglangRealtimeTokenCounter, func(labels map[string]string) bool {
		return labels["mode"] == "decode" && labels["priority"] == ""
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
	running, runningValid := parseSGLangRequestGauge(index, "sglang:num_running_reqs", runningValue, runningPresent)
	waiting, waitingValid := parseSGLangRequestGauge(index, "sglang:num_queue_reqs", waitingValue, waitingPresent)
	if runningPresent != waitingPresent {
		runningValid = false
		waitingValid = false
	}
	generation := uint64(0)
	generationValid := !index.has(sglangRealtimeTokenCounter)
	decodeSamplePresent, decodeLabelsValid := index.hasMatchingSamples(
		sglangRealtimeTokenCounter,
		func(labels map[string]string) bool { return labels["mode"] == "decode" },
	)
	if generationPresent {
		generation, generationValid = exactNonNegativeMetricUint64(
			generationValue,
			decodeLabelsValid && index.declaredType(sglangRealtimeTokenCounter, "counter"),
		)
	} else if !decodeLabelsValid || decodeSamplePresent {
		// Decode samples exist, but none is the unified scheduler's total.
		generationValid = false
	} else if index.has(sglangRealtimeTokenCounter) {
		// Prefill-only children materialize the counter family before its first
		// decode child. The absent decode child is still an exact zero.
		generationValid = index.declaredType(sglangRealtimeTokenCounter, "counter")
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
	}
	adaptSGLangKV(index, &sample)
	adaptSGLangCache(index, modelName, &sample)
	return sample
}

func adaptSGLangCache(index metricIndex, modelName string, sample *telemetry.Sample) {
	const metricName = "sglang:prefill_effective_tokens_total"
	if sample == nil || modelName == "" || !index.declaredType(metricName, "counter") ||
		!index.hasSamples(metricName) {
		return
	}
	modes := map[string]uint64{
		"input":       0,
		"device_hit":  0,
		"host_hit":    0,
		"storage_hit": 0,
	}
	present := make(map[string]bool, len(modes))
	dpRank := ""
	dpRankSet := false
	for _, item := range index.samples[metricName] {
		if !item.labelsValid || item.labels["model_name"] != modelName ||
			item.labels["engine_type"] != "unified" || item.labels["priority"] != "" {
			return
		}
		candidateDP := item.labels["dp_rank"]
		if dpRankSet && candidateDP != dpRank {
			return
		}
		dpRank = candidateDP
		dpRankSet = true
		mode := item.labels["mode"]
		if _, ok := modes[mode]; !ok {
			return
		}
		value, valid := exactNonNegativeMetricUint64(item.value, item.valueValid)
		if !valid {
			return
		}
		if !present[mode] || value > modes[mode] {
			modes[mode] = value
		}
		present[mode] = true
	}
	for mode := range modes {
		if !present[mode] {
			return
		}
	}
	hits := modes["device_hit"]
	for _, mode := range []string{"host_hit", "storage_hit"} {
		if hits > (1<<53)-modes[mode] {
			return
		}
		hits += modes[mode]
	}
	if modes["input"] > (1<<53)-hits {
		return
	}
	sample.CacheQueryTokens = modes["input"] + hits
	sample.CacheHitTokens = hits
	sample.CacheTokensValid = true
}

func parseSGLangRequestGauge(index metricIndex, name string, value float64, present bool) (int, bool) {
	if present {
		return exactNonNegativeMetricInt(value, index.declaredType(name, "gauge"))
	}
	if index.hasSamples(name) {
		// A priority subset without priority="" is not a scheduler total.
		return 0, false
	}
	if index.has(name) && !index.declaredType(name, "gauge") {
		return 0, false
	}
	// prometheus_client multiprocess mode does not expose HELP/TYPE or a sample
	// before the first labeled child is written. An entirely unmaterialized
	// request gauge is therefore the exact cold-start zero.
	return 0, true
}

func parseSGLangRetractions(index metricIndex) (uint64, bool) {
	totalPriority := func(labels map[string]string) bool {
		return labels["priority"] == ""
	}
	value, present := index.maximum(sglangRetractionCounter, totalPriority)
	if present {
		return exactNonNegativeMetricUint64(value, index.declaredType(sglangRetractionCounter, "counter"))
	}
	if index.hasSamples(sglangRetractionCounter) {
		// Per-priority children without the unified total cannot be aggregated
		// safely, especially when TP ranks also duplicate each child.
		return 0, false
	}
	if index.has(sglangRetractionCounter) {
		return 0, index.declaredType(sglangRetractionCounter, "counter")
	}

	legacyValue, legacyPresent := index.maximum("sglang:num_retracted_reqs", totalPriority)
	if legacyPresent {
		legacy, legacyValid := exactNonNegativeMetricInt(
			legacyValue,
			index.declaredType("sglang:num_retracted_reqs", "gauge"),
		)
		// A non-zero resettable gauge proves that a retraction occurred but
		// cannot be converted into a monotonic total. Keep the sample invalid
		// until the real counter materializes instead of fabricating a delta.
		return 0, legacyValid && legacy == 0
	}
	if index.hasSamples("sglang:num_retracted_reqs") ||
		(index.has("sglang:num_retracted_reqs") && !index.declaredType("sglang:num_retracted_reqs", "gauge")) {
		return 0, false
	}
	// The current SGLang counter is a labeled prometheus_client multiprocess
	// metric. Before its first increment, even HELP/TYPE is absent from the
	// scrape. With a coherent modern SGLang admission schema, that absence is
	// the exact cold-start zero.
	return 0, true
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
	allKVAbsent := !availablePresent && !evictablePresent && !directUsedPresent
	registeredColdKV := allKVAbsent &&
		validAbsentSGLangGauge(index, "sglang:kv_available_tokens") &&
		validAbsentSGLangGauge(index, "sglang:kv_evictable_tokens") &&
		validAbsentSGLangGauge(index, "sglang:kv_used_tokens")
	if registeredColdKV && capacityValid {
		available = capacity
		availableValid = true
		evictableValid = true
		directUsedValid = true
	}
	if !index.declaredType("sglang:max_total_num_tokens", "gauge") ||
		!index.declaredType("sglang:page_size", "gauge") ||
		!index.declaredType("sglang:num_pages", "gauge") ||
		(!allKVAbsent && (!index.declaredType("sglang:kv_available_tokens", "gauge") ||
			!index.declaredType("sglang:kv_evictable_tokens", "gauge") ||
			!index.declaredType("sglang:kv_used_tokens", "gauge"))) ||
		!capacityValid || capacity <= 0 || !availableValid || !evictableValid || !directUsedValid ||
		available > capacity || evictable > capacity-available {
		return
	}
	used := capacity - available - evictable
	// SGLang reports directUsed as active locked KV only. The derived value also
	// includes protected or session-held slots, so directUsed is a lower bound.
	// A larger direct value proves the scrape is torn in an unsafe direction.
	if directUsed > used {
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

func validAbsentSGLangGauge(index metricIndex, name string) bool {
	return !index.has(name) || index.declaredType(name, "gauge")
}
