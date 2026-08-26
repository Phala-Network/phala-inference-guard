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
	classifierEvidenceShapeScanLimit
	classifierEvidenceUnsupportedEndpoint
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
	"shape_scan_limit",
	"unsupported_endpoint",
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

type requestEvidence struct {
	classifierOutcomes [classifierEvidenceOutcomeCount]atomic.Uint64
	streamingStates    [streamingEvidenceStateCount]atomic.Uint64
	decodeFanout       [admissionEvidenceFanoutBucketCount]atomic.Uint64
}

type requestEvidenceSnapshot struct {
	classifierOutcomes [classifierEvidenceOutcomeCount]uint64
	streamingStates    [streamingEvidenceStateCount]uint64
	decodeFanout       [admissionEvidenceFanoutBucketCount]uint64
}

func (e *requestEvidence) Record(classification apprequest.Classification) {
	if e == nil {
		return
	}
	e.classifierOutcomes[classifierEvidenceOutcomeFor(classification)].Add(1)
	e.streamingStates[streamingEvidenceStateFor(classification)].Add(1)
	e.decodeFanout[admissionEvidenceFanoutFor(classification.DecodeSequences)].Add(1)
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
}

func classifierEvidenceOutcomeFor(classification apprequest.Classification) classifierEvidenceOutcome {
	if classification.Supported {
		return classifierEvidenceSupported
	}
	switch classification.UnsupportedReason {
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
	case "shape_scan_limit":
		return classifierEvidenceShapeScanLimit
	case "unsupported_endpoint":
		return classifierEvidenceUnsupportedEndpoint
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
