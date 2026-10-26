package server

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
)

func TestAdmissionEvidenceNamesCountBoundReasons(t *testing.T) {
	var evidence admissionEvidence
	for _, reason := range []coreadmission.Reason{coreadmission.ReasonRunningLimit, coreadmission.ReasonWindowConcurrency} {
		evidence.Record(coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect,
			Reason: reason,
			Scope:  coreadmission.ProtectionLoad,
			Demand: coreadmission.TPSRequestDemand{DecodeSequences: 1},
		})
	}
	var output bytes.Buffer
	writeAdmissionEvidenceMetrics(&output, evidence.Snapshot())
	body := output.String()
	for _, want := range []string{
		`pig_predictive_admission_protections_total{reason="running_limit",scope="load"} 1`,
		`pig_predictive_admission_protections_total{reason="window_concurrency",scope="load"} 1`,
		`pig_predictive_admission_protections_total{reason="unknown",scope="load"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q\n%s", want, body)
		}
	}
}

func TestAdmissionEvidenceCountsOutcomesAndProtectionsByTier(t *testing.T) {
	var evidence admissionEvidence
	var empty bytes.Buffer
	writeAdmissionEvidenceMetrics(&empty, evidence.Snapshot())
	record := func(tier coreadmission.RequestTier, admitted bool) {
		decision := coreadmission.DecisionRecord{
			Action: coreadmission.ActionAdmit,
			Reason: coreadmission.ReasonOpen,
			Demand: coreadmission.TPSRequestDemand{DecodeSequences: 1, Tier: tier},
		}
		if !admitted {
			decision.Action = coreadmission.ActionProtect
			decision.Reason = coreadmission.ReasonTPSReference
			decision.Scope = coreadmission.ProtectionLoad
		}
		evidence.Record(decision)
	}
	record(coreadmission.TierPriority, true)
	record(coreadmission.TierDefault, false)
	record(coreadmission.TierFlex, false)
	record(coreadmission.TierFlex, false)
	record(coreadmission.RequestTier(99), true)

	var output bytes.Buffer
	writeAdmissionEvidenceMetrics(&output, evidence.Snapshot())
	body := output.String()
	if strings.Count(empty.String(), "\n") != strings.Count(body, "\n") {
		t.Fatalf("tier evidence changed cardinality")
	}
	for _, want := range []string{
		`pig_predictive_admission_tier_outcomes_total{tier="priority",outcome="admitted"} 1`,
		`pig_predictive_admission_tier_outcomes_total{tier="default",outcome="admitted"} 1`,
		`pig_predictive_admission_tier_outcomes_total{tier="default",outcome="load_protected"} 1`,
		`pig_predictive_admission_tier_outcomes_total{tier="flex",outcome="load_protected"} 2`,
		`pig_predictive_admission_tier_protections_total{tier="flex",reason="tps_reference",scope="load"} 2`,
		`pig_predictive_admission_tier_protections_total{tier="priority",reason="tps_reference",scope="load"} 0`,
		`pig_predictive_admission_protections_total{reason="tps_reference",scope="load"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q\n%s", want, body)
		}
	}
}

func TestSuccessfulCompletionTokensAreCountedByTier(t *testing.T) {
	var evidence responseUsageEvidence
	for tier, tokens := range map[coreadmission.RequestTier]int64{
		coreadmission.TierPriority: 10,
		coreadmission.TierFlex:     7,
	} {
		request := evidence.Begin(apprequest.Classification{}, "/v1/chat/completions", tier)
		request.observe(openai.CompletionUsageEvidence{
			Outcome: openai.CompletionUsageAvailable,
			Usage:   openai.CompletionUsage{CompletionTokens: tokens},
		})
		request.Complete(proxyResult{status: http.StatusOK})
	}
	failed := evidence.Begin(apprequest.Classification{}, "/v1/chat/completions", coreadmission.TierDefault)
	failed.observe(openai.CompletionUsageEvidence{
		Outcome: openai.CompletionUsageAvailable,
		Usage:   openai.CompletionUsage{CompletionTokens: 5},
	})
	failed.Complete(proxyResult{status: http.StatusTooManyRequests})

	var output bytes.Buffer
	writeResponseUsageEvidenceMetrics(&output, evidence.Snapshot())
	body := output.String()
	for _, want := range []string{
		`pig_predictive_successful_completion_tokens_total 17`,
		`pig_predictive_tier_successful_completion_tokens_total{tier="priority"} 10`,
		`pig_predictive_tier_successful_completion_tokens_total{tier="default"} 0`,
		`pig_predictive_tier_successful_completion_tokens_total{tier="flex"} 7`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q\n%s", want, body)
		}
	}
}
