package prometheus

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func attentionDPFixture(tpSize, dpSize, ranks int, includeDP bool) string {
	var result strings.Builder
	for rank := 0; rank < ranks; rank++ {
		dp := rank / (tpSize / dpSize)
		label := fmt.Sprintf("tp_rank=\"%d\"", rank)
		if includeDP {
			label += fmt.Sprintf(",dp_rank=\"%d\"", dp)
		}
		text := strings.ReplaceAll(coherentSGLangFixture(), "tp_rank=\"0\"", label)
		for _, line := range strings.Split(text, "\n") {
			value := ""
			switch {
			case strings.HasPrefix(line, "sglang:num_running_reqs{"):
				value = fmt.Sprint(dp + 1)
			case strings.HasPrefix(line, "sglang:num_queue_reqs{"):
				value = fmt.Sprint(dp)
			case strings.HasPrefix(line, "sglang:realtime_tokens_total{"):
				value = fmt.Sprint(100 * (dp + 1))
			}
			if value != "" {
				line = line[:strings.LastIndexByte(line, ' ')+1] + value
			}
			result.WriteString(line + "\n")
		}
		result.WriteString("# TYPE sglang:prefill_effective_tokens_total counter\n")
		for mode, value := range map[string]int{"input": 20, "device_hit": 30, "host_hit": 10, "storage_hit": 5} {
			result.WriteString(fmt.Sprintf("sglang:prefill_effective_tokens_total{engine_type=\"unified\",model_name=\"meta/test-model\",%s,priority=\"\",mode=\"%s\"} %d\n", label, mode, value))
		}
	}
	return result.String()
}

func TestSGLangAttentionDPSumsReplicasAndDeduplicatesTP(t *testing.T) {
	metrics := attentionDPFixture(4, 2, 4, true)
	if ParseSample(metrics).ModelNameValid {
		t.Fatal("legacy contract unexpectedly accepts multiple DP replicas")
	}
	sample := ParseSampleWithSGLangTopology(metrics, SGLangTopology{TPSize: 4, DPSize: 2})
	if !sample.ModelNameValid || !sample.RunningValid || !sample.WaitingValid ||
		!sample.GenerationValid || !sample.PreemptionsValid ||
		sample.Running != 3 || sample.Waiting != 1 || sample.Generation != 300 {
		t.Fatalf("independent DP groups were not aggregated exactly: %#v", sample)
	}
	if !sample.KVTokenMetricsValid || sample.KVCapacityTokens != 2_000_000 ||
		sample.KVUsedTokens != 100_000 || sample.KVAvailableTokens != 1_800_000 ||
		sample.KVEvictableTokens != 100_000 || !sample.KVBlockSizeValid || sample.KVBlockSize != 16 {
		t.Fatalf("logical KV capacity duplicated or lost: %#v", sample)
	}
	if !sample.CacheTokensValid || sample.CacheQueryTokens != 130 || sample.CacheHitTokens != 90 {
		t.Fatalf("cache token DP sum is incorrect: %#v", sample)
	}
}

func TestSGLangAttentionDPUsesConfiguredGeometryWhenDPLabelIsAbsent(t *testing.T) {
	sample := ParseSampleWithSGLangTopology(attentionDPFixture(8, 4, 8, false), SGLangTopology{TPSize: 8, DPSize: 4})
	if !sample.ModelNameValid || sample.Running != 10 || sample.Waiting != 6 || sample.Generation != 1000 {
		t.Fatalf("global TP ranks were not partitioned into four attention groups: %#v", sample)
	}
}

func TestSGLangAttentionDPRejectsIncompleteOrContradictoryScrapes(t *testing.T) {
	complete := attentionDPFixture(4, 2, 4, true)
	tests := map[string]string{
		"missing replica":        attentionDPFixture(4, 2, 2, true),
		"contradictory DP label": strings.Replace(complete, "tp_rank=\"2\",dp_rank=\"1\"", "tp_rank=\"2\",dp_rank=\"0\"", 1),
		"out of range TP":        strings.Replace(complete, "tp_rank=\"3\"", "tp_rank=\"4\"", 1),
		"malformed TP":           strings.Replace(complete, "tp_rank=\"2\"", "tp_rank=\"02\"", 1),
		"mixed model":            strings.Replace(complete, "meta/test-model", "meta/other-model", 1),
	}
	for name, metrics := range tests {
		t.Run(name, func(t *testing.T) {
			sample := ParseSampleWithSGLangTopology(metrics, SGLangTopology{TPSize: 4, DPSize: 2})
			if sample.ModelNameValid {
				t.Fatalf("unsafe partial observation accepted: %#v", sample)
			}
		})
	}
}

