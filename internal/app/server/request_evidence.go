package server

import (
	"fmt"
	"io"
	"sync/atomic"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
)

type classifierEvidenceOutcome uint8

const (
	classifierEvidenceSupported classifierEvidenceOutcome = iota
	classifierEvidenceBodyNotScannable
	classifierEvidenceBodyTooLarge
	classifierEvidenceUnsupportedContentType
	classifierEvidenceSaturated
	classifierEvidenceBodyReadFailed
	classifierEvidenceInvalidJSON
	classifierEvidenceUnsupportedRequestShape
	classifierEvidenceUnsupportedEndpoint
	classifierEvidenceBodyExternalContext
	classifierEvidenceInvalidEstimatorConfig
	classifierEvidenceInvalidRequestShape
	classifierEvidenceEmptyBody
	classifierEvidenceEstimateOverflow
	classifierEvidenceUnknown
	classifierEvidenceOutcomeCount
)

var classifierEvidenceOutcomeLabels = [...]string{
	"supported",
	"body_not_scannable",
	"body_too_large",
	"unsupported_content_type",
	"classifier_saturated",
	"body_read_failed",
	"invalid_json",
	"unsupported_request_shape",
	"unsupported_endpoint",
	"body_external_context",
	"invalid_estimator_config",
	"invalid_request_shape",
	"empty_body",
	"request_estimate_overflow",
	"unknown",
}

type streamingEvidenceState uint8

const (
	streamingEvidenceUnknown streamingEvidenceState = iota
	streamingEvidenceUnspecified
	streamingEvidenceTrue
	streamingEvidenceFalse
	streamingEvidenceInvalid
	streamingEvidenceStateCount
)

var streamingEvidenceStateLabels = [...]string{
	"unknown",
	"unspecified",
	"true",
	"false",
	"invalid",
}

type estimatorValidationKind uint8

const (
	estimatorValidationSelectionInput estimatorValidationKind = iota
	estimatorValidationContextUpperBound
	estimatorValidationKVReservation
	estimatorValidationKindCount
)

var estimatorValidationKindLabels = [...]string{
	"selection_input",
	"context_upper_bound",
	"kv_reservation",
}

type estimatorValidationResult uint8

const (
	estimatorValidationKnown estimatorValidationResult = iota
	estimatorValidationUnknown
	estimatorValidationBodyNotScannable
	estimatorValidationBodyTooLarge
	estimatorValidationUnsupportedContentType
	estimatorValidationSaturated
	estimatorValidationBodyReadFailed
	estimatorValidationInvalidJSON
	estimatorValidationUnsupportedRequestShape
	estimatorValidationUnsupportedEndpoint
	estimatorValidationBodyExternalContext
	estimatorValidationInvalidEstimatorConfig
	estimatorValidationInvalidRequestShape
	estimatorValidationEmptyBody
	estimatorValidationEstimateOverflow
	estimatorValidationResultCount
)

var estimatorValidationResultLabels = [...]string{
	"known",
	"unknown",
	"body_not_scannable",
	"body_too_large",
	"unsupported_content_type",
	"classifier_saturated",
	"body_read_failed",
	"invalid_json",
	"unsupported_request_shape",
	"unsupported_endpoint",
	"body_external_context",
	"invalid_estimator_config",
	"invalid_request_shape",
	"empty_body",
	"request_estimate_overflow",
}

type requestEvidence struct {
	classifierOutcomes  [classifierEvidenceOutcomeCount]atomic.Uint64
	streamingStates     [streamingEvidenceStateCount]atomic.Uint64
	decodeFanout        [admissionEvidenceFanoutBucketCount]atomic.Uint64
	estimatorValidation [estimatorValidationKindCount][estimatorValidationResultCount]atomic.Uint64
}

type requestEvidenceSnapshot struct {
	classifierOutcomes  [classifierEvidenceOutcomeCount]uint64
	streamingStates     [streamingEvidenceStateCount]uint64
	decodeFanout        [admissionEvidenceFanoutBucketCount]uint64
	estimatorValidation [estimatorValidationKindCount][estimatorValidationResultCount]uint64
}

func (e *requestEvidence) Record(classification apprequest.Classification) {
	if e == nil {
		return
	}
	e.classifierOutcomes[classifierEvidenceOutcomeFor(classification)].Add(1)
	e.streamingStates[streamingEvidenceStateFor(classification)].Add(1)
	e.decodeFanout[admissionEvidenceFanoutFor(classification.DecodeSequences)].Add(1)
	for kind := estimatorValidationKind(0); kind < estimatorValidationKindCount; kind++ {
		result := estimatorValidationResultFor(classification, kind)
		e.estimatorValidation[kind][result].Add(1)
	}
}

func (e *requestEvidence) Snapshot() requestEvidenceSnapshot {
	if e == nil {
		return requestEvidenceSnapshot{}
	}
	var snapshot requestEvidenceSnapshot
	for outcome := classifierEvidenceOutcome(0); outcome < classifierEvidenceOutcomeCount; outcome++ {
		snapshot.classifierOutcomes[outcome] = e.classifierOutcomes[outcome].Load()
	}
	for state := streamingEvidenceState(0); state < streamingEvidenceStateCount; state++ {
		snapshot.streamingStates[state] = e.streamingStates[state].Load()
	}
	for bucket := admissionEvidenceFanoutBucket(0); bucket < admissionEvidenceFanoutBucketCount; bucket++ {
		snapshot.decodeFanout[bucket] = e.decodeFanout[bucket].Load()
	}
	for kind := estimatorValidationKind(0); kind < estimatorValidationKindCount; kind++ {
		for result := estimatorValidationResult(0); result < estimatorValidationResultCount; result++ {
			snapshot.estimatorValidation[kind][result] = e.estimatorValidation[kind][result].Load()
		}
	}
	return snapshot
}

