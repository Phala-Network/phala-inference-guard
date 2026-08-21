package server

import (
	"bytes"
	"strings"
	"testing"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
)

func TestV01218RequestEvidenceNormalizesUnknownClassifierReasons(t *testing.T) {
	var evidence requestEvidence
	var empty bytes.Buffer
	writeRequestEvidenceMetrics(&empty, evidence.Snapshot())

	evidence.Record(apprequest.Classification{
		Cost:             kvadmission.Cost{UnsupportedReason: "request-derived-outcome"},
		JSONFieldsKnown:  true,
		StreamingPresent: true,
		StreamingKnown:   false,
		DecodeSequences:  17,
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
		`pig_predictive_estimator_validation_total{estimate_kind="selection_input",result="unknown"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="context_upper_bound",result="unknown"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="kv_reservation",result="unknown"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("normalized request evidence missing %q", want)
		}
	}
}

func TestV01218RequestEvidenceUsesFixedEndpointFallbackLabels(t *testing.T) {
	var evidence requestEvidence
	var empty bytes.Buffer
	writeRequestEvidenceMetrics(&empty, evidence.Snapshot())

	for _, reason := range []string{"unsupported_endpoint", "body_external_context"} {
		evidence.Record(apprequest.Classification{Cost: kvadmission.Cost{UnsupportedReason: reason}})
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
		`pig_predictive_classifier_outcomes_total{outcome="body_external_context"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="selection_input",result="unsupported_endpoint"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="selection_input",result="body_external_context"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="context_upper_bound",result="unsupported_endpoint"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="context_upper_bound",result="body_external_context"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="kv_reservation",result="unsupported_endpoint"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="kv_reservation",result="body_external_context"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("fixed endpoint fallback evidence missing %q\nmetrics:\n%s", want, metricsBody)
		}
	}
}
