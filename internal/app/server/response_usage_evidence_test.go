package server

import (
	"bytes"
	"strings"
	"testing"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestV01218ResponseUsageEvidenceIsFixedCardinalityAndExactOnce(t *testing.T) {
	var evidence responseUsageEvidence
	var empty bytes.Buffer
	writeResponseUsageEvidenceMetrics(&empty, evidence.Snapshot())

	request := evidence.Begin(apprequest.Classification{
		Cost: kvadmission.Cost{Supported: true, Estimate: domainpredictive.RequestEstimate{
			OutputLimitKnown: true,
			OutputLimitTokens: 1_024,
		}},
	})
	request.Censor()
	request.Censor()
	request.Complete(proxyResult{status: 200})

	var output bytes.Buffer
	writeResponseUsageEvidenceMetrics(&output, evidence.Snapshot())
	metricsBody := output.String()
	if strings.Count(empty.String(), "\n") != strings.Count(metricsBody, "\n") {
		t.Fatalf(
			"response-usage evidence changed cardinality: empty=%d recorded=%d",
			strings.Count(empty.String(), "\n"),
			strings.Count(metricsBody, "\n"),
		)
	}
	for _, want := range []string{
		`pig_predictive_response_usage_outcomes_total{outcome="censored"} 1`,
		`pig_predictive_output_limit_comparison_total{actual_bucket="censored",declared_bucket="le_1024"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("response-usage evidence missing %q", want)
		}
	}
}
