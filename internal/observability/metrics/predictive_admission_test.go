package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
)

func TestWritePredictiveAdmissionExposesCurrentOperationalState(t *testing.T) {
	prediction := histogram.NewPredictiveDurationHistogram()
	bodyRead := histogram.NewPredictiveDurationHistogram()
	estimator := histogram.NewPredictiveDurationHistogram()
	preForward := histogram.NewPredictiveDurationHistogram()
	prediction.Observe(3 * time.Millisecond)
	bodyRead.Observe(2 * time.Millisecond)
	estimator.Observe(4 * time.Microsecond)
	preForward.Observe(5 * time.Millisecond)
	var out bytes.Buffer
	WritePredictiveAdmission(&out, PredictiveAdmissionInput{
		Mode:                                            "enforce",
		CapabilityProfileSource:                         "automatic",
		CapabilityProfileSchema:                         "request-aware-capability-v3",
		CapabilityInitializationReason:                  "metadata",
		CapabilityKVCapacityTokens:                      1_000_000,
		CapabilityKVBlockSize:                           64,
		CapabilityKVHardLimitTokens:                     880_000,
		CapabilityMaxModelLenTokens:                     650 * 1024,
		CapabilityMaximumAdmissibleInputTokens:          650*1024 - 256,
		CapabilityPrefillRegularTokens:                  64 * 1024,
		CapabilityPrefillExclusiveTokens:                256 * 1024,
		CapabilityPrefillQuiescentTokens:                512 * 1024,
		CapabilityPrefillContendedBudgetTokens:          64 * 1024,
		CapabilityPrefillAggregateBudgetTokens:          256 * 1024,
		Attempts:                                        9,
		Fits:                                            5,
		Risks:                                           3,
		Unknown:                                         1,
		EnforcedRejects:                                 4,
		LastReason:                                      "request_size",
		LastSource:                                      "deterministic",
		LastRejectReason:                                "request_size",
		LastRejectSource:                                "deterministic",
		LastRejectScope:                                 "request",
		LastRejectAt:                                    time.Unix(102, 0),
		IntakeOpen:                                      true,
		Reservations:                                    2,
		VirtualDecodeSequences:                          3,
		ForwardedPendingPrefills:                        1,
		ForwardedPendingPrefillTokens:                   100,
		RetiredReservations:                             3,
		RetiredEvictions:                                1,
		FailureForward:                                  3,
		FailurePrefill:                                  4,
		FailureTerminal:                                 6,
		PredictionDuration:                              &prediction,
		BodyReadDuration:                                &bodyRead,
		EstimatorDuration:                               &estimator,
		PreForwardDuration:                              &preForward,
		RequestAwareAction:                              "size_protect",
		RequestAwareReason:                              "prefill_busy",
		RequestAwarePressureSource:                      "prefill",
		RequestAwarePressure:                            1,
		RequestAwareSelectionInputTokens:                1500,
		RequestAwareReservedTokens:                      1600,
		RequestAwareAllowanceTokens:                     0,
		RequestAwareEffectiveKV:                         7000,
		RequestAwarePostAdmitKV:                         8600,
		RequestAwareRemainingKV:                         2000,
		RequestAwareRunning:                             4,
		RequestAwareWaiting:                             1,
		RequestAwareEffectiveSequences:                  4,
		RequestAwareAggregateTPSProxy:                   80,
		RequestAwareMeanActiveTPSProxy:                  20,
		RequestAwarePrefillClass:                        "weighted",
		RequestAwareEstimatedPrefillTokens:              1500,
		RequestAwarePendingPrefillSequences:             2,
		RequestAwarePendingPrefillTokens:                2000,
		RequestAwarePostAdmitPendingPrefillTokens:       3500,
		RequestAwarePendingLongPrefillSequences:         1,
		RequestAwarePendingQuiescentPrefillSequences:    0,
		RequestAwareLastDecisionPendingPrefillSequences: 3,
		RequestAwareLastDecisionPendingPrefillTokens:    3000,
		RequestAwareLastDecisionPostAdmitPendingPrefillTokens:    4500,
		RequestAwareLastDecisionPendingLongPrefillSequences:      2,
		RequestAwareLastDecisionPendingQuiescentPrefillSequences: 1,
		RouterBackpressure: PredictiveRouterBackpressureInput{
			Active: true, Activation: 2, Scope: "load", Applied: true,
			Reason: "request_size", Source: "deterministic", InspectCapacity: 1,
			ActivatedAt: time.Unix(100, 0), Activations: 2, LatestRejectAt: time.Unix(102, 0),
			PredictiveRunning: 3, RawRunning: 1, EffectiveRunning: 3,
			RawGlobalLimit: 50, EffectiveGlobalLimit: 4,
		},
	})

	got := out.String()
	for _, want := range []string{
		`pig_predictive_admission_mode_info{mode="enforce"} 1`,
		`pig_predictive_capability_profile_info{schema="request-aware-capability-v3",source="automatic",reason="metadata"} 1`,
		"pig_predictive_capability_kv_capacity_tokens 1000000",
		"pig_predictive_capability_kv_block_size 64",
		"pig_predictive_capability_kv_hard_limit_tokens 880000",
		"pig_predictive_capability_max_model_len_tokens 665600",
		"pig_predictive_capability_maximum_admissible_input_tokens 665344",
		"pig_predictive_capability_prefill_regular_tokens 65536",
		"pig_predictive_capability_prefill_exclusive_tokens 262144",
		"pig_predictive_capability_prefill_quiescent_tokens 524288",
		"pig_predictive_capability_prefill_contended_budget_tokens 65536",
		"pig_predictive_capability_prefill_aggregate_budget_tokens 262144",
		"pig_predictive_admission_enforce 1",
		"pig_predictive_admission_attempts_total 9",
		`pig_predictive_admission_decisions_total{decision="fit"} 5`,
		`pig_predictive_admission_decisions_total{decision="risk"} 3`,
		`pig_predictive_admission_decisions_total{decision="unknown"} 1`,
		"pig_predictive_admission_enforced_rejects_total 4",
		`pig_predictive_admission_last_decision_info{reason="request_size",source="deterministic"} 1`,
		`pig_predictive_admission_last_reject_info{reason="request_size",source="deterministic",scope="request"} 1`,
		"pig_predictive_admission_reservations 2",
		"pig_predictive_admission_virtual_decode_sequences 3",
		"pig_predictive_admission_forwarded_pending_prefills 1",
		"pig_predictive_admission_forwarded_pending_prefill_tokens 100",
		"pig_predictive_admission_intake_open 1",
		"pig_predictive_admission_retired_evictions_total 1",
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		`pig_predictive_router_backpressure_state_info{scope="load",reason="request_size",source="deterministic"} 1`,
		"pig_predictive_router_backpressure_activation 2",
		"pig_predictive_router_backpressure_activations_total 2",
		"pig_predictive_router_backpressure_latest_load_reject_at_seconds 102.000000",
		"pig_predictive_router_backpressure_predictive_running 3",
		"pig_predictive_router_backpressure_raw_running 1",
		"pig_predictive_router_backpressure_effective_running 3",
		"pig_predictive_router_backpressure_raw_global_limit 50",
		"pig_predictive_router_backpressure_effective_global_limit 4",
		`pig_predictive_request_aware_last_decision_info{action="size_protect",reason="prefill_busy",pressure_source="prefill",prefill_class="weighted"} 1`,
		"pig_predictive_request_aware_pressure 1.000000",
		"pig_predictive_request_aware_selection_input_tokens 1500",
		"pig_predictive_request_aware_reserved_tokens 1600",
		"pig_predictive_request_aware_allowance_tokens 0",
		"pig_predictive_request_aware_effective_kv_tokens 7000",
		"pig_predictive_request_aware_post_admit_kv_tokens 8600",
		"pig_predictive_request_aware_remaining_kv_tokens 2000",
		"pig_predictive_request_aware_running 4",
		"pig_predictive_request_aware_waiting 1",
		"pig_predictive_request_aware_effective_sequences 4",
		"pig_predictive_request_aware_aggregate_tps_proxy 80.000000",
		"pig_predictive_request_aware_mean_active_tps_proxy 20.000000",
		"pig_predictive_request_aware_estimated_prefill_tokens 1500",
		"pig_predictive_request_aware_pending_prefill_sequences 2",
		"pig_predictive_request_aware_pending_prefill_tokens 2000",
		"pig_predictive_request_aware_post_admit_pending_prefill_tokens 3500",
		"pig_predictive_request_aware_pending_long_prefill_sequences 1",
		"pig_predictive_request_aware_pending_quiescent_prefill_sequences 0",
		"pig_predictive_request_aware_last_decision_pending_prefill_sequences 3",
		"pig_predictive_request_aware_last_decision_pending_prefill_tokens 3000",
		"pig_predictive_request_aware_last_decision_post_admit_pending_prefill_tokens 4500",
		"pig_predictive_request_aware_last_decision_pending_long_prefill_sequences 2",
		"pig_predictive_request_aware_last_decision_pending_quiescent_prefill_sequences 1",
		"pig_predictive_router_inspect_capacity 1",
		`pig_predictive_admission_failures_total{phase="forward"} 3`,
		`pig_predictive_admission_failures_total{phase="prefill"} 4`,
		`pig_predictive_admission_failures_total{phase="terminal"} 6`,
		"pig_predictive_admission_prediction_duration_seconds_count 1",
		"pig_predictive_admission_body_read_duration_seconds_count 1",
		"pig_predictive_admission_estimator_duration_seconds_count 1",
		"pig_predictive_admission_pre_forward_duration_seconds_count 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, got)
		}
	}
}