func writeRequestEvidenceMetrics(w io.Writer, snapshot requestEvidenceSnapshot) {
	for outcome, label := range classifierEvidenceOutcomeLabels {
		fmt.Fprintf(w, "pig_predictive_classifier_outcomes_total{outcome=%q} %d\n", label, snapshot.classifierOutcomes[outcome])
	}
	for state, label := range streamingEvidenceStateLabels {
		fmt.Fprintf(w, "pig_predictive_request_streaming_total{state=%q} %d\n", label, snapshot.streamingStates[state])
	}
	for bucket, label := range admissionEvidenceFanoutBucketLabels {
		fmt.Fprintf(w, "pig_predictive_request_decode_fanout_total{bucket=%q} %d\n", label, snapshot.decodeFanout[bucket])
	}
	for kind, kindLabel := range estimatorValidationKindLabels {
		for result, resultLabel := range estimatorValidationResultLabels {
			fmt.Fprintf(
				w,
				"pig_predictive_estimator_validation_total{estimate_kind=%q,result=%q} %d\n",
				kindLabel,
				resultLabel,
				snapshot.estimatorValidation[kind][result],
			)
		}
	}
}

func classifierEvidenceOutcomeFor(classification apprequest.Classification) classifierEvidenceOutcome {
	if classification.Cost.Supported {
		return classifierEvidenceSupported
	}
	switch classification.Cost.UnsupportedReason {
	case "body_not_scannable":
		return classifierEvidenceBodyNotScannable
	case "body_too_large":
		return classifierEvidenceBodyTooLarge
	case "unsupported_content_type":
		return classifierEvidenceUnsupportedContentType
	case "classifier_saturated":
		return classifierEvidenceSaturated
	case "body_read_failed":
		return classifierEvidenceBodyReadFailed
	case "invalid_json":
		return classifierEvidenceInvalidJSON
	case "unsupported_request_shape":
		return classifierEvidenceUnsupportedRequestShape
	case "unsupported_endpoint":
		return classifierEvidenceUnsupportedEndpoint
	case "body_external_context":
		return classifierEvidenceBodyExternalContext
	case "invalid_estimator_config":
		return classifierEvidenceInvalidEstimatorConfig
	case "invalid_request_shape":
		return classifierEvidenceInvalidRequestShape
	case "empty_body":
		return classifierEvidenceEmptyBody
	case "request_estimate_overflow":
		return classifierEvidenceEstimateOverflow
	default:
		return classifierEvidenceUnknown
	}
}

func streamingEvidenceStateFor(classification apprequest.Classification) streamingEvidenceState {
	if !classification.JSONFieldsKnown {
		return streamingEvidenceUnknown
	}
	if !classification.StreamingPresent {
		return streamingEvidenceUnspecified
	}
	if !classification.StreamingKnown {
		return streamingEvidenceInvalid
	}
	if classification.Streaming {
		return streamingEvidenceTrue
	}
	return streamingEvidenceFalse
}

func estimatorValidationResultFor(
	classification apprequest.Classification,
	kind estimatorValidationKind,
) estimatorValidationResult {
	if !classification.Cost.Supported {
		return estimatorValidationUnsupportedResult(classifierEvidenceOutcomeFor(classification))
	}
	estimate := classification.Cost.Estimate
	switch kind {
	case estimatorValidationSelectionInput:
		if estimate.SelectionInputTokens > 0 {
			return estimatorValidationKnown
		}
	case estimatorValidationContextUpperBound:
		if estimate.MaximumSequenceInputTokens > 0 {
			return estimatorValidationKnown
		}
	case estimatorValidationKVReservation:
		if estimate.KVReservationInputTokens > 0 && estimate.MaximumSequenceKVReservationInputTokens > 0 {
			return estimatorValidationKnown
		}
	}
	return estimatorValidationUnknown
}

func estimatorValidationUnsupportedResult(outcome classifierEvidenceOutcome) estimatorValidationResult {
	switch outcome {
	case classifierEvidenceBodyNotScannable:
		return estimatorValidationBodyNotScannable
	case classifierEvidenceBodyTooLarge:
		return estimatorValidationBodyTooLarge
	case classifierEvidenceUnsupportedContentType:
		return estimatorValidationUnsupportedContentType
	case classifierEvidenceSaturated:
		return estimatorValidationSaturated
	case classifierEvidenceBodyReadFailed:
		return estimatorValidationBodyReadFailed
	case classifierEvidenceInvalidJSON:
		return estimatorValidationInvalidJSON
	case classifierEvidenceUnsupportedRequestShape:
		return estimatorValidationUnsupportedRequestShape
	case classifierEvidenceUnsupportedEndpoint:
		return estimatorValidationUnsupportedEndpoint
	case classifierEvidenceBodyExternalContext:
		return estimatorValidationBodyExternalContext
	case classifierEvidenceInvalidEstimatorConfig:
		return estimatorValidationInvalidEstimatorConfig
	case classifierEvidenceInvalidRequestShape:
		return estimatorValidationInvalidRequestShape
	case classifierEvidenceEmptyBody:
		return estimatorValidationEmptyBody
	case classifierEvidenceEstimateOverflow:
		return estimatorValidationEstimateOverflow
	default:
		return estimatorValidationUnknown
	}
}
