package server

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestV01218AdmissionEvidenceNormalizesInvalidEnumsAndKeepsFixedCardinality(t *testing.T) {
	var evidence admissionEvidence
	var empty bytes.Buffer
	writeAdmissionEvidenceMetrics(&empty, evidence.Snapshot())

	evidence.Record(coreadmission.DecisionRecord{
		Action:       coreadmission.ActionProtect,
		Reason:       coreadmission.Reason("request-derived-reason"),
		Scope:        coreadmission.ProtectionScope("request-derived-scope"),
		PrefillClass: coreadmission.PrefillClass("request-derived-class"),
		Estimate: domainpredictive.RequestEstimate{
			SelectionInputTokens:    512,
			DecodeSequences:         32,
			InputEstimateConfidence: domainpredictive.InputEstimateConfidence(255),
		},
	})
	var output bytes.Buffer
	writeAdmissionEvidenceMetrics(&output, evidence.Snapshot())
	metricsBody := output.String()
	if strings.Count(empty.String(), "\n") != strings.Count(metricsBody, "\n") {
		t.Fatalf(
			"invalid enums changed metric cardinality: empty=%d recorded=%d",
			strings.Count(empty.String(), "\n"),
			strings.Count(metricsBody, "\n"),
		)
	}
	for _, forbidden := range []string{
		"request-derived-reason",
		"request-derived-scope",
		"request-derived-class",
	} {
		if strings.Contains(metricsBody, forbidden) {
			t.Fatalf("request-derived metric label escaped normalization: %q", forbidden)
		}
	}
	for _, want := range []string{
		`pig_predictive_admission_outcomes_total{outcome="availability_protected"} 1`,
		`pig_predictive_admission_protections_total{reason="unknown",scope="unknown"} 1`,
		`pig_predictive_admission_estimate_confidence_total{confidence="unknown",outcome="availability_protected"} 1`,
		`pig_predictive_admission_prefill_class_total{outcome="availability_protected",prefill_class="unknown"} 1`,
		`pig_predictive_admission_decode_fanout_total{bucket=">16",outcome="availability_protected"} 1`,
		`pig_predictive_admission_selection_input_tokens_bucket{le="1024",outcome="availability_protected"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("normalized bounded evidence missing %q", want)
		}
	}
}

func TestV01218AdmissionEvidenceSnapshotAndMetricsScrapeAreReadOnly(t *testing.T) {
	var evidence admissionEvidence
	for _, decision := range []coreadmission.DecisionRecord{
		{
			Action:       coreadmission.ActionAdmit,
			Reason:       coreadmission.ReasonOpen,
			PrefillClass: coreadmission.PrefillRegular,
			Estimate: domainpredictive.RequestEstimate{
				SelectionInputTokens:    1_000,
				DecodeSequences:         1,
				InputEstimateConfidence: domainpredictive.InputEstimateConfidenceLexical,
			},
		},
		{
			Action:       coreadmission.ActionProtect,
			Reason:       coreadmission.ReasonInputLimit,
			Scope:        coreadmission.ProtectionRequest,
			PrefillClass: coreadmission.PrefillWeighted,
			Estimate: domainpredictive.RequestEstimate{
				SelectionInputTokens:    2_000,
				DecodeSequences:         2,
				InputEstimateConfidence: domainpredictive.InputEstimateConfidenceConservative,
			},
		},
		{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonTPSReference,
			Scope:  coreadmission.ProtectionLoad,
		},
		{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonObservationStale,
			Scope:  coreadmission.ProtectionAvailability,
		},
	} {
		evidence.Record(decision)
	}

	before := evidence.Snapshot()
	var first bytes.Buffer
	var second bytes.Buffer
	writeAdmissionEvidenceMetrics(&first, before)
	writeAdmissionEvidenceMetrics(&second, evidence.Snapshot())
	after := evidence.Snapshot()
	if !reflect.DeepEqual(before, after) || first.String() != second.String() {
		t.Fatal("metrics scrape mutated cumulative admission evidence")
	}
	var attempts uint64
	for _, count := range after.outcomes {
		attempts += count
	}
	if attempts != 4 ||
		after.outcomes[admissionEvidenceAdmitted] != 1 ||
		after.outcomes[admissionEvidenceRequestProtected] != 1 ||
		after.outcomes[admissionEvidenceLoadProtected] != 1 ||
		after.outcomes[admissionEvidenceAvailabilityProtected] != 1 {
		t.Fatalf("outcome partition=%v attempts=%d, want one of each outcome", after.outcomes, attempts)
	}
	if after.selectionInputTokens[0][admissionEvidenceAdmitted] != 1 ||
		after.selectionInputTokens[0][admissionEvidenceRequestProtected] != 0 ||
		after.selectionInputTokens[1][admissionEvidenceRequestProtected] != 1 ||
		after.unknownInputTokens[admissionEvidenceLoadProtected] != 1 ||
		after.unknownInputTokens[admissionEvidenceAvailabilityProtected] != 1 {
		t.Fatalf("input histogram is not cumulative or did not preserve unknowns: %+v", after)
	}
}
