package prometheus

import (
	"fmt"
	"strings"
	"testing"
)

func pdDecodeFixture() string {
	return strings.ReplaceAll(coherentSGLangFixture(), `engine_type="unified"`, `engine_type="decode"`)
}

func pdQueue(name, labels, value string) string {
	return fmt.Sprintf("# TYPE sglang:%s gauge\nsglang:%s{engine_type=\"decode\",model_name=\"meta/test-model\",%s} %s\n", name, name, labels, value)
}

func TestSGLangPDDecodeCountsDisjointQueuesAndDeduplicatesTP(t *testing.T) {
	text := pdDecodeFixture() +
		pdQueue("num_decode_prealloc_queue_reqs", `tp_rank="0"`, "2") +
		pdQueue("num_decode_prealloc_queue_reqs", `tp_rank="1"`, "2") +
		pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0"`, "3") +
		pdQueue("num_decode_transfer_queue_reqs", `tp_rank="1"`, "3")
	text += "sglang:realtime_tokens_total{engine_type=\"decode\",model_name=\"meta/test-model\",mode=\"decode\",tp_rank=\"1\"} 100\n"
	s := ParseSample(text)
	if !s.ModelNameValid || !s.RunningValid || !s.WaitingValid || s.Waiting != 0 ||
		!s.DecodePendingValid || s.DecodePending != 5 || !s.GenerationValid || s.Generation != 100 {
		t.Fatalf("PD decode observation is not coherent/deduplicated: %#v", s)
	}
	if s.CacheTokensValid {
		t.Fatal("Decode must not fabricate Prefill cache accounting")
	}
}

func TestSGLangPDDecodeColdQueueAndRoleContract(t *testing.T) {
	for _, role := range []string{"unified", "decode", "prefill"} {
		s := ParseSample(strings.ReplaceAll(pdDecodeFixture(), `engine_type="decode"`, `engine_type="`+role+`"`))
		if s.ModelNameValid != (role != "prefill") {
			t.Fatalf("role=%s sample=%#v", role, s)
		}
		if role == "decode" && (!s.WaitingValid || s.Waiting != 0 || !s.DecodePendingValid || s.DecodePending != 0) {
			t.Fatalf("cold decode queue %#v", s)
		}
	}
}

func TestSGLangPDDecodeRejectsInvalidQueueSamples(t *testing.T) {
	for name, extra := range map[string]string{
		"negative":               pdQueue("num_decode_prealloc_queue_reqs", `tp_rank="0"`, "-1"),
		"negative-hidden-by-max": pdQueue("num_decode_prealloc_queue_reqs", `tp_rank="0"`, "-1") + pdQueue("num_decode_prealloc_queue_reqs", `tp_rank="1"`, "2"),
		"nan":                    pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0"`, "NaN"),
		"fraction":               pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0"`, "1.5"),
		"priority-only":          pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0",priority="1"`, "2"),
		"mixed-role":             strings.ReplaceAll(pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0"`, "2"), `engine_type="decode"`, `engine_type="prefill"`),
		"mixed-model":            strings.ReplaceAll(pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0"`, "2"), "meta/test-model", "meta/other"),
		"multi-dp":               pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0",dp_rank="1"`, "2"),
		"wrong-type":             strings.ReplaceAll(pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0"`, "2"), " gauge", " counter"),
		"malformed-label":        pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0",tp_rank="1"`, "2"),
	} {
		t.Run(name, func(t *testing.T) {
			s := ParseSample(pdDecodeFixture() + extra)
			if s.ModelNameValid && s.DecodePendingValid {
				t.Fatalf("unsafe PD queue accepted: %#v", s)
			}
		})
	}
}

func TestSGLangPDDecodeTransferBurstDoesNotBecomeSchedulerWaiting(t *testing.T) {
	s := ParseSample(pdDecodeFixture() + pdQueue("num_decode_transfer_queue_reqs", `tp_rank="0"`, "124"))
	if !s.ModelNameValid || !s.WaitingValid || s.Waiting != 0 || !s.DecodePendingValid || s.DecodePending != 124 {
		t.Fatalf("PD transfer burst conflated with scheduler waiting: %#v", s)
	}
}
