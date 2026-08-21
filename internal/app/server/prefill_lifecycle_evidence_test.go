package server

import (
	"bytes"
	"strings"
	"testing"
	"time"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestV01218PrefillLifecycleEvidenceIsExactOnceAndFixedCardinality(t *testing.T) {
	var evidence prefillLifecycleEvidence
	var empty bytes.Buffer
	writePrefillLifecycleEvidenceMetrics(&empty, evidence.Snapshot())

	request := evidence.Begin(apprequest.Classification{
		Cost: kvadmission.Cost{Supported: true, Estimate: domainpredictive.RequestEstimate{
			BasePromptCount: 2,
			DecodeSequences: 4,
		}},
	})
	started := time.Unix(100, 0)
	if request.MarkFirstByte(started) || !request.MarkForwarded() || request.MarkForwarded() ||
		!request.MarkFirstByte(started) || request.MarkFirstByte(started.Add(time.Millisecond)) ||
		!request.Terminate(started.Add(250*time.Millisecond)) || request.Terminate(started.Add(time.Second)) {
		t.Fatal("Prefill lifecycle transitions were not exact-once")
	}

	var output bytes.Buffer
	writePrefillLifecycleEvidenceMetrics(&output, evidence.Snapshot())
	metricsBody := output.String()
	if strings.Count(empty.String(), "\n") != strings.Count(metricsBody, "\n") {
		t.Fatalf(
			"Prefill lifecycle evidence changed cardinality: empty=%d recorded=%d",
			strings.Count(empty.String(), "\n"),
			strings.Count(metricsBody, "\n"),
		)
	}
	for _, want := range []string{
		`pig_predictive_prefill_lifecycle_total{outcome="first_byte_then_terminal",sequence_shape="prompt_batch_fanout"} 1`,
		`pig_predictive_prefill_first_byte_to_terminal_seconds_count 1`,
		`pig_predictive_prefill_first_byte_to_terminal_seconds_sum 0.250000`,
		`pig_predictive_prefill_first_byte_to_terminal_seconds_bucket{le="0.25"} 1`,
		`pig_predictive_prefill_first_byte_to_terminal_seconds_bucket{le="+Inf"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("Prefill lifecycle evidence missing %q", want)
		}
	}
}
