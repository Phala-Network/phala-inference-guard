package server

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
)

func TestV01218ResponseUsageEvidenceIsFixedCardinalityAndExactOnce(t *testing.T) {
	var evidence responseUsageEvidence
	var empty bytes.Buffer
	writeResponseUsageEvidenceMetrics(&empty, evidence.Snapshot())

	request := evidence.Begin(apprequest.Classification{
		Cost: kvadmission.Cost{Supported: true, Estimate: domainpredictive.RequestEstimate{
			OutputLimitKnown:  true,
			OutputLimitTokens: 1_024,
		}},
	}, "/v1/chat/completions", false)
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

func TestSuccessfulCompletionTokensCountOnlySuccessfulExactUsageOnce(t *testing.T) {
	var evidence responseUsageEvidence
	request := evidence.Begin(apprequest.Classification{}, "/v1/chat/completions", false)
	request.observe(openai.CompletionUsageEvidence{
		Outcome: openai.CompletionUsageAvailable,
		Usage:   openai.CompletionUsage{CompletionTokens: 123},
	})
	request.Complete(proxyResult{status: http.StatusOK})
	request.Complete(proxyResult{status: http.StatusOK})
	request.Censor()

	var output bytes.Buffer
	writeResponseUsageEvidenceMetrics(&output, evidence.Snapshot())
	for _, want := range []string{
		`pig_predictive_successful_completion_tokens_total 123`,
		`pig_predictive_response_usage_outcomes_total{outcome="available"} 1`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("successful completion evidence missing %q\nmetrics:\n%s", want, output.String())
		}
	}
}

func TestSuccessfulCompletionTokensExcludeEveryNonSuccessTerminal(t *testing.T) {
	var evidence responseUsageEvidence
	results := []proxyResult{
		{status: http.StatusInternalServerError},
		{status: http.StatusOK, proxyFailed: true},
		{status: http.StatusOK, timedOut: true},
		{status: clientClosedRequestStatus},
	}
	for _, result := range results {
		request := evidence.Begin(apprequest.Classification{}, "/v1/chat/completions", false)
		request.observe(openai.CompletionUsageEvidence{
			Outcome: openai.CompletionUsageAvailable,
			Usage:   openai.CompletionUsage{CompletionTokens: 50},
		})
		request.Complete(result)
	}

	var output bytes.Buffer
	writeResponseUsageEvidenceMetrics(&output, evidence.Snapshot())
	for _, want := range []string{
		`pig_predictive_successful_completion_tokens_total 0`,
		`pig_predictive_response_usage_outcomes_total{outcome="available"} 0`,
		`pig_predictive_response_usage_outcomes_total{outcome="censored"} 4`,
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("non-success completion evidence missing %q\nmetrics:\n%s", want, output.String())
		}
	}
}
