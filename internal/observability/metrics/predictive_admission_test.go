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
	estimator := histogram.NewPredictiveDurationHistogram()
	prediction.Observe(3 * time.Millisecond)
	estimator.Observe(4 * time.Microsecond)
	var out bytes.Buffer
	WritePredictiveAdmission(&out, PredictiveAdmissionInput{
		Mode:                                    "enforce",
		Attempts:                                9,
		Fits:                                    5,
		Risks:                                   3,
		Unknown:                                 1,
		EnforcedRejects:                         4,
		LastReason:                              "request_size",
		LastSource:                              "deterministic",
		LastRejectReason:                        "request_size",
		LastRejectSource:                        "deterministic",
		LastRejectScope:                         "request",
		LastRejectAt:                            time.Unix(102, 0),
		IntakeOpen:                              true,
		Reservations:                            2,
		VirtualDecodeSequences:                  3,
		ForwardedPendingPrefills:                1,
		ForwardedPendingPrefillTokens:           100,
		ForwardedPendingPrefillAttributionValid: true,
		RetiredReservations:                     3,
		RetiredEvictions:                        1,
		FailureForward:                          3,
		FailureSemantic:                         4,
		FailureCompletion:                       5,
		FailureResourceRelease:                  7,
		FailureTerminal:                         6,
		PredictionDuration:                      &prediction,
		EstimatorDuration:                       &estimator,
		RequestAwareAction:                      "size_protect",
		RequestAwareReason:                      "request_size",
		RequestAwarePressureSource:              "kv",
		RequestAwarePressure:                    0.333333,
		RequestAwareSelectionInputTokens:        1500,
		RequestAwareReservedTokens:              1600,
		RequestAwareAllowanceTokens:             1333,
		RequestAwareEffectiveKV:                 7000,
		RequestAwarePostAdmitKV:                 8600,
		RequestAwareRemainingKV:                 2000,
		RequestAwareRunning:                     4,
		RequestAwareWaiting:                     1,
		RequestAwareEffectiveSequences:          4,
		RequestAwareAggregateTPSProxy:           80,
		RequestAwareMeanActiveTPSProxy:          20,
		RequestAwareProjectedTPSProxy:           16,
		RequestAwareTPSForecastValid:            true,
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
		"pig_predictive_admission_forwarded_pending_prefill_attribution_valid 1",
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
		`pig_predictive_request_aware_last_decision_info{action="size_protect",reason="request_size",pressure_source="kv"} 1`,
		"pig_predictive_request_aware_pressure 0.333333",
		"pig_predictive_request_aware_selection_input_tokens 1500",
		"pig_predictive_request_aware_reserved_tokens 1600",
		"pig_predictive_request_aware_allowance_tokens 1333",
		"pig_predictive_request_aware_effective_kv_tokens 7000",
		"pig_predictive_request_aware_post_admit_kv_tokens 8600",
		"pig_predictive_request_aware_remaining_kv_tokens 2000",
		"pig_predictive_request_aware_running 4",
		"pig_predictive_request_aware_waiting 1",
		"pig_predictive_request_aware_effective_sequences 4",
		"pig_predictive_request_aware_aggregate_tps_proxy 80.000000",
		"pig_predictive_request_aware_mean_active_tps_proxy 20.000000",
		"pig_predictive_request_aware_projected_tps_proxy 16.000000",
		"pig_predictive_request_aware_tps_forecast_valid 1",
		"pig_predictive_router_inspect_capacity 1",
		`pig_predictive_admission_failures_total{phase="forward"} 3`,
		`pig_predictive_admission_failures_total{phase="semantic"} 4`,
		`pig_predictive_admission_failures_total{phase="completion"} 5`,
		`pig_predictive_admission_failures_total{phase="resource_release"} 7`,
		`pig_predictive_admission_failures_total{phase="terminal"} 6`,
		"pig_predictive_admission_prediction_duration_seconds_count 1",
		"pig_predictive_admission_estimator_duration_seconds_count 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, got)
		}
	}
}

func TestWritePredictiveAdmissionNormalizesDisabledModeAndNilHistograms(t *testing.T) {
	var out bytes.Buffer
	WritePredictiveAdmission(&out, PredictiveAdmissionInput{})
	got := out.String()
	for _, want := range []string{
		`pig_predictive_admission_mode_info{mode="off"} 1`,
		"pig_predictive_admission_enabled 0",
		`pig_predictive_router_backpressure_state_info{scope="none",reason="none",source="unknown"} 1`,
		`pig_predictive_request_aware_last_decision_info{action="unknown",reason="unknown",pressure_source="none"} 1`,
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
	})
	got := out.String()
	if !strings.Contains(got, `pig_predictive_request_aware_last_decision_info{action="unknown",reason="unknown",pressure_source="none"} 1`) ||
		strings.Contains(got, "request-derived-") {
		t.Fatalf("request-aware labels were not normalized:\n%s", got)
	}
}

func TestWritePredictiveAdmissionOmitsRetiredLearningMetrics(t *testing.T) {
	var out bytes.Buffer
	WritePredictiveAdmission(&out, PredictiveAdmissionInput{Mode: "enforce"})
	got := out.String()
	for _, retired := range []string{
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
	} {
		if strings.Contains(got, retired) {
			t.Fatalf("retired metric %q remains in production output", retired)
		}
	}
}
