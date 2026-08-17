package prometheus

import "testing"

func TestParseSampleUsesCoherentSGLangAdmissionMetrics(t *testing.T) {
	metrics := `
# TYPE sglang:num_retracted_requests_total counter
sglang:max_total_num_tokens{model_name="meta/test-model",tp_rank="0",priority=""} 1000000
sglang:page_size{model_name="meta/test-model",tp_rank="0",priority=""} 16
sglang:num_pages{model_name="meta/test-model",tp_rank="0",priority=""} 62500
sglang:kv_available_tokens{model_name="meta/test-model",tp_rank="0",priority=""} 600000
sglang:kv_evictable_tokens{model_name="meta/test-model",tp_rank="0",priority=""} 100000
sglang:kv_used_tokens{model_name="meta/test-model",tp_rank="0",priority=""} 250000
sglang:num_running_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 3
sglang:num_running_reqs{model_name="meta/test-model",tp_rank="0",priority="10"} 2
sglang:num_running_reqs{model_name="meta/test-model",tp_rank="0",priority="20"} 1
sglang:num_queue_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 2
sglang:num_queue_reqs{model_name="meta/test-model",tp_rank="0",priority="10"} 1
sglang:num_queue_reqs{model_name="meta/test-model",tp_rank="0",priority="20"} 1
sglang:realtime_tokens_total{mode="prefill_compute",model_name="meta/test-model",tp_rank="0",priority=""} 500
sglang:realtime_tokens_total{mode="prefill_cache",model_name="meta/test-model",tp_rank="0",priority=""} 700
sglang:realtime_tokens_total{mode="decode",model_name="meta/test-model",tp_rank="0",priority=""} 25
sglang:generation_tokens_total{is_streaming="false",model_name="meta/test-model",priority=""} 10
process_start_time_seconds 1234
`
	sample := ParseSample(metrics)
	if sample.BackendKind != "sglang" || sample.ModelName != "meta/test-model" || !sample.ModelNameValid {
		t.Fatalf("SGLang identity sample=%#v", sample)
	}
	if !sample.KVTokenMetricsValid || !sample.KVBlockSizeValid ||
		sample.KVCapacityTokens != 1_000_000 || sample.KVBlockSize != 16 ||
		sample.KVUsedTokens != 300_000 || sample.KVAvailableTokens != 600_000 ||
		sample.KVEvictableTokens != 100_000 {
		t.Fatalf("SGLang KV sample=%#v", sample)
	}
	if !sample.RunningValid || sample.Running != 3 || !sample.WaitingValid || sample.Waiting != 2 {
		t.Fatalf("SGLang request counts sample=%#v", sample)
	}
	if !sample.GenerationValid || sample.Generation != 25 {
		t.Fatalf("SGLang generation must use realtime decode only: %#v", sample)
	}
	if !sample.PreemptionsValid || sample.Preemptions != 0 {
		t.Fatalf("registered zero SGLang retraction counter sample=%#v", sample)
	}
}

func TestParseSampleUsesOnlyMonotonicSGLangRetractionCounter(t *testing.T) {
	metrics := coherentSGLangFixture() + `
sglang:num_retracted_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 7
sglang:num_paused_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 5
sglang:num_retracted_requests_total{model_name="meta/test-model",tp_rank="0",priority=""} 2
sglang:num_retracted_requests_total{model_name="meta/test-model",tp_rank="0",priority="batch"} 1
`
	sample := ParseSample(metrics)
	if !sample.PreemptionsValid || sample.Preemptions != 3 {
		t.Fatalf("SGLang preemption must use summed retraction counter only: %#v", sample)
	}
}

func TestParseSampleRejectsLegacySGLangRetractionGaugesAsPreemptionCounter(t *testing.T) {
	metrics := coherentSGLangFixtureWithoutRetractionDeclaration() + `
sglang:num_retracted_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 7
sglang:num_paused_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 5
`
	sample := ParseSample(metrics)
	if sample.PreemptionsValid || sample.Preemptions != 0 {
		t.Fatalf("legacy SGLang gauges fabricated preemption counter: %#v", sample)
	}
}

func TestParseSampleRejectsInvalidSGLangPageGeometry(t *testing.T) {
	metrics := coherentSGLangFixture() + `
sglang:page_size{model_name="meta/test-model",tp_rank="0",priority="override"} 8
`
	sample := ParseSample(metrics)
	if sample.KVTokenMetricsValid || sample.KVBlockSizeValid {
		t.Fatalf("ambiguous SGLang page geometry was accepted: %#v", sample)
	}
}

func TestParseSampleRejectsMixedBackendSignatures(t *testing.T) {
	metrics := coherentSGLangFixture() + `
vllm:cache_config_info{block_size="16",kv_cache_size_tokens="1000000"} 1
vllm:kv_cache_usage_perc 0
vllm:num_requests_running{model_name="meta/test-model",engine="0"} 0
vllm:num_requests_waiting{model_name="meta/test-model",engine="0"} 0
vllm:num_preemptions_total{model_name="meta/test-model",engine="0"} 0
vllm:generation_tokens_total{model_name="meta/test-model",engine="0"} 0
`
	sample := ParseSample(metrics)
	if sample.BackendKind != "" || sample.ModelNameValid || sample.KVTokenMetricsValid ||
		sample.RunningValid || sample.WaitingValid || sample.GenerationValid || sample.PreemptionsValid {
		t.Fatalf("mixed backend scrape was accepted: %#v", sample)
	}
}

func coherentSGLangFixture() string {
	return "# TYPE sglang:num_retracted_requests_total counter\n" + coherentSGLangFixtureWithoutRetractionDeclaration()
}

func coherentSGLangFixtureWithoutRetractionDeclaration() string {
	return `
sglang:max_total_num_tokens{model_name="meta/test-model",tp_rank="0",priority=""} 1000000
sglang:page_size{model_name="meta/test-model",tp_rank="0",priority=""} 16
sglang:num_pages{model_name="meta/test-model",tp_rank="0",priority=""} 62500
sglang:kv_available_tokens{model_name="meta/test-model",tp_rank="0",priority=""} 900000
sglang:kv_evictable_tokens{model_name="meta/test-model",tp_rank="0",priority=""} 50000
sglang:kv_used_tokens{model_name="meta/test-model",tp_rank="0",priority=""} 50000
sglang:num_running_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 0
sglang:num_queue_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 0
sglang:realtime_tokens_total{mode="decode",model_name="meta/test-model",tp_rank="0",priority=""} 100
`
}
