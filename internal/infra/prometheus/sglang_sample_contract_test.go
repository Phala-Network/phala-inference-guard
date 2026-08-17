package prometheus

import (
	"strings"
	"testing"
)

func TestParseSampleUsesCoherentSGLangAdmissionMetrics(t *testing.T) {
	metrics := sglangGaugeDeclarations + `
# TYPE sglang:num_retracted_requests_total counter
# TYPE sglang:realtime_tokens_total counter
sglang:max_total_num_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 1000000
sglang:page_size{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 16
sglang:num_pages{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 62500
sglang:kv_available_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 600000
sglang:kv_evictable_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 100000
sglang:kv_used_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 300000
sglang:num_running_reqs{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 3
sglang:num_running_reqs{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority="10"} 2
sglang:num_running_reqs{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority="20"} 1
sglang:num_queue_reqs{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 2
sglang:num_queue_reqs{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority="10"} 1
sglang:num_queue_reqs{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority="20"} 1
sglang:realtime_tokens_total{engine_type="unified",mode="prefill_compute",model_name="meta/test-model",tp_rank="0",priority=""} 500
sglang:realtime_tokens_total{engine_type="unified",mode="prefill_cache",model_name="meta/test-model",tp_rank="0",priority=""} 700
sglang:realtime_tokens_total{engine_type="unified",mode="decode",model_name="meta/test-model",tp_rank="0",priority=""} 25
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

func TestParseSampleAcceptsLiveShapedColdSGLangRetractionCounter(t *testing.T) {
	metrics := coherentSGLangFixtureWithoutRetractionDeclaration() + `
# TYPE sglang:num_retracted_reqs gauge
sglang:num_retracted_reqs{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 0
`
	sample := ParseSample(metrics)
	if !sample.PreemptionsValid || sample.Preemptions != 0 || !sample.GenerationValid ||
		!sample.ModelNameValid || !sample.KVTokenMetricsValid {
		t.Fatalf("live-shaped cold SGLang retraction counter self-locked startup: %#v", sample)
	}
}

func TestParseSampleDeduplicatesMonotonicSGLangRetractionCounterAcrossTPRanks(t *testing.T) {
	metrics := coherentSGLangFixture() + `
sglang:num_retracted_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 7
sglang:num_paused_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 5
sglang:num_retracted_requests_total{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 2
sglang:num_retracted_requests_total{engine_type="unified",model_name="meta/test-model",tp_rank="1",priority=""} 2
`
	sample := ParseSample(metrics)
	if !sample.PreemptionsValid || sample.Preemptions != 2 {
		t.Fatalf("SGLang preemption must deduplicate TP-rank retraction counters: %#v", sample)
	}
}

func TestParseSampleRequiresRealtimeDecodeCounterType(t *testing.T) {
	metrics := coherentSGLangFixtureWithoutCounterDeclarations()
	sample := ParseSample(metrics)
	if sample.GenerationValid || sample.Generation != 0 {
		t.Fatalf("untyped SGLang realtime gauge fabricated a generation counter: %#v", sample)
	}
}

func TestParseSampleTreatsRegisteredColdSGLangDecodeCounterAsZero(t *testing.T) {
	metrics := coherentSGLangFixtureWithoutCounterDeclarations() +
		"# TYPE sglang:realtime_tokens_total counter\n"
	metrics = strings.ReplaceAll(metrics,
		"sglang:realtime_tokens_total{engine_type=\"unified\",mode=\"decode\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"\"} 100\n",
		"sglang:realtime_tokens_total{engine_type=\"unified\",mode=\"prefill_compute\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"\"} 200\n")
	sample := ParseSample(metrics)
	if !sample.ModelNameValid || !sample.GenerationValid || sample.Generation != 0 {
		t.Fatalf("prefill-only SGLang counter did not preserve exact decode zero: %#v", sample)
	}
}

func TestParseSampleAcceptsRegisteredColdSGLangDynamicMetricsAsIdle(t *testing.T) {
	metrics := `
# TYPE sglang:max_total_num_tokens gauge
# TYPE sglang:page_size gauge
# TYPE sglang:num_pages gauge
# TYPE sglang:kv_available_tokens gauge
# TYPE sglang:kv_evictable_tokens gauge
# TYPE sglang:kv_used_tokens gauge
# TYPE sglang:num_running_reqs gauge
# TYPE sglang:num_queue_reqs gauge
# TYPE sglang:realtime_tokens_total counter
# TYPE sglang:num_retracted_requests_total counter
sglang:max_total_num_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 1000000
sglang:page_size{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 16
sglang:num_pages{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 62500
`
	sample := ParseSample(metrics)
	if !sample.ModelNameValid || !sample.KVTokenMetricsValid || !sample.KVBlockSizeValid ||
		!sample.RunningValid || !sample.WaitingValid || !sample.GenerationValid || !sample.PreemptionsValid ||
		sample.KVCapacityTokens != 1_000_000 || sample.KVAvailableTokens != 1_000_000 ||
		sample.KVUsedTokens != 0 || sample.Running != 0 || sample.Waiting != 0 || sample.Generation != 0 ||
		sample.Preemptions != 0 {
		t.Fatalf("registered cold SGLang metrics did not produce coherent idle state: %#v", sample)
	}
}

func TestParseSampleAcceptsUnmaterializedMultiprocessSGLangMetricsAsIdle(t *testing.T) {
	metrics := `
# TYPE sglang:max_total_num_tokens gauge
# TYPE sglang:page_size gauge
# TYPE sglang:num_pages gauge
sglang:max_total_num_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 1000000
sglang:page_size{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 16
sglang:num_pages{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 62500
`
	sample := ParseSample(metrics)
	if !sample.ModelNameValid || !sample.KVTokenMetricsValid || !sample.KVBlockSizeValid ||
		!sample.RunningValid || !sample.WaitingValid || !sample.GenerationValid || !sample.PreemptionsValid ||
		sample.KVCapacityTokens != 1_000_000 || sample.KVAvailableTokens != 1_000_000 ||
		sample.KVUsedTokens != 0 || sample.Running != 0 || sample.Waiting != 0 || sample.Generation != 0 ||
		sample.Preemptions != 0 {
		t.Fatalf("unmaterialized multiprocess metrics did not produce coherent idle state: %#v", sample)
	}
}

func TestParseSampleRejectsMultipleSGLangDPReplicas(t *testing.T) {
	metrics := coherentSGLangFixture() + `
sglang:num_running_reqs{engine_type="unified",model_name="meta/test-model",dp_rank="1",tp_rank="0",priority=""} 1
`
	sample := ParseSample(metrics)
	if sample.ModelNameValid {
		t.Fatalf("multi-DP SGLang topology was accepted as one admission capacity: %#v", sample)
	}
}

func TestParseSampleRejectsIncoherentSGLangAbsoluteKVAccounting(t *testing.T) {
	metrics := coherentSGLangFixture() + `
sglang:kv_used_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="1",priority=""} 49999
`
	sample := ParseSample(metrics)
	if sample.KVTokenMetricsValid {
		t.Fatalf("incoherent SGLang absolute KV gauges were accepted: %#v", sample)
	}
}

func TestParseSampleAcceptsProtectedSGLangKVGap(t *testing.T) {
	metrics := strings.ReplaceAll(
		coherentSGLangFixture(),
		"sglang:kv_used_tokens{engine_type=\"unified\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"\"} 50000\n",
		"sglang:kv_used_tokens{engine_type=\"unified\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"\"} 40000\n",
	)
	sample := ParseSample(metrics)
	if !sample.KVTokenMetricsValid || sample.KVUsedTokens != 50_000 || sample.KVAvailableTokens != 900_000 {
		t.Fatalf("protected SGLang KV gap was rejected or excluded from non-reclaimable KV: %#v", sample)
	}
}

func TestParseSampleRejectsSGLangActiveKVAboveDerivedNonReclaimableKV(t *testing.T) {
	metrics := strings.ReplaceAll(
		coherentSGLangFixture(),
		"sglang:kv_used_tokens{engine_type=\"unified\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"\"} 50000\n",
		"sglang:kv_used_tokens{engine_type=\"unified\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"\"} 50001\n",
	)
	sample := ParseSample(metrics)
	if sample.KVTokenMetricsValid {
		t.Fatalf("SGLang active KV above derived non-reclaimable KV was accepted: %#v", sample)
	}
}

func TestParseSampleRejectsLegacySGLangRetractionGaugesAsPreemptionCounter(t *testing.T) {
	metrics := coherentSGLangFixtureWithoutRetractionDeclaration() + `
# TYPE sglang:num_retracted_reqs gauge
sglang:num_retracted_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 7
sglang:num_paused_reqs{model_name="meta/test-model",tp_rank="0",priority=""} 5
`
	sample := ParseSample(metrics)
	if sample.PreemptionsValid || sample.Preemptions != 0 {
		t.Fatalf("legacy SGLang gauges fabricated preemption counter: %#v", sample)
	}
}

func TestParseSampleRejectsSGLangPriorityChildrenWithoutTotals(t *testing.T) {
	metrics := strings.ReplaceAll(coherentSGLangFixture(),
		"sglang:num_running_reqs{engine_type=\"unified\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"\"} 0\n",
		"sglang:num_running_reqs{engine_type=\"unified\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"10\"} 1\n")
	metrics = strings.ReplaceAll(metrics,
		"sglang:realtime_tokens_total{engine_type=\"unified\",mode=\"decode\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"\"} 100\n",
		"sglang:realtime_tokens_total{engine_type=\"unified\",mode=\"decode\",model_name=\"meta/test-model\",tp_rank=\"0\",priority=\"10\"} 100\n")
	sample := ParseSample(metrics)
	if sample.RunningValid || sample.GenerationValid {
		t.Fatalf("SGLang priority children were fabricated into scheduler totals: %#v", sample)
	}
}

func TestParseSampleRequiresSGLangMetricTypesOnceSamplesMaterialize(t *testing.T) {
	metrics := strings.ReplaceAll(coherentSGLangFixture(),
		"# TYPE sglang:kv_used_tokens gauge\n",
		"# TYPE sglang:kv_used_tokens counter\n")
	sample := ParseSample(metrics)
	if sample.KVTokenMetricsValid {
		t.Fatalf("wrongly typed SGLang KV sample was accepted: %#v", sample)
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
	return "# TYPE sglang:num_retracted_requests_total counter\n# TYPE sglang:realtime_tokens_total counter\n" +
		coherentSGLangFixtureWithoutCounterDeclarations()
}

func coherentSGLangFixtureWithoutRetractionDeclaration() string {
	return "# TYPE sglang:realtime_tokens_total counter\n" + coherentSGLangFixtureWithoutCounterDeclarations()
}

func coherentSGLangFixtureWithoutCounterDeclarations() string {
	return sglangGaugeDeclarations + `
sglang:max_total_num_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 1000000
sglang:page_size{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 16
sglang:num_pages{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 62500
sglang:kv_available_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 900000
sglang:kv_evictable_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 50000
sglang:kv_used_tokens{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 50000
sglang:num_running_reqs{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 0
sglang:num_queue_reqs{engine_type="unified",model_name="meta/test-model",tp_rank="0",priority=""} 0
sglang:realtime_tokens_total{engine_type="unified",mode="decode",model_name="meta/test-model",tp_rank="0",priority=""} 100
`
}

const sglangGaugeDeclarations = `
# TYPE sglang:max_total_num_tokens gauge
# TYPE sglang:page_size gauge
# TYPE sglang:num_pages gauge
# TYPE sglang:kv_available_tokens gauge
# TYPE sglang:kv_evictable_tokens gauge
# TYPE sglang:kv_used_tokens gauge
# TYPE sglang:num_running_reqs gauge
# TYPE sglang:num_queue_reqs gauge
`
