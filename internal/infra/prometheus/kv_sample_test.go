package prometheus

import (
	"strings"
	"testing"
)

const vllmMetricTypeDeclarations = `
# TYPE vllm:cache_config_info gauge
# TYPE vllm:kv_cache_usage_perc gauge
# TYPE vllm:num_requests_running gauge
# TYPE vllm:num_requests_waiting gauge
# TYPE vllm:num_preemptions_total counter
# TYPE vllm:generation_tokens_total counter
`

func TestParseSampleUsesVLLMGroupAwareTokenCapacity(t *testing.T) {
	metrics := vllmMetricTypeDeclarations + `
vllm:cache_config_info{block_size="64",kv_cache_size_tokens="862437",num_gpu_blocks="9120"} 1
vllm:kv_cache_usage_perc 0.5
vllm:num_requests_running 4
`
	sample := ParseSample(metrics)
	if sample.BackendKind != "vllm" || !sample.KVTokenMetricsValid {
		t.Fatalf("sample=%#v want valid vllm token metrics", sample)
	}
	if sample.KVCapacityTokens != 862437 {
		t.Fatalf("capacity=%d want label capacity 862437", sample.KVCapacityTokens)
	}
	if sample.KVCapacityTokens == 9120*64 {
		t.Fatal("capacity was derived from num_gpu_blocks * block_size")
	}
}

func TestParseSampleCapturesOptionalRuntimeStartTime(t *testing.T) {
	withEpoch := ParseSample("process_start_time_seconds 1786639534.03\n")
	if !withEpoch.RuntimeStartTimeValid || withEpoch.RuntimeStartTime != 1786639534.03 {
		t.Fatalf("runtime start sample=%#v want valid process epoch", withEpoch)
	}
	for name, metrics := range map[string]string{
		"missing":   "# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running 0\n",
		"zero":      "process_start_time_seconds 0\n",
		"nonfinite": "process_start_time_seconds +Inf\n",
	} {
		t.Run(name, func(t *testing.T) {
			sample := ParseSample(metrics)
			if sample.RuntimeStartTimeValid || sample.RuntimeStartTime != 0 {
				t.Fatalf("invalid runtime start sample=%#v", sample)
			}
		})
	}
}

func TestParseSampleDeduplicatesSGLangRanksAndExcludesEvictable(t *testing.T) {
	metrics := `
# TYPE sglang:max_total_num_tokens gauge
# TYPE sglang:page_size gauge
# TYPE sglang:num_pages gauge
# TYPE sglang:kv_available_tokens gauge
# TYPE sglang:kv_evictable_tokens gauge
# TYPE sglang:kv_used_tokens gauge
sglang:max_total_num_tokens{tp_rank="0"} 1041408
sglang:max_total_num_tokens{tp_rank="1"} 1041408
sglang:page_size{tp_rank="0"} 16
sglang:page_size{tp_rank="1"} 16
sglang:num_pages{tp_rank="0"} 65088
sglang:num_pages{tp_rank="1"} 65088
sglang:kv_available_tokens{tp_rank="0"} 300000
sglang:kv_available_tokens{tp_rank="1"} 300000
sglang:kv_evictable_tokens{tp_rank="0"} 400000
sglang:kv_evictable_tokens{tp_rank="1"} 400000
sglang:kv_used_tokens{tp_rank="0"} 341408
sglang:kv_used_tokens{tp_rank="1"} 341408
sglang:token_usage{tp_rank="0"} 0.7119
sglang:token_usage{tp_rank="1"} 0.7119
`
	sample := ParseSample(metrics)
	if sample.BackendKind != "sglang" || !sample.KVTokenMetricsValid {
		t.Fatalf("sample=%#v want valid sglang token metrics", sample)
	}
	if sample.KVCapacityTokens != 1041408 {
		t.Fatalf("capacity=%d indicates TP ranks may have been summed", sample.KVCapacityTokens)
	}
	if sample.KVUsedTokens != 341408 {
		t.Fatalf("active=%d want capacity-available-evictable=341408", sample.KVUsedTokens)
	}
	if sample.KVEvictableTokens != 400000 {
		t.Fatalf("evictable=%d want 400000", sample.KVEvictableTokens)
	}
}

func TestParseSampleLeavesOldSGLangPercentageInvalidForTokenShadow(t *testing.T) {
	metrics := `
sglang:max_total_num_tokens 1041408
sglang:token_usage 0.5
`
	sample := ParseSample(metrics)
	if sample.BackendKind != "sglang" {
		t.Fatalf("kind=%q want sglang", sample.BackendKind)
	}
	if sample.KVTokenMetricsValid {
		t.Fatalf("legacy percentage-only sample must not be token-valid: %#v", sample)
	}
}

func TestParseSampleRejectsNonFiniteKVTokenGauges(t *testing.T) {
	for name, metrics := range map[string]string{
		"vllm usage": vllmMetricTypeDeclarations + `
vllm:cache_config_info{kv_cache_size_tokens="100000"} 1
vllm:kv_cache_usage_perc +Inf
`,
		"sglang used": `
sglang:max_total_num_tokens 100000
sglang:kv_used_tokens +Inf
`,
	} {
		t.Run(name, func(t *testing.T) {
			sample := ParseSample(metrics)
			if sample.KVTokenMetricsValid {
				t.Fatalf("non-finite token metric must fail closed: %#v", sample)
			}
		})
	}
}

