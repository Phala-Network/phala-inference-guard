package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
)

type responseUsageEvidenceOutcome uint8

const (
	responseUsageEvidenceAvailable responseUsageEvidenceOutcome = iota
	responseUsageEvidenceUnavailable
	responseUsageEvidenceMalformed
	responseUsageEvidenceCensored
	responseUsageEvidenceOutcomeCount
)

var responseUsageEvidenceOutcomeLabels = [...]string{
	"available",
	"unavailable",
	"malformed",
	"censored",
}

type actualOutputTokenBucket uint8

const (
	actualOutputTokensLE64 actualOutputTokenBucket = iota
	actualOutputTokensLE256
	actualOutputTokensLE1024
	actualOutputTokensLE4096
	actualOutputTokensLE16384
	actualOutputTokensGT16384
	actualOutputTokensUnavailable
	actualOutputTokensMalformed
	actualOutputTokensCensored
	actualOutputTokenBucketCount
)

var actualOutputTokenBucketLabels = [...]string{
	"le_64",
	"le_256",
	"le_1024",
	"le_4096",
	"le_16384",
	"gt_16384",
	"unavailable",
	"malformed",
	"censored",
}

type responseUsageEvidence struct {
	mu                         sync.Mutex
	outcomes                   [responseUsageEvidenceOutcomeCount]uint64
	actuals                    [actualOutputTokenBucketCount]uint64
	successfulCompletionTokens uint64
}

type responseUsageEvidenceSnapshot struct {
	outcomes                   [responseUsageEvidenceOutcomeCount]uint64
	actuals                    [actualOutputTokenBucketCount]uint64
	successfulCompletionTokens uint64
}

type responseUsageRequestEvidence struct {
	owner          *responseUsageEvidence
	streamingKnown bool
	streaming      bool
	formatKnown    bool
	format         openai.CompletionUsageFormat

	mu             sync.Mutex
	parserAttached bool
	observation    openai.CompletionUsageEvidence
	observed       bool
	completed      bool
}

type responseUsageRequestEvidenceContextKey struct{}

func (e *responseUsageEvidence) Begin(
	classification apprequest.Classification,
	path string,
) *responseUsageRequestEvidence {
	format, formatKnown := responseUsageFormatForPath(path)
	return &responseUsageRequestEvidence{
		owner: e,
		streamingKnown: classification.JSONFieldsKnown &&
			(!classification.StreamingPresent || classification.StreamingKnown),
		streaming:   classification.Streaming,
		formatKnown: formatKnown,
		format:      format,
	}
}

func (e *responseUsageEvidence) record(
	outcome responseUsageEvidenceOutcome,
	actual actualOutputTokenBucket,
	successfulCompletionTokens uint64,
) {
	if e == nil {
		return
	}
	if outcome >= responseUsageEvidenceOutcomeCount {
		outcome = responseUsageEvidenceMalformed
	}
	if actual >= actualOutputTokenBucketCount {
		actual = actualOutputTokensMalformed
	}
	e.mu.Lock()
	e.outcomes[outcome]++
	e.actuals[actual]++
	e.successfulCompletionTokens += successfulCompletionTokens
	e.mu.Unlock()
}

func (e *responseUsageEvidence) Snapshot() responseUsageEvidenceSnapshot {
	if e == nil {
		return responseUsageEvidenceSnapshot{}
	}
	e.mu.Lock()
	snapshot := responseUsageEvidenceSnapshot{
		outcomes:                   e.outcomes,
		actuals:                    e.actuals,
		successfulCompletionTokens: e.successfulCompletionTokens,
	}
	e.mu.Unlock()
	return snapshot
}

func writeResponseUsageEvidenceMetrics(w io.Writer, snapshot responseUsageEvidenceSnapshot) {
	fmt.Fprintf(w, "pig_predictive_successful_completion_tokens_total %d\n", snapshot.successfulCompletionTokens)
	for outcome, label := range responseUsageEvidenceOutcomeLabels {
		fmt.Fprintf(w, "pig_predictive_response_usage_outcomes_total{outcome=%q} %d\n", label, snapshot.outcomes[outcome])
	}
	for actual, label := range actualOutputTokenBucketLabels {
		fmt.Fprintf(w, "pig_predictive_response_completion_tokens_total{bucket=%q} %d\n", label, snapshot.actuals[actual])
	}
}