func TestWritePredictiveAdmissionNormalizesInvalidModeAndNilHistograms(t *testing.T) {
	var out bytes.Buffer
	WritePredictiveAdmission(&out, PredictiveAdmissionInput{})
	got := out.String()
	for _, want := range []string{
		`pig_predictive_admission_mode_info{mode="unknown"} 1`,
		"pig_predictive_admission_enabled 0",
		`pig_predictive_router_backpressure_state_info{scope="none",reason="none",source="unknown"} 1`,
		`pig_predictive_request_aware_last_decision_info{action="unknown",reason="unknown",pressure_source="none",prefill_class="unknown"} 1`,
		"pig_predictive_admission_prediction_duration_seconds_count 0",
		"pig_predictive_admission_estimator_duration_seconds_count 0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, got)
		}
	}
}

func TestWritePredictiveAdmissionBoundsRequestAwareLabels(t *testing.T) {
	var out bytes.Buffer
	WritePredictiveAdmission(&out, PredictiveAdmissionInput{
		RequestAwareAction:         "request-derived-action",
		RequestAwareReason:         "request-derived-reason",
		RequestAwarePressureSource: "request-derived-source",
		RequestAwarePrefillClass:   "request-derived-class",
	})
	got := out.String()
	if !strings.Contains(got, `pig_predictive_request_aware_last_decision_info{action="unknown",reason="unknown",pressure_source="none",prefill_class="unknown"} 1`) ||
		strings.Contains(got, "request-derived-") {
		t.Fatalf("request-aware labels were not normalized:\n%s", got)
	}
}