func TestParseSampleMarksGenerationCounterPresence(t *testing.T) {
	withCounter := ParseSample("# TYPE vllm:generation_tokens_total counter\nvllm:generation_tokens_total 123\n")
	if !withCounter.GenerationValid || withCounter.Generation != 123 {
		t.Fatalf("generation counter sample = %#v, want valid 123", withCounter)
	}
	withoutCounter := ParseSample("# TYPE vllm:num_requests_running gauge\nvllm:num_requests_running 0\n")
	if withoutCounter.GenerationValid || withoutCounter.Generation != 0 {
		t.Fatalf("missing generation counter sample = %#v, want invalid zero", withoutCounter)
	}
}

func TestParseSampleMarksRequiredPredictiveCountersPresentAndExact(t *testing.T) {
	valid := ParseSample(vllmMetricTypeDeclarations + `
vllm:num_requests_running 2
vllm:num_requests_waiting 1
vllm:num_preemptions_total 3
`)
	if !valid.RunningValid || !valid.WaitingValid || !valid.PreemptionsValid || valid.Running != 2 || valid.Waiting != 1 || valid.Preemptions != 3 {
		t.Fatalf("valid predictive counters = %#v", valid)
	}

	invalid := ParseSample(vllmMetricTypeDeclarations + `
vllm:num_requests_running 0.5
vllm:num_requests_waiting +Inf
vllm:num_preemptions_total +Inf
`)
	if invalid.RunningValid || invalid.WaitingValid || invalid.PreemptionsValid || invalid.Running != 0 || invalid.Waiting != 0 || invalid.Preemptions != 0 {
		t.Fatalf("invalid predictive counters did not fail closed: %#v", invalid)
	}
}

func TestParseSampleRequiresVLLMMetricTypes(t *testing.T) {
	fixture := vllmMetricTypeDeclarations + `
vllm:cache_config_info{block_size="16",kv_cache_size_tokens="1000000",engine="0"} 1
vllm:kv_cache_usage_perc{model_name="vendor/model",engine="0"} 0.25
vllm:num_requests_running{model_name="vendor/model",engine="0"} 2
vllm:num_requests_waiting{model_name="vendor/model",engine="0"} 1
vllm:num_preemptions_total{model_name="vendor/model",engine="0"} 3
vllm:generation_tokens_total{model_name="vendor/model",engine="0"} 100
`
	allValid := func(metrics string) bool {
		sample := ParseSample(metrics)
		return sample.ModelNameValid && sample.KVTokenMetricsValid &&
			sample.RunningValid && sample.WaitingValid &&
			sample.PreemptionsValid && sample.GenerationValid
	}
	if !allValid(fixture) {
		t.Fatalf("upstream-typed vLLM fixture was rejected: %#v", ParseSample(fixture))
	}

	for _, metricType := range []struct {
		name string
		want string
		bad  string
	}{
		{name: "cache config", want: "# TYPE vllm:cache_config_info gauge\n", bad: "# TYPE vllm:cache_config_info counter\n"},
		{name: "KV usage", want: "# TYPE vllm:kv_cache_usage_perc gauge\n", bad: "# TYPE vllm:kv_cache_usage_perc counter\n"},
		{name: "running", want: "# TYPE vllm:num_requests_running gauge\n", bad: "# TYPE vllm:num_requests_running counter\n"},
		{name: "waiting", want: "# TYPE vllm:num_requests_waiting gauge\n", bad: "# TYPE vllm:num_requests_waiting counter\n"},
		{name: "preemption", want: "# TYPE vllm:num_preemptions_total counter\n", bad: "# TYPE vllm:num_preemptions_total gauge\n"},
		{name: "generation", want: "# TYPE vllm:generation_tokens_total counter\n", bad: "# TYPE vllm:generation_tokens_total gauge\n"},
	} {
		t.Run(metricType.name+" missing", func(t *testing.T) {
			if allValid(strings.Replace(fixture, metricType.want, "", 1)) {
				t.Fatalf("vLLM fixture without %s TYPE was accepted", metricType.name)
			}
		})
		t.Run(metricType.name+" wrong", func(t *testing.T) {
			if allValid(strings.Replace(fixture, metricType.want, metricType.bad, 1)) {
				t.Fatalf("vLLM fixture with wrong %s TYPE was accepted", metricType.name)
			}
		})
	}
}

func TestParseSampleAggregatesVLLMEnginesWithoutSGLangRules(t *testing.T) {
	metrics := vllmMetricTypeDeclarations + `
vllm:cache_config_info{block_size="16",kv_cache_size_tokens="1000000",engine="0"} 1
vllm:cache_config_info{block_size="16",kv_cache_size_tokens="1000000",engine="1"} 1
vllm:kv_cache_usage_perc{model_name="vendor/model",engine="0"} 0.2
vllm:kv_cache_usage_perc{model_name="vendor/model",engine="1"} 0.6
vllm:num_requests_running{model_name="vendor/model",engine="0"} 2
vllm:num_requests_running{model_name="vendor/model",engine="1"} 3
vllm:num_requests_waiting{model_name="vendor/model",engine="0"} 1
vllm:num_requests_waiting{model_name="vendor/model",engine="1"} 2
vllm:num_preemptions_total{model_name="vendor/model",engine="0"} 4
vllm:num_preemptions_total{model_name="vendor/model",engine="1"} 5
vllm:generation_tokens_total{model_name="vendor/model",engine="0"} 100
vllm:generation_tokens_total{model_name="vendor/model",engine="1"} 200
`
	sample := ParseSample(metrics)
	if sample.Running != 5 || sample.Waiting != 3 || sample.Preemptions != 9 || sample.Generation != 300 {
		t.Fatalf("vLLM engine counters were not summed: %#v", sample)
	}
	if !sample.KVTokenMetricsValid || sample.KVCapacityTokens != 1_000_000 || sample.KVUsedTokens != 600_000 {
		t.Fatalf("vLLM engine KV geometry/maximum aggregation changed: %#v", sample)
	}
}
