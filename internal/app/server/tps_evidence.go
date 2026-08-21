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
	tpsEvidenceSubreasonIdle
	tpsEvidenceSubreasonBaseRate
	tpsEvidenceSubreasonCurrentRate
	tpsEvidenceSubreasonQoSBudgetGranted
	tpsEvidenceSubreasonQoSBudgetOutputUnknown
	tpsEvidenceSubreasonQoSBudgetMultiSequence
	tpsEvidenceSubreasonQoSBudgetUnobserved
	tpsEvidenceSubreasonQoSBudgetActiveLease
	tpsEvidenceSubreasonQoSBudgetWaveLimit
	tpsEvidenceSubreasonQoSBudgetNoSurplus
	tpsEvidenceSubreasonQoSBudgetInvalidRate
	tpsEvidenceSubreasonQoSBudgetLifetime
	tpsEvidenceSubreasonQoSBudgetIneligible
	tpsEvidenceSubreasonCount
)

var tpsEvidenceSubreasonLabels = [...]string{
	"unknown",
	"disabled",
	"invalid_state",
	"waiting",
	"preemption",
	"warming",
	"idle",
	"base_rate",
	"current_rate",
	"qos_budget_granted",
	"qos_budget_output_unknown",
	"qos_budget_multi_sequence",
	"qos_budget_unobserved",
	"qos_budget_active_lease",
	"qos_budget_wave_limit",
	"qos_budget_no_surplus",
	"qos_budget_invalid_rate",
	"qos_budget_lifetime",
	"qos_budget_ineligible",
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
	case coreadmission.TPSDecisionSubreasonIdle:
		return tpsEvidenceSubreasonIdle
	case coreadmission.TPSDecisionSubreasonBaseRate:
		return tpsEvidenceSubreasonBaseRate
	case coreadmission.TPSDecisionSubreasonCurrentRate:
		return tpsEvidenceSubreasonCurrentRate
	case coreadmission.TPSDecisionSubreasonQoSBudgetGranted:
		return tpsEvidenceSubreasonQoSBudgetGranted
	case coreadmission.TPSDecisionSubreasonQoSBudgetOutputUnknown:
		return tpsEvidenceSubreasonQoSBudgetOutputUnknown
	case coreadmission.TPSDecisionSubreasonQoSBudgetMultiSequence:
		return tpsEvidenceSubreasonQoSBudgetMultiSequence
	case coreadmission.TPSDecisionSubreasonQoSBudgetUnobserved:
		return tpsEvidenceSubreasonQoSBudgetUnobserved
	case coreadmission.TPSDecisionSubreasonQoSBudgetActiveLease:
		return tpsEvidenceSubreasonQoSBudgetActiveLease
	case coreadmission.TPSDecisionSubreasonQoSBudgetWaveLimit:
		return tpsEvidenceSubreasonQoSBudgetWaveLimit
	case coreadmission.TPSDecisionSubreasonQoSBudgetNoSurplus:
		return tpsEvidenceSubreasonQoSBudgetNoSurplus
	case coreadmission.TPSDecisionSubreasonQoSBudgetInvalidRate:
		return tpsEvidenceSubreasonQoSBudgetInvalidRate
	case coreadmission.TPSDecisionSubreasonQoSBudgetLifetime:
		return tpsEvidenceSubreasonQoSBudgetLifetime
	case coreadmission.TPSDecisionSubreasonQoSBudgetIneligible:
		return tpsEvidenceSubreasonQoSBudgetIneligible
	default:
		return tpsEvidenceSubreasonUnknown
	}
}