func TestSGLangAttentionDPRejectsCounterOverflowAndInvalidTopology(t *testing.T) {
	metrics := attentionDPFixture(4, 2, 4, true)
	lines := strings.Split(metrics, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "sglang:realtime_tokens_total{") {
			lines[i] = line[:strings.LastIndexByte(line, ' ')+1] + "9007199254740992"
		}
	}
	if ParseSampleWithSGLangTopology(strings.Join(lines, "\n"), SGLangTopology{TPSize: 4, DPSize: 2}).ModelNameValid {
		t.Fatal("sum beyond exact counter range accepted")
	}
	for _, geometry := range []SGLangTopology{{TPSize: 4, DPSize: 3}, {TPSize: 0, DPSize: 2}, {TPSize: 5000, DPSize: 1}} {
		if ParseSampleWithSGLangTopology(metrics, geometry).ModelNameValid {
			t.Fatalf("invalid geometry accepted: %#v", geometry)
		}
	}
}

func TestSGLangAttentionDPOptionalCacheMismatchKeepsAdmissionUsable(t *testing.T) {
	metrics := attentionDPFixture(4, 2, 4, true)
	lines := strings.Split(metrics, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "sglang:prefill_effective_tokens_total{") {
			lines[i] = strings.Replace(line, "tp_rank=\"0\",dp_rank=\"0\"", "tp_rank=\"0\",dp_rank=\"1\"", 1)
		}
	}
	sample := ParseSampleWithSGLangTopology(strings.Join(lines, "\n"), SGLangTopology{TPSize: 4, DPSize: 2})
	if !sample.ModelNameValid || sample.Running != 3 || sample.CacheTokensValid || sample.CacheQueryTokens != 0 {
		t.Fatalf("optional cache corruption changed admission or fabricated cache hits: %#v", sample)
	}
}

func TestFetchSampleCarriesSGLangTopology(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(attentionDPFixture(8, 8, 8, true)))
	}))
	defer server.Close()
	sample, err := FetchSampleContextWithSGLangTopology(context.Background(), server.Client(), server.URL, SGLangTopology{TPSize: 8, DPSize: 8})
	if err != nil || !sample.ModelNameValid || sample.Running != 36 || sample.Generation != 3600 {
		t.Fatalf("fetch lost topology or summed TP copies: sample=%#v err=%v", sample, err)
	}
}

func TestSGLangAttentionDPActualGLM53Metrics(t *testing.T) {
	// Captured 2026-09-15 14:52 UTC from GLM-5.3 TP8/DP8. Immutable serving
	// image: ghcr.io/phala-network/sglang:glm53-standard-r3, source 18e91bc0.
	raw, err := os.ReadFile("testdata/sglang_dp8_live_20260915.prom")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "9849d4e40061ee11ee21081988ab209bc65cf086e307ad4324c92d66431db3b8" {
		t.Fatal("captured fixture changed")
	}
	if ParseSample(string(raw)).ModelNameValid {
		t.Fatal("the deployed single-DP contract unexpectedly accepted this actual scrape")
	}
	sample := ParseSampleWithSGLangTopology(string(raw), SGLangTopology{TPSize: 8, DPSize: 8})
	if !sample.ModelNameValid || sample.ModelName != "z-ai/glm-5.3" ||
		sample.Generation != 311 || sample.Running != 0 || sample.Waiting != 0 ||
		!sample.KVTokenMetricsValid || sample.KVCapacityTokens != 25524736 ||
		sample.KVUsedTokens != 159744 || sample.KVAvailableTokens != 25050944 ||
		sample.KVEvictableTokens != 314048 {
		t.Fatalf("actual DP8 scrape was lost or TP duplicated: %#v", sample)
	}
}
