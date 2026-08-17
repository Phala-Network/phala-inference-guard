package prometheus

import "testing"

func TestParseSampleUsesVLLMGroupAwareTokenCapacity(t *testing.T) {
	metrics := `
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
		"missing":   "vllm:num_requests_running 0\n",
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
sglang:max_total_num_tokens{tp_rank="0"} 1041408
sglang:max_total_num_tokens{tp_rank="1"} 1041408
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
		"vllm usage": `
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
	withCounter := ParseSample("vllm:generation_tokens_total 123\n")
	if !withCounter.GenerationValid || withCounter.Generation != 123 {
		t.Fatalf("generation counter sample = %#v, want valid 123", withCounter)
	}
	withoutCounter := ParseSample("vllm:num_requests_running 0\n")
	if withoutCounter.GenerationValid || withoutCounter.Generation != 0 {
		t.Fatalf("missing generation counter sample = %#v, want invalid zero", withoutCounter)
	}
}

func TestParseSampleMarksRequiredPredictiveCountersPresentAndExact(t *testing.T) {
	valid := ParseSample(`
vllm:num_requests_running 2
vllm:num_requests_waiting 1
vllm:num_preemptions_total 3
`)
	if !valid.RunningValid || !valid.WaitingValid || !valid.PreemptionsValid || valid.Running != 2 || valid.Waiting != 1 || valid.Preemptions != 3 {
		t.Fatalf("valid predictive counters = %#v", valid)
	}

	invalid := ParseSample(`
vllm:num_requests_running 0.5
vllm:num_requests_waiting +Inf
vllm:num_preemptions_total +Inf
`)
	if invalid.RunningValid || invalid.WaitingValid || invalid.PreemptionsValid || invalid.Running != 0 || invalid.Waiting != 0 || invalid.Preemptions != 0 {
		t.Fatalf("invalid predictive counters did not fail closed: %#v", invalid)
	}
}
