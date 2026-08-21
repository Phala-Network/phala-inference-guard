package server

import (
	"bytes"
	"strings"
	"testing"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

func TestV01218TPSEvidenceNormalizesInvalidDecisionEnums(t *testing.T) {
	var evidence tpsDecisionEvidence
	var empty bytes.Buffer
	writeTPSDecisionEvidenceMetrics(&empty, evidence.Snapshot())

	evidence.Record(coreadmission.DecisionRecord{
		TPSDecisionResult:    coreadmission.TPSDecisionResult(255),
		TPSDecisionSubreason: coreadmission.TPSDecisionSubreason(255),
	})
	var output bytes.Buffer
	writeTPSDecisionEvidenceMetrics(&output, evidence.Snapshot())
	metricsBody := output.String()
	if strings.Count(empty.String(), "\n") != strings.Count(metricsBody, "\n") {
		t.Fatalf(
			"TPS evidence changed metric cardinality: empty=%d recorded=%d",
			strings.Count(empty.String(), "\n"),
			strings.Count(metricsBody, "\n"),
		)
	}
	if !strings.Contains(
		metricsBody,
		`pig_predictive_tps_decisions_total{result="unknown",subreason="unknown"} 1`,
	) {
		t.Fatalf("invalid TPS enums were not normalized:\n%s", metricsBody)
	}
}
