package prometheus

import (
	"math"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

var indexedAdmissionMetrics = metricNameSet(
	"process_start_time_seconds",
	"vllm:cache_config_info",
	"vllm:kv_cache_usage_perc",
	"vllm:num_requests_running",
	"vllm:num_requests_waiting",
	"vllm:num_preemptions_total",
	"vllm:generation_tokens_total",
	"vllm:prefix_cache_queries_total",
	"vllm:prefix_cache_hits_total",
	"sglang:max_total_num_tokens",
	"sglang:page_size",
	"sglang:num_pages",
	"sglang:kv_available_tokens",
	"sglang:kv_evictable_tokens",
	"sglang:kv_used_tokens",
	"sglang:num_running_reqs",
	"sglang:num_queue_reqs",
	"sglang:realtime_tokens_total",
	"sglang:prefill_effective_tokens_total",
	"sglang:num_retracted_requests_total",
	"sglang:num_retracted_reqs",
)

var vllmAdmissionSignatures = []string{
	"vllm:cache_config_info",
	"vllm:kv_cache_usage_perc",
	"vllm:num_requests_running",
	"vllm:num_requests_waiting",
	"vllm:num_preemptions_total",
	"vllm:generation_tokens_total",
}

var sglangAdmissionSignatures = []string{
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
	"sglang:num_retracted_reqs",
}

func ParseSample(metricsText string) telemetry.Sample {
	index := newMetricIndex(metricsText, indexedAdmissionMetrics)
	hasVLLM := index.hasAny(vllmAdmissionSignatures...)
	hasSGLang := index.hasAny(sglangAdmissionSignatures...)

	var sample telemetry.Sample
	switch {
	case hasVLLM && hasSGLang:
		// A direct backend scrape cannot coherently describe two serving
		// frameworks. Return no admission fields rather than mixing families.
	case hasVLLM:
		sample = parseVLLMSample(index)
	case hasSGLang:
		sample = parseSGLangSample(index)
	}
	applyRuntimeEpoch(index, &sample)
	return sample
}

func applyRuntimeEpoch(index metricIndex, sample *telemetry.Sample) {
	if sample == nil {
		return
	}
	runtimeStartTime, ok := index.maximum("process_start_time_seconds", nil)
	if ok && finitePositive(runtimeStartTime) {
		sample.RuntimeStartTime = runtimeStartTime
		sample.RuntimeStartTimeValid = true
	}
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

func exactNonNegativeMetricInt64(value float64, present bool) (int64, bool) {
	const maximumExactFloatInteger = float64(1 << 53)
	if !present || !finiteNonNegative(value) || math.Trunc(value) != value || value > maximumExactFloatInteger {
		return 0, false
	}
	return int64(value), true
}

func exactNonNegativeMetricUint64(value float64, present bool) (uint64, bool) {
	const maximumExactFloatInteger = float64(1 << 53)
	if !present || !finiteNonNegative(value) || math.Trunc(value) != value || value > maximumExactFloatInteger {
		return 0, false
	}
	return uint64(value), true
}

func metricNameSet(names ...string) map[string]struct{} {
	values := make(map[string]struct{}, len(names))
	for _, name := range names {
		values[strings.TrimSpace(name)] = struct{}{}
	}
	return values
}