func TestWritePredictiveAdmissionOmitsRetiredMetrics(t *testing.T) {
	var out bytes.Buffer
	WritePredictiveAdmission(&out, PredictiveAdmissionInput{Mode: "enforce"})
	got := out.String()
	for _, retired := range []string{
		`pig_predictive_admission_mode_info{mode="off"}`,
		"pig_predictive_admission_forwarded_pending_prefill_attribution_valid",
		`pig_predictive_admission_failures_total{phase="forward_rejected"}`,
		`pig_predictive_admission_failures_total{phase="semantic"}`,
		`pig_predictive_admission_failures_total{phase="completion"}`,
		`pig_predictive_admission_failures_total{phase="resource_release"}`,
		"pig_predictive_learning_",
		"pig_predictive_input_size_",
		"pig_predictive_existing_prefill_",
		"pig_predictive_deferred_outcomes",
		"pig_predictive_shadow_observations",
		"pig_predictive_completion_observer_",
		"pig_predictive_tps_outcomes_",
		"pig_predictive_qualified_user_tps",
		"pig_predictive_qualified_tpot",
		"pig_predictive_admission_shadow_pending_",
		"pig_predictive_admission_exploratory_",
		"pig_predictive_pressure_last_",
		"pig_predictive_pressure_cells",
		"pig_predictive_pressure_samples_",
		"pig_predictive_pressure_maturations_",
		"pig_predictive_pressure_controlled_",
		"pig_predictive_pressure_invalidations_",
		"pig_predictive_pressure_events_",
		"pig_predictive_pressure_global_",
		"pig_predictive_decode_pacer_",
		"pig_predictive_request_aware_decode_pacer_",
		"pig_predictive_request_aware_projected_tps_proxy",
		"pig_predictive_request_aware_tps_forecast_valid",
		"pig_predictive_capability_kv_soft_limit_tokens",
		"pig_predictive_capability_safe_cold_prefill_tokens_per_second",
	} {
		if strings.Contains(got, retired) {
			t.Fatalf("retired metric %q remains in production output", retired)
		}
	}
}

func TestCapabilityInitializationReasonRejectsRetiredFallback(t *testing.T) {
	if got := normalizeCapabilityInitializationReason("metadata_fallback"); got != "unknown" {
		t.Fatalf("retired metadata fallback normalized to %q, want unknown", got)
	}
}
