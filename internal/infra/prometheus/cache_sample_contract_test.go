package prometheus

import "testing"

func TestParseSampleUsesSGLangEffectivePrefillCountersWithoutSummingTPRanks(t *testing.T) {
	metrics := coherentSGLangFixture() + `
# TYPE sglang:prefill_effective_tokens_total counter
sglang:prefill_effective_tokens_total{engine_type="unified",mode="input",model_name="meta/test-model",tp_rank="0",priority=""} 400
sglang:prefill_effective_tokens_total{engine_type="unified",mode="device_hit",model_name="meta/test-model",tp_rank="0",priority=""} 500
sglang:prefill_effective_tokens_total{engine_type="unified",mode="host_hit",model_name="meta/test-model",tp_rank="0",priority=""} 75
sglang:prefill_effective_tokens_total{engine_type="unified",mode="storage_hit",model_name="meta/test-model",tp_rank="0",priority=""} 25
sglang:prefill_effective_tokens_total{engine_type="unified",mode="input",model_name="meta/test-model",tp_rank="1",priority=""} 0
sglang:prefill_effective_tokens_total{engine_type="unified",mode="device_hit",model_name="meta/test-model",tp_rank="1",priority=""} 0
sglang:prefill_effective_tokens_total{engine_type="unified",mode="host_hit",model_name="meta/test-model",tp_rank="1",priority=""} 0
sglang:prefill_effective_tokens_total{engine_type="unified",mode="storage_hit",model_name="meta/test-model",tp_rank="1",priority=""} 0
`
	sample := ParseSample(metrics)
	if !sample.CacheTokensValid || sample.CacheQueryTokens != 1_000 || sample.CacheHitTokens != 600 {
		t.Fatalf("SGLang cache counters=%#v", sample)
	}
}

func TestParseSampleSumsVLLMCacheCountersAcrossEngines(t *testing.T) {
	metrics := vllmMetricTypeDeclarations + `
# TYPE vllm:prefix_cache_queries_total counter
# TYPE vllm:prefix_cache_hits_total counter
vllm:cache_config_info{block_size="16",kv_cache_size_tokens="1000000",engine="0"} 1
vllm:cache_config_info{block_size="16",kv_cache_size_tokens="1000000",engine="1"} 1
vllm:kv_cache_usage_perc{model_name="vendor/model",engine="0"} 0.2
vllm:kv_cache_usage_perc{model_name="vendor/model",engine="1"} 0.3
vllm:num_requests_running{model_name="vendor/model",engine="0"} 1
vllm:num_requests_running{model_name="vendor/model",engine="1"} 2
vllm:num_requests_waiting{model_name="vendor/model",engine="0"} 0
vllm:num_requests_waiting{model_name="vendor/model",engine="1"} 0
vllm:num_preemptions_total{model_name="vendor/model",engine="0"} 0
vllm:num_preemptions_total{model_name="vendor/model",engine="1"} 0
vllm:generation_tokens_total{model_name="vendor/model",engine="0"} 100
vllm:generation_tokens_total{model_name="vendor/model",engine="1"} 200
vllm:prefix_cache_queries_total{model_name="vendor/model",engine="0"} 1000
vllm:prefix_cache_queries_total{model_name="vendor/model",engine="1"} 2000
vllm:prefix_cache_hits_total{model_name="vendor/model",engine="0"} 250
vllm:prefix_cache_hits_total{model_name="vendor/model",engine="1"} 1000
`
	sample := ParseSample(metrics)
	if !sample.CacheTokensValid || sample.CacheQueryTokens != 3_000 || sample.CacheHitTokens != 1_250 {
		t.Fatalf("vLLM cache counters=%#v", sample)
	}
}

func TestParseSampleTreatsInvalidOptionalCacheContractsAsColdFallback(t *testing.T) {
	for name, metrics := range map[string]string{
		"SGLang wrong type": coherentSGLangFixture() + `
# TYPE sglang:prefill_effective_tokens_total gauge
sglang:prefill_effective_tokens_total{engine_type="unified",mode="input",model_name="meta/test-model",tp_rank="0",priority=""} 1000
sglang:prefill_effective_tokens_total{engine_type="unified",mode="device_hit",model_name="meta/test-model",tp_rank="0",priority=""} 500
`,
		"SGLang priority child": coherentSGLangFixture() + `
# TYPE sglang:prefill_effective_tokens_total counter
sglang:prefill_effective_tokens_total{engine_type="unified",mode="input",model_name="meta/test-model",tp_rank="0",priority="10"} 1000
sglang:prefill_effective_tokens_total{engine_type="unified",mode="device_hit",model_name="meta/test-model",tp_rank="0",priority="10"} 500
`,
		"vLLM wrong type": vllmMetricTypeDeclarations + `
# TYPE vllm:prefix_cache_queries_total gauge
# TYPE vllm:prefix_cache_hits_total counter
vllm:prefix_cache_queries_total{model_name="vendor/model",engine="0"} 1000
vllm:prefix_cache_hits_total{model_name="vendor/model",engine="0"} 500
`,
	} {
		t.Run(name, func(t *testing.T) {
			sample := ParseSample(metrics)
			if sample.CacheTokensValid || sample.CacheQueryTokens != 0 || sample.CacheHitTokens != 0 {
				t.Fatalf("invalid optional cache contract did not fall back cold: %#v", sample)
			}
		})
	}
}
