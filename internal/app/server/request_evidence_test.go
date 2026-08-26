package server

import (
	"bytes"
	"strings"
	"testing"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
)

func TestV01218RequestEvidenceNormalizesUnknownClassifierReasons(t *testing.T) {
	var evidence requestEvidence
	var empty bytes.Buffer
	writeRequestEvidenceMetrics(&empty, evidence.Snapshot())

	evidence.Record(apprequest.Classification{
		UnsupportedReason: "request-derived-outcome",
		JSONFieldsKnown:   true,
		StreamingPresent:  true,
		StreamingKnown:    false,
		DecodeSequences:   17,
	})
	var output bytes.Buffer
	writeRequestEvidenceMetrics(&output, evidence.Snapshot())
	metricsBody := output.String()
	if strings.Count(empty.String(), "\n") != strings.Count(metricsBody, "\n") {
		t.Fatalf(
			"classifier evidence changed metric cardinality: empty=%d recorded=%d",
			strings.Count(empty.String(), "\n"),
			strings.Count(metricsBody, "\n"),
		)
	}
	if strings.Contains(metricsBody, "request-derived-outcome") {
		t.Fatal("request-derived classifier outcome escaped normalization")
	}
	for _, want := range []string{
		`pig_predictive_classifier_outcomes_total{outcome="unknown"} 1`,
		`pig_predictive_request_streaming_total{state="invalid"} 1`,
		`pig_predictive_request_decode_fanout_total{bucket=">16"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("normalized request evidence missing %q", want)
		}
	}
}

func TestV01218RequestEvidenceUsesFixedUnsupportedLabels(t *testing.T) {
	var evidence requestEvidence
	var empty bytes.Buffer
	writeRequestEvidenceMetrics(&empty, evidence.Snapshot())

	for _, reason := range []string{"unsupported_endpoint", "unsupported_request_shape", "shape_scan_limit"} {
		evidence.Record(apprequest.Classification{UnsupportedReason: reason})
	}
	var output bytes.Buffer
	writeRequestEvidenceMetrics(&output, evidence.Snapshot())
	metricsBody := output.String()
	if strings.Count(empty.String(), "\n") != strings.Count(metricsBody, "\n") {
		t.Fatalf("endpoint fallback evidence changed metric cardinality: empty=%d recorded=%d",
			strings.Count(empty.String(), "\n"), strings.Count(metricsBody, "\n"))
	}
	for _, want := range []string{
		`pig_predictive_classifier_outcomes_total{outcome="unsupported_endpoint"} 1`,
		`pig_predictive_classifier_outcomes_total{outcome="unsupported_request_shape"} 1`,
		`pig_predictive_classifier_outcomes_total{outcome="shape_scan_limit"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("fixed endpoint fallback evidence missing %q\nmetrics:\n%s", want, metricsBody)
		}
	}
}
