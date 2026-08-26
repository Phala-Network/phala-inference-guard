package server

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

func TestAdmissionEvidenceNormalizesInvalidEnumsAndKeepsFixedCardinality(t *testing.T) {
	var evidence admissionEvidence
	var empty bytes.Buffer
	writeAdmissionEvidenceMetrics(&empty, evidence.Snapshot())

	evidence.Record(coreadmission.DecisionRecord{
		Action: coreadmission.ActionProtect,
		Reason: coreadmission.Reason("request-derived-reason"),
		Scope:  coreadmission.ProtectionScope("request-derived-scope"),
		Demand: coreadmission.TPSRequestDemand{
			DecodeSequences: 32,
			Source:          coreadmission.TPSDemandSource("request-derived-source"),
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
		"request-derived-source",
	} {
		if strings.Contains(metricsBody, forbidden) {
			t.Fatalf("request-derived metric label escaped normalization: %q", forbidden)
		}
	}
	for _, want := range []string{
		`pig_predictive_admission_outcomes_total{outcome="availability_protected"} 1`,
		`pig_predictive_admission_protections_total{reason="unknown",scope="unknown"} 1`,
		`pig_predictive_admission_decode_fanout_total{bucket=">16",outcome="availability_protected"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("normalized bounded evidence missing %q", want)
		}
	}
}

func TestAdmissionEvidenceSnapshotAndMetricsScrapeAreReadOnly(t *testing.T) {
	var evidence admissionEvidence
	for _, decision := range []coreadmission.DecisionRecord{
		{
			Action: coreadmission.ActionAdmit,
			Reason: coreadmission.ReasonOpen,
			Demand: coreadmission.TPSRequestDemand{DecodeSequences: 1},
		},
		{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonInvalidRequest,
			Scope:  coreadmission.ProtectionRequest,
			Demand: coreadmission.TPSRequestDemand{DecodeSequences: 2},
		},
		{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonTPSReference,
			Scope:  coreadmission.ProtectionLoad,
			Demand: coreadmission.TPSRequestDemand{DecodeSequences: 4},
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
	if after.decodeFanout[admissionEvidenceFanoutOne][admissionEvidenceAdmitted] != 1 ||
		after.decodeFanout[admissionEvidenceFanoutTwo][admissionEvidenceRequestProtected] != 1 ||
		after.decodeFanout[admissionEvidenceFanoutThreeToFour][admissionEvidenceLoadProtected] != 1 ||
		after.decodeFanout[admissionEvidenceFanoutUnknown][admissionEvidenceAvailabilityProtected] != 1 {
		t.Fatalf("fanout evidence=%+v", after.decodeFanout)
	}
}
