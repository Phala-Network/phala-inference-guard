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

type declaredOutputTokenBucket uint8

const (
	declaredOutputTokensUnknown declaredOutputTokenBucket = iota
	declaredOutputTokensLE64
	declaredOutputTokensLE256
	declaredOutputTokensLE1024
	declaredOutputTokensLE4096
	declaredOutputTokensLE16384
	declaredOutputTokensGT16384
	declaredOutputTokenBucketCount
)

var declaredOutputTokenBucketLabels = [...]string{
	"unknown",
	"le_64",
	"le_256",
	"le_1024",
	"le_4096",
	"le_16384",
	"gt_16384",
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
	mu          sync.Mutex
	outcomes    [responseUsageEvidenceOutcomeCount]uint64
	comparisons [declaredOutputTokenBucketCount][actualOutputTokenBucketCount]uint64
}

type responseUsageEvidenceSnapshot struct {
	outcomes    [responseUsageEvidenceOutcomeCount]uint64
	comparisons [declaredOutputTokenBucketCount][actualOutputTokenBucketCount]uint64
}

type responseUsageRequestEvidence struct {
	owner          *responseUsageEvidence
	declared       declaredOutputTokenBucket
	streamingKnown bool
	streaming      bool

	mu             sync.Mutex
	parserAttached bool
	observation    openai.CompletionUsageEvidence
	observed       bool
	completed      bool
}

type responseUsageRequestEvidenceContextKey struct{}

func (e *responseUsageEvidence) Begin(classification apprequest.Classification) *responseUsageRequestEvidence {
	declared := declaredOutputTokensUnknown
	estimate := classification.Cost.Estimate
	if estimate.OutputLimitKnown && estimate.OutputLimitTokens > 0 {
		declared = declaredOutputTokenBucketFor(estimate.OutputLimitTokens)
	}
	return &responseUsageRequestEvidence{
		owner:          e,
		declared:       declared,
		streamingKnown: classification.JSONFieldsKnown && classification.StreamingKnown,
		streaming:      classification.Streaming,
	}
}

func (e *responseUsageEvidence) record(
	declared declaredOutputTokenBucket,
	outcome responseUsageEvidenceOutcome,
	actual actualOutputTokenBucket,
) {
	if e == nil {
		return
	}
	if declared >= declaredOutputTokenBucketCount {
		declared = declaredOutputTokensUnknown
	}
	if outcome >= responseUsageEvidenceOutcomeCount {
		outcome = responseUsageEvidenceMalformed
	}
	if actual >= actualOutputTokenBucketCount {
		actual = actualOutputTokensMalformed
	}
	e.mu.Lock()
	e.outcomes[outcome]++
	e.comparisons[declared][actual]++
	e.mu.Unlock()
}

func (e *responseUsageEvidence) Snapshot() responseUsageEvidenceSnapshot {
	if e == nil {
		return responseUsageEvidenceSnapshot{}
	}
	e.mu.Lock()
	snapshot := responseUsageEvidenceSnapshot{
		outcomes:    e.outcomes,
		comparisons: e.comparisons,
	}
	e.mu.Unlock()
	return snapshot
}

func writeResponseUsageEvidenceMetrics(w io.Writer, snapshot responseUsageEvidenceSnapshot) {
	for outcome, label := range responseUsageEvidenceOutcomeLabels {
		fmt.Fprintf(w, "pig_predictive_response_usage_outcomes_total{outcome=%q} %d\n", label, snapshot.outcomes[outcome])
	}
	for declared, declaredLabel := range declaredOutputTokenBucketLabels {
		for actual, actualLabel := range actualOutputTokenBucketLabels {
			fmt.Fprintf(
				w,
				"pig_predictive_output_limit_comparison_total{actual_bucket=%q,declared_bucket=%q} %d\n",
				actualLabel,
				declaredLabel,
				snapshot.comparisons[declared][actual],
			)
		}
	}
}

func (r *responseUsageRequestEvidence) WrapResponse(response *http.Response) {
	if r == nil || response == nil || response.Body == nil ||
		response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices ||
		!r.streamingKnown || !openai.CompletionUsageContentTypeEligible(response.Header.Get("Content-Type"), r.streaming) {
		return
	}
	r.mu.Lock()
	if r.completed || r.parserAttached {
		r.mu.Unlock()
		return
	}
	r.parserAttached = true
	r.mu.Unlock()
	response.Body = openai.ObserveCompletionUsageEvidenceBody(response.Body, r.streaming, r.observe)
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
	switch {
	case r.observed:
		outcome, actual = responseUsageEvidenceForObservation(r.observation)
	case result.status >= http.StatusOK && result.status < http.StatusMultipleChoices &&
		!result.proxyFailed && !result.timedOut && result.status != clientClosedRequestStatus:
		if !r.parserAttached {
			outcome = responseUsageEvidenceUnavailable
			actual = actualOutputTokensUnavailable
		}
	}
	r.completed = true
	owner := r.owner
	declared := r.declared
	r.mu.Unlock()
	owner.record(declared, outcome, actual)
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
	declared := r.declared
	r.mu.Unlock()
	owner.record(declared, responseUsageEvidenceCensored, actualOutputTokensCensored)
}

func responseUsageEvidenceForObservation(
	observation openai.CompletionUsageEvidence,
) (responseUsageEvidenceOutcome, actualOutputTokenBucket) {
	switch observation.Outcome {
	case openai.CompletionUsageAvailable:
		if observation.Usage.CompletionTokens > 0 {
			return responseUsageEvidenceAvailable, actualOutputTokenBucketFor(observation.Usage.CompletionTokens)
		}
	case openai.CompletionUsageUnavailable:
		return responseUsageEvidenceUnavailable, actualOutputTokensUnavailable
	}
	return responseUsageEvidenceMalformed, actualOutputTokensMalformed
}

func declaredOutputTokenBucketFor(tokens int64) declaredOutputTokenBucket {
	switch {
	case tokens <= 0:
		return declaredOutputTokensUnknown
	case tokens <= 64:
		return declaredOutputTokensLE64
	case tokens <= 256:
		return declaredOutputTokensLE256
	case tokens <= 1_024:
		return declaredOutputTokensLE1024
	case tokens <= 4_096:
		return declaredOutputTokensLE4096
	case tokens <= 16_384:
		return declaredOutputTokensLE16384
	default:
		return declaredOutputTokensGT16384
	}
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
