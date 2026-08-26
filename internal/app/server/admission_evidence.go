package server

import (
	"fmt"
	"io"
	"sync/atomic"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

type admissionEvidenceOutcome uint8

const (
	admissionEvidenceAdmitted admissionEvidenceOutcome = iota
	admissionEvidenceRequestProtected
	admissionEvidenceLoadProtected
	admissionEvidenceAvailabilityProtected
	admissionEvidenceOutcomeCount
)

var admissionEvidenceOutcomeLabels = [...]string{
	"admitted",
	"request_protected",
	"load_protected",
	"availability_protected",
}

type admissionEvidenceProtectionReason uint8

const (
	admissionEvidenceReasonUnknown admissionEvidenceProtectionReason = iota
	admissionEvidenceReasonControllerUnavailable
	admissionEvidenceReasonObservationMissing
	admissionEvidenceReasonObservationInvalid
	admissionEvidenceReasonObservationStale
	admissionEvidenceReasonInvalidRequest
	admissionEvidenceReasonTPSReference
	admissionEvidenceReasonRuntimeIdentityDrift
	admissionEvidenceReasonResourceExhausted
	admissionEvidenceReasonCounterOverflow
	admissionEvidenceReasonClosed
	admissionEvidenceProtectionReasonCount
)

var admissionEvidenceProtectionReasonLabels = [...]string{
	"unknown",
	"controller_unavailable",
	"observation_missing",
	"observation_invalid",
	"observation_stale",
	"invalid_request",
	"tps_reference",
	"runtime_identity_drift",
	"resource_exhausted",
	"counter_overflow",
	"closed",
}

type admissionEvidenceProtectionScope uint8

const (
	admissionEvidenceScopeUnknown admissionEvidenceProtectionScope = iota
	admissionEvidenceScopeRequest
	admissionEvidenceScopeLoad
	admissionEvidenceScopeAvailability
	admissionEvidenceProtectionScopeCount
)

var admissionEvidenceProtectionScopeLabels = [...]string{
	"unknown",
	"request",
	"load",
	"availability",
}

type admissionEvidenceFanoutBucket uint8

const (
	admissionEvidenceFanoutUnknown admissionEvidenceFanoutBucket = iota
	admissionEvidenceFanoutOne
	admissionEvidenceFanoutTwo
	admissionEvidenceFanoutThreeToFour
	admissionEvidenceFanoutFiveToEight
	admissionEvidenceFanoutNineToSixteen
	admissionEvidenceFanoutAboveSixteen
	admissionEvidenceFanoutBucketCount
)

var admissionEvidenceFanoutBucketLabels = [...]string{
	"unknown",
	"1",
	"2",
	"3-4",
	"5-8",
	"9-16",
	">16",
}

type admissionEvidence struct {
	outcomes     [admissionEvidenceOutcomeCount]atomic.Uint64
	protections  [admissionEvidenceProtectionReasonCount][admissionEvidenceProtectionScopeCount]atomic.Uint64
	decodeFanout [admissionEvidenceFanoutBucketCount][admissionEvidenceOutcomeCount]atomic.Uint64
}

type admissionEvidenceSnapshot struct {
	outcomes     [admissionEvidenceOutcomeCount]uint64
	protections  [admissionEvidenceProtectionReasonCount][admissionEvidenceProtectionScopeCount]uint64
	decodeFanout [admissionEvidenceFanoutBucketCount][admissionEvidenceOutcomeCount]uint64
}

func (e *admissionEvidence) Record(decision coreadmission.DecisionRecord) {
	if e == nil {
		return
	}
	outcome := admissionEvidenceOutcomeFor(decision)
	e.outcomes[outcome].Add(1)
	if !decision.Admitted() {
		reason := admissionEvidenceReasonFor(decision.Reason)
		scope := admissionEvidenceScopeFor(decision.Scope)
		e.protections[reason][scope].Add(1)
	}
	e.decodeFanout[admissionEvidenceFanoutFor(decision.Demand.DecodeSequences)][outcome].Add(1)
}

func (e *admissionEvidence) Snapshot() admissionEvidenceSnapshot {
	if e == nil {
		return admissionEvidenceSnapshot{}
	}
	var snapshot admissionEvidenceSnapshot
	for outcome := admissionEvidenceOutcome(0); outcome < admissionEvidenceOutcomeCount; outcome++ {
		snapshot.outcomes[outcome] = e.outcomes[outcome].Load()
	}
	for reason := admissionEvidenceProtectionReason(0); reason < admissionEvidenceProtectionReasonCount; reason++ {
		for scope := admissionEvidenceProtectionScope(0); scope < admissionEvidenceProtectionScopeCount; scope++ {
			snapshot.protections[reason][scope] = e.protections[reason][scope].Load()
		}
	}
	for fanout := admissionEvidenceFanoutBucket(0); fanout < admissionEvidenceFanoutBucketCount; fanout++ {
		for outcome := admissionEvidenceOutcome(0); outcome < admissionEvidenceOutcomeCount; outcome++ {
			snapshot.decodeFanout[fanout][outcome] = e.decodeFanout[fanout][outcome].Load()
		}
	}
	return snapshot
}

func writeAdmissionEvidenceMetrics(w io.Writer, snapshot admissionEvidenceSnapshot) {
	for outcome, label := range admissionEvidenceOutcomeLabels {
		fmt.Fprintf(w, "pig_predictive_admission_outcomes_total{outcome=%q} %d\n", label, snapshot.outcomes[outcome])
	}
	for reason, reasonLabel := range admissionEvidenceProtectionReasonLabels {
		for scope, scopeLabel := range admissionEvidenceProtectionScopeLabels {
			fmt.Fprintf(
				w,
				"pig_predictive_admission_protections_total{reason=%q,scope=%q} %d\n",
				reasonLabel,
				scopeLabel,
				snapshot.protections[reason][scope],
			)
		}
	}
	for fanout, fanoutLabel := range admissionEvidenceFanoutBucketLabels {
		for outcome, outcomeLabel := range admissionEvidenceOutcomeLabels {
			fmt.Fprintf(
				w,
				"pig_predictive_admission_decode_fanout_total{bucket=%q,outcome=%q} %d\n",
				fanoutLabel,
				outcomeLabel,
				snapshot.decodeFanout[fanout][outcome],
			)
		}
	}
}

func admissionEvidenceOutcomeFor(decision coreadmission.DecisionRecord) admissionEvidenceOutcome {
	if decision.Admitted() {
		return admissionEvidenceAdmitted
	}
	switch decision.Scope {
	case coreadmission.ProtectionRequest:
		return admissionEvidenceRequestProtected
	case coreadmission.ProtectionLoad:
		return admissionEvidenceLoadProtected
	default:
		return admissionEvidenceAvailabilityProtected
	}
}

func admissionEvidenceReasonFor(reason coreadmission.Reason) admissionEvidenceProtectionReason {
	switch reason {
	case coreadmission.ReasonControllerUnavailable:
		return admissionEvidenceReasonControllerUnavailable
	case coreadmission.ReasonObservationMissing:
		return admissionEvidenceReasonObservationMissing
	case coreadmission.ReasonObservationInvalid:
		return admissionEvidenceReasonObservationInvalid
	case coreadmission.ReasonObservationStale:
		return admissionEvidenceReasonObservationStale
	case coreadmission.ReasonInvalidRequest:
		return admissionEvidenceReasonInvalidRequest
	case coreadmission.ReasonTPSReference:
		return admissionEvidenceReasonTPSReference
	case coreadmission.ReasonRuntimeIdentityDrift:
		return admissionEvidenceReasonRuntimeIdentityDrift
	case coreadmission.ReasonResourceExhausted:
		return admissionEvidenceReasonResourceExhausted
	case coreadmission.ReasonCounterOverflow:
		return admissionEvidenceReasonCounterOverflow
	case coreadmission.ReasonClosed:
		return admissionEvidenceReasonClosed
	default:
		return admissionEvidenceReasonUnknown
	}
}

func admissionEvidenceScopeFor(scope coreadmission.ProtectionScope) admissionEvidenceProtectionScope {
	switch scope {
	case coreadmission.ProtectionRequest:
		return admissionEvidenceScopeRequest
	case coreadmission.ProtectionLoad:
		return admissionEvidenceScopeLoad
	case coreadmission.ProtectionAvailability:
		return admissionEvidenceScopeAvailability
	default:
		return admissionEvidenceScopeUnknown
	}
}

func admissionEvidenceFanoutFor(sequences int64) admissionEvidenceFanoutBucket {
	switch {
	case sequences <= 0:
		return admissionEvidenceFanoutUnknown
	case sequences == 1:
		return admissionEvidenceFanoutOne
	case sequences == 2:
		return admissionEvidenceFanoutTwo
	case sequences <= 4:
		return admissionEvidenceFanoutThreeToFour
	case sequences <= 8:
		return admissionEvidenceFanoutFiveToEight
	case sequences <= 16:
		return admissionEvidenceFanoutNineToSixteen
	default:
		return admissionEvidenceFanoutAboveSixteen
	}
}