func (r *responseUsageRequestEvidence) WrapResponse(response *http.Response) {
	if r == nil || response == nil || response.Body == nil ||
		response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices ||
		!r.streamingKnown || !r.formatKnown ||
		!openai.CompletionUsageContentTypeEligible(response.Header.Get("Content-Type"), r.streaming) {
		return
	}
	r.mu.Lock()
	if r.completed || r.parserAttached {
		r.mu.Unlock()
		return
	}
	r.parserAttached = true
	r.mu.Unlock()
	response.Body = openai.ObserveCompletionUsageEvidenceBodyForFormatLength(
		response.Body,
		r.streaming,
		r.format,
		response.ContentLength,
		r.observe,
	)
}

func (r *responseUsageRequestEvidence) observe(observation openai.CompletionUsageEvidence) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.completed && !r.observed {
		r.observation = observation
		r.observed = true
	}
	r.mu.Unlock()
}

func (r *responseUsageRequestEvidence) Complete(result proxyResult) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.completed {
		r.mu.Unlock()
		return
	}
	outcome := responseUsageEvidenceCensored
	actual := actualOutputTokensCensored
	var successfulCompletionTokens uint64
	if proxyResultSucceeded(result) {
		switch {
		case r.observed:
			outcome, actual, successfulCompletionTokens = responseUsageEvidenceForObservation(r.observation)
		case !r.parserAttached:
			outcome = responseUsageEvidenceUnavailable
			actual = actualOutputTokensUnavailable
		}
	}
	r.completed = true
	owner := r.owner
	r.mu.Unlock()
	owner.record(outcome, actual, successfulCompletionTokens)
}

func (r *responseUsageRequestEvidence) Censor() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.completed {
		r.mu.Unlock()
		return
	}
	r.completed = true
	owner := r.owner
	r.mu.Unlock()
	owner.record(responseUsageEvidenceCensored, actualOutputTokensCensored, 0)
}

func responseUsageEvidenceForObservation(
	observation openai.CompletionUsageEvidence,
) (responseUsageEvidenceOutcome, actualOutputTokenBucket, uint64) {
	switch observation.Outcome {
	case openai.CompletionUsageAvailable:
		if observation.Usage.CompletionTokens >= 0 {
			return responseUsageEvidenceAvailable,
				actualOutputTokenBucketFor(observation.Usage.CompletionTokens),
				uint64(observation.Usage.CompletionTokens)
		}
	case openai.CompletionUsageUnavailable:
		return responseUsageEvidenceUnavailable, actualOutputTokensUnavailable, 0
	}
	return responseUsageEvidenceMalformed, actualOutputTokensMalformed, 0
}

func actualOutputTokenBucketFor(tokens int64) actualOutputTokenBucket {
	switch {
	case tokens <= 64:
		return actualOutputTokensLE64
	case tokens <= 256:
		return actualOutputTokensLE256
	case tokens <= 1_024:
		return actualOutputTokensLE1024
	case tokens <= 4_096:
		return actualOutputTokensLE4096
	case tokens <= 16_384:
		return actualOutputTokensLE16384
	default:
		return actualOutputTokensGT16384
	}
}

func responseUsageFormatForPath(path string) (openai.CompletionUsageFormat, bool) {
	switch path {
	case "/v1/chat/completions", "/v1/completions":
		return openai.CompletionUsageFormatCompletions, true
	case "/v1/responses":
		return openai.CompletionUsageFormatResponses, true
	default:
		return openai.CompletionUsageFormatCompletions, false
	}
}

func attachResponseUsageRequestEvidence(
	ctx context.Context,
	evidence *responseUsageRequestEvidence,
) context.Context {
	if evidence == nil {
		return ctx
	}
	return context.WithValue(ctx, responseUsageRequestEvidenceContextKey{}, evidence)
}

func responseUsageRequestEvidenceFrom(response *http.Response) *responseUsageRequestEvidence {
	if response == nil || response.Request == nil {
		return nil
	}
	evidence, _ := response.Request.Context().Value(responseUsageRequestEvidenceContextKey{}).(*responseUsageRequestEvidence)
	return evidence
}
