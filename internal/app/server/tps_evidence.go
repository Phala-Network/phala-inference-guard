package server

import (
	"fmt"
	"io"
	"sync/atomic"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

type tpsEvidenceResult uint8

const (
	tpsEvidenceResultUnknown tpsEvidenceResult = iota
	tpsEvidenceResultDisabled
	tpsEvidenceResultAdmit
	tpsEvidenceResultProtect
	tpsEvidenceResultInvalid
	tpsEvidenceResultCount
)

var tpsEvidenceResultLabels = [...]string{
	"unknown",
	"disabled",
	"admit",
	"protect",
	"invalid",
}

type tpsEvidenceSubreason uint8

const (
	tpsEvidenceSubreasonUnknown tpsEvidenceSubreason = iota
	tpsEvidenceSubreasonDisabled
	tpsEvidenceSubreasonInvalidState
	tpsEvidenceSubreasonWaiting
	tpsEvidenceSubreasonPreemption
	tpsEvidenceSubreasonWarming
	tpsEvidenceSubreasonNoCurrentEvidence
	tpsEvidenceSubreasonHealthyWindow
	tpsEvidenceSubreasonRecoveredCurrent
	tpsEvidenceSubreasonBelowReference
	tpsEvidenceSubreasonCount
)

var tpsEvidenceSubreasonLabels = [...]string{
	"unknown",
	"disabled",
	"invalid_state",
	"waiting",
	"preemption",
	"warming",
	"no_current_evidence",
	"healthy_window",
	"recovered_current",
	"below_reference",
}

type tpsDecisionEvidence struct {
	decisions [tpsEvidenceResultCount][tpsEvidenceSubreasonCount]atomic.Uint64
}

type tpsDecisionEvidenceSnapshot struct {
	decisions [tpsEvidenceResultCount][tpsEvidenceSubreasonCount]uint64
}

func (e *tpsDecisionEvidence) Record(decision coreadmission.DecisionRecord) {
	if e == nil {
		return
	}
	result := tpsEvidenceResultFor(decision.TPSDecisionResult)
	subreason := tpsEvidenceSubreasonFor(decision.TPSDecisionSubreason)
	e.decisions[result][subreason].Add(1)
}

func (e *tpsDecisionEvidence) Snapshot() tpsDecisionEvidenceSnapshot {
	if e == nil {
		return tpsDecisionEvidenceSnapshot{}
	}
	var snapshot tpsDecisionEvidenceSnapshot
	for result := tpsEvidenceResult(0); result < tpsEvidenceResultCount; result++ {
		for subreason := tpsEvidenceSubreason(0); subreason < tpsEvidenceSubreasonCount; subreason++ {
			snapshot.decisions[result][subreason] = e.decisions[result][subreason].Load()
		}
	}
	return snapshot
}

func writeTPSDecisionEvidenceMetrics(w io.Writer, snapshot tpsDecisionEvidenceSnapshot) {
	for result, resultLabel := range tpsEvidenceResultLabels {
		for subreason, subreasonLabel := range tpsEvidenceSubreasonLabels {
			fmt.Fprintf(
				w,
				"pig_predictive_tps_decisions_total{result=%q,subreason=%q} %d\n",
				resultLabel,
				subreasonLabel,
				snapshot.decisions[result][subreason],
			)
		}
	}
}

func writeTPSDenominatorEvidenceMetrics(w io.Writer, evidence coreadmission.TPSDenominatorEvidence) {
	selections := [...]struct {
		label string
		value uint64
	}{
		{label: "endpoint", value: evidence.EndpointSelections},
		{label: "local_forwarded", value: evidence.LocalForwardedSelections},
		{label: "local_response", value: evidence.LocalResponseSelections},
		{label: "fallback_liability", value: evidence.FallbackLiabilitySelections},
		{label: "tie", value: evidence.TieSelections},
		{label: "none", value: evidence.NoneSelections},
	}
	for _, selection := range selections {
		fmt.Fprintf(
			w,
			"pig_predictive_tps_denominator_selections_total{source=%q} %d\n",
			selection.label,
			selection.value,
		)
	}
	seconds := [...]struct {
		label string
		value float64
	}{
		{label: "endpoint", value: evidence.EndpointSequenceSeconds},
		{label: "local_forwarded", value: evidence.LocalForwardedSeconds},
		{label: "local_response", value: evidence.LocalResponseSeconds},
		{label: "fallback_liability", value: evidence.FallbackLiabilitySeconds},
		{label: "selected", value: evidence.SelectedSequenceSeconds},
	}
	for _, source := range seconds {
		fmt.Fprintf(
			w,
			"pig_predictive_tps_denominator_sequence_seconds_total{source=%q} %.6f\n",
			source.label,
			source.value,
		)
	}
}

func tpsEvidenceResultFor(result coreadmission.TPSDecisionResult) tpsEvidenceResult {
	switch result {
	case coreadmission.TPSDecisionResultDisabled:
		return tpsEvidenceResultDisabled
	case coreadmission.TPSDecisionResultAdmit:
		return tpsEvidenceResultAdmit
	case coreadmission.TPSDecisionResultProtect:
		return tpsEvidenceResultProtect
	case coreadmission.TPSDecisionResultInvalid:
		return tpsEvidenceResultInvalid
	default:
		return tpsEvidenceResultUnknown
	}
}

func tpsEvidenceSubreasonFor(subreason coreadmission.TPSDecisionSubreason) tpsEvidenceSubreason {
	switch subreason {
	case coreadmission.TPSDecisionSubreasonDisabled:
		return tpsEvidenceSubreasonDisabled
	case coreadmission.TPSDecisionSubreasonInvalidState:
		return tpsEvidenceSubreasonInvalidState
	case coreadmission.TPSDecisionSubreasonWaiting:
		return tpsEvidenceSubreasonWaiting
	case coreadmission.TPSDecisionSubreasonPreemption:
		return tpsEvidenceSubreasonPreemption
	case coreadmission.TPSDecisionSubreasonWarming:
		return tpsEvidenceSubreasonWarming
	case coreadmission.TPSDecisionSubreasonNoCurrentEvidence:
		return tpsEvidenceSubreasonNoCurrentEvidence
	case coreadmission.TPSDecisionSubreasonHealthyWindow:
		return tpsEvidenceSubreasonHealthyWindow
	case coreadmission.TPSDecisionSubreasonRecoveredCurrent:
		return tpsEvidenceSubreasonRecoveredCurrent
	case coreadmission.TPSDecisionSubreasonBelowReference:
		return tpsEvidenceSubreasonBelowReference
	default:
		return tpsEvidenceSubreasonUnknown
	}
}
