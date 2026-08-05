package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func TestWritePredictiveAdmissionExposesBoundedOperationalState(t *testing.T) {
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
		ExploratoryFits:                         2,
		ExploratoryRisks:                        1,
		EnforcedRejects:                         4,
		LastReason:                              "existing_tps_at_risk",
		LastSource:                              "calibrated",
		LastSamples:                             7,
		LastExploratory:                         true,
		IntakeOpen:                              true,
		Reservations:                            2,
		VirtualDecodeSequences:                  3,
		ForwardedPendingPrefills:                1,
		ForwardedPendingPrefillTokens:           100,
		ForwardedPendingPrefillAttributionValid: true,
		ShadowPendingPrefills:                   1,
		ShadowPendingPrefillTokens:              200,
		ShadowPendingPrefillAttributionValid:    true,
		ShadowPendingPrefillAttributionState:    "aggregate",
		RetiredReservations:                     3,
		RetiredEvictions:                        1,
		LearningAccepted:                        11,
		LearningRejected:                        2,
		LearningInvalidations:                   3,
		LearningCells:                           4,
		LearningGlobalSamples:                   5,
		LearningAggregateThroughputSamples:      9,
		LearningAggregateThroughputCells:        3,
		LearningAdverseEvidenceMaxAge:           3 * time.Second,
		LearningExplorationBlockedUntil:         time.Unix(110, 0),
		LearningLastLoadPressureAt:              time.Unix(107, 0),
		LearningAdverseEvidenceEvents:           2,
		LearningHardExistingTPSAdverse:          3,
		LearningHardNewTPSAdverse:               4,
		LearningHardTPOTAdverse:                 5,
		LearningHardExistingTPSExploratory:      1,
		LearningHardExistingTPSNonExploratory:   2,
		LearningHardNewTPSExploratory:           3,
		LearningHardNewTPSNonExploratory:        1,
		LearningHardTPOTExploratory:             2,
		LearningHardTPOTNonExploratory:          3,
		LearningSoftExistingTPSMisses:           6,
		LearningSoftNewTPSMisses:                7,
		LearningSoftTPOTMisses:                  8,
		LearningExploratoryPredictions:          4,
		LearningExploratorySamples:              3,
		LearningWaitingPressureEvents:           5,
		LearningPreemptionPressureEvents:        1,
		InputSizeAccepted:                       12,
		InputSizeRejected:                       2,
		InputSizeInvalidations:                  1,
		InputSizeStored:                         9,
		InputSizeClasses:                        3,
		InputSizeCold:                           8,
		InputSizeLearned:                        4,
		InputSizeHintSamples:                    6,
		InputSizeHintInvalidations:              2,
		InputSizeHintUsed:                       3,
		InputSizeHintFallback:                   2,
		InputSizeHintMissing:                    7,
		InputSizeLastSource:                     "learned",
		InputSizeLastSamples:                    3,
		InputSizeLastRawHigh:                    100,
		InputSizeLastUpper:                      72,
		InputSizeLastHint:                       64,
		InputSizeLastHintSamples:                4,
		InputSizeLastHintKnown:                  true,
		InputSizeLastHintUsed:                   true,
		TPSBackend:                              6,
		TPSLocal:                                7,
		TPSLocalCensored:                        10,
		TPSMissing:                              8,
		TPSRejected:                             9,
		QualifiedUserTPS: telemetry.HistogramSample{Count: 2, Sum: 70, Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 20, Count: 1}, {UpperBound: 80, Count: 2},
		}},
		QualifiedTPOT: telemetry.HistogramSample{Count: 2, Sum: 0.07, Buckets: []telemetry.HistogramBucketSample{
			{UpperBound: 0.025, Count: 1}, {UpperBound: 0.05, Count: 2},
		}},
		ShadowObservations: PredictiveShadowObservationInput{
			Active: 2, Created: 10, Terminated: 7, Qualified: 5, Censored: 2, Dropped: 1,
		},
		DeferredOutcomes: PredictiveDeferredOutcomeInput{
			Active: 3, Released: 12, Terminated: 8, Qualified: 6, Censored: 2, Dropped: 1,
		},
		ExistingPrefill: PredictiveExistingPrefillInput{
			Accepted: 3, Rejected: 1, Censored: 2, Deduplicated: 4, LastExistingUserTPS: 1.998185, LastExistingUserTPSValid: true, LastExploratory: true,
		},
		FailureClose:               1,
		FailureDecide:              2,
		FailureForward:             3,
		FailureSemantic:            4,
		FailureCompletion:          5,
		FailureResourceRelease:     7,
		FailureTerminal:            6,
		CompletionObserverAttached: 4,
		CompletionObserverClaimed:  3,
		CompletionObserverUsage:    2,
		CompletionObserverTerminal: 3,
		PredictionDuration:         &prediction,
		EstimatorDuration:          &estimator,
		RouterBackpressure: PredictiveRouterBackpressureInput{
			Active: true, Activation: 2, Scope: "load", Applied: true, Reason: "existing_tps_at_risk", Source: "calibrated", Samples: 7, Exploratory: true,
			AggregateCompletionTPSEstimate: 154, PreviousAggregateCompletionTPSEstimate: 300,
			ActivatedAt: time.Unix(100, 0), Until: time.Unix(103, 0), Hold: 3 * time.Second,
			Activations: 2, Extensions: 5, LatestRejectAt: time.Unix(102, 0), RenewalLogs: 2, RenewalsSuppressed: 3,
			PredictiveRunning: 3, RawRunning: 1, EffectiveRunning: 3,
			RawGlobalLimit: 50, EffectiveGlobalLimit: 2,
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
		`pig_predictive_admission_exploratory_decisions_total{decision="fit"} 2`,
		`pig_predictive_admission_exploratory_decisions_total{decision="risk"} 1`,
		"pig_predictive_admission_last_exploratory 1",
		"pig_predictive_admission_enforced_rejects_total 4",
		`pig_predictive_admission_last_decision_info{reason="existing_tps_at_risk",source="calibrated"} 1`,
		"pig_predictive_admission_reservations 2",
		"pig_predictive_admission_virtual_decode_sequences 3",
		"pig_predictive_admission_forwarded_pending_prefills 1",
		"pig_predictive_admission_forwarded_pending_prefill_tokens 100",
		"pig_predictive_admission_forwarded_pending_prefill_attribution_valid 1",
		"pig_predictive_admission_shadow_pending_prefills 1",
		"pig_predictive_admission_shadow_pending_prefill_tokens 200",
		"pig_predictive_admission_shadow_pending_prefill_attribution_valid 1",
		`pig_predictive_admission_shadow_pending_prefill_attribution_state_info{state="aggregate"} 1`,
		"pig_predictive_admission_intake_open 1",
		"pig_predictive_admission_retired_evictions_total 1",
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		`pig_predictive_router_backpressure_state_info{scope="load",reason="existing_tps_at_risk",source="calibrated"} 1`,
		"pig_predictive_router_backpressure_exploratory 1",
		"pig_predictive_router_backpressure_samples 7",
		"pig_predictive_router_backpressure_aggregate_completion_tps_estimate 154.000000",
		"pig_predictive_router_backpressure_previous_aggregate_completion_tps_estimate 300.000000",
		"pig_predictive_router_backpressure_hold_seconds 3.000000",
		"pig_predictive_router_backpressure_activations_total 2",
		"pig_predictive_router_backpressure_extensions_total 5",
		"pig_predictive_router_backpressure_latest_load_reject_at_seconds 102.000000",
		"pig_predictive_router_backpressure_renewal_logs_total 2",
		"pig_predictive_router_backpressure_renewal_logs_suppressed_total 3",
		"pig_predictive_router_backpressure_predictive_running 3",
		"pig_predictive_router_backpressure_raw_running 1",
		"pig_predictive_router_backpressure_effective_running 3",
		"pig_predictive_router_backpressure_raw_global_limit 50",
		"pig_predictive_router_backpressure_effective_global_limit 2",
		`pig_predictive_learning_samples_total{result="accepted"} 11`,
		"pig_predictive_learning_invalidations_total 3",
		"pig_predictive_learning_global_samples 5",
		"pig_predictive_learning_aggregate_throughput_samples_total 9",
		"pig_predictive_learning_aggregate_throughput_cells 3",
		"pig_predictive_learning_adverse_evidence_max_age_seconds 3.000000",
		"pig_predictive_learning_exploration_blocked_until_seconds 110.000000",
		"pig_predictive_learning_last_load_pressure_at_seconds 107.000000",
		"pig_predictive_learning_adverse_evidence_events_total 2",
		`pig_predictive_learning_hard_adverse_total{dimension="existing_tps"} 3`,
		`pig_predictive_learning_hard_adverse_total{dimension="new_tps"} 4`,
		`pig_predictive_learning_hard_adverse_total{dimension="tpot"} 5`,
		`pig_predictive_learning_hard_adverse_origin_total{dimension="existing_tps",origin="exploratory"} 1`,
		`pig_predictive_learning_hard_adverse_origin_total{dimension="existing_tps",origin="non_exploratory"} 2`,
		`pig_predictive_learning_hard_adverse_origin_total{dimension="new_tps",origin="exploratory"} 3`,
		`pig_predictive_learning_hard_adverse_origin_total{dimension="new_tps",origin="non_exploratory"} 1`,
		`pig_predictive_learning_hard_adverse_origin_total{dimension="tpot",origin="exploratory"} 2`,
		`pig_predictive_learning_hard_adverse_origin_total{dimension="tpot",origin="non_exploratory"} 3`,
		`pig_predictive_learning_soft_qos_misses_total{dimension="existing_tps"} 6`,
		`pig_predictive_learning_soft_qos_misses_total{dimension="new_tps"} 7`,
		`pig_predictive_learning_soft_qos_misses_total{dimension="tpot"} 8`,
		"pig_predictive_learning_exploratory_predictions_total 4",
		"pig_predictive_learning_exploratory_samples_total 3",
		`pig_predictive_learning_load_pressure_events_total{kind="waiting"} 5`,
		`pig_predictive_learning_load_pressure_events_total{kind="preemption"} 1`,
		"pig_predictive_input_size_hint_samples_stored 6",
		"pig_predictive_input_size_hint_invalidations_total 2",
		`pig_predictive_input_size_hint_estimates_total{result="used"} 3`,
		`pig_predictive_input_size_hint_estimates_total{result="fallback"} 2`,
		`pig_predictive_input_size_hint_estimates_total{result="missing"} 7`,
		"pig_predictive_input_size_last_hint_tokens 64",
		"pig_predictive_input_size_last_hint_samples 4",
		"pig_predictive_input_size_last_hint_known 1",
		"pig_predictive_input_size_last_hint_used 1",
		`pig_predictive_input_size_samples_total{result="accepted"} 12`,
		`pig_predictive_input_size_estimates_total{source="learned"} 4`,
		`pig_predictive_input_size_last_estimate_info{source="learned"} 1`,
		"pig_predictive_input_size_last_upper_tokens 72",
		`pig_predictive_tps_outcomes_total{result="backend_qualified"} 6`,
		`pig_predictive_tps_outcomes_total{result="local_corroborated"} 7`,
		`pig_predictive_tps_outcomes_total{result="local_censored"} 10`,
		`pig_predictive_tps_outcomes_total{result="missing"} 8`,
		`pig_predictive_tps_outcomes_total{result="rejected"} 9`,
		"pig_predictive_qualified_user_tps_count 2",
		"pig_predictive_qualified_user_tps_sum 70.000000",
		`pig_predictive_qualified_user_tps_bucket{le="20"} 1`,
		`pig_predictive_qualified_user_tps_bucket{le="80"} 2`,
		"pig_predictive_qualified_tpot_seconds_count 2",
		"pig_predictive_qualified_tpot_seconds_sum 0.070000",
		`pig_predictive_qualified_tpot_seconds_bucket{le="0.025"} 1`,
		`pig_predictive_qualified_tpot_seconds_bucket{le="0.05"} 2`,
		`pig_predictive_completion_observer_events_total{event="attached"} 4`,
		`pig_predictive_completion_observer_events_total{event="claimed"} 3`,
		`pig_predictive_completion_observer_events_total{event="usage"} 2`,
		`pig_predictive_completion_observer_events_total{event="terminal"} 3`,
		"pig_predictive_shadow_observations 2",
		`pig_predictive_shadow_observations_total{result="created"} 10`,
		`pig_predictive_shadow_observations_total{result="terminated"} 7`,
		`pig_predictive_shadow_observations_total{result="qualified"} 5`,
		`pig_predictive_shadow_observations_total{result="censored"} 2`,
		`pig_predictive_shadow_observations_total{result="dropped"} 1`,
		"pig_predictive_deferred_outcomes 3",
		`pig_predictive_deferred_outcomes_total{result="released"} 12`,
		`pig_predictive_deferred_outcomes_total{result="terminated"} 8`,
		`pig_predictive_deferred_outcomes_total{result="qualified"} 6`,
		`pig_predictive_deferred_outcomes_total{result="censored"} 2`,
		`pig_predictive_deferred_outcomes_total{result="dropped"} 1`,
		`pig_predictive_existing_prefill_outcomes_total{result="accepted"} 3`,
		`pig_predictive_existing_prefill_outcomes_total{result="deduplicated"} 4`,
		"pig_predictive_existing_prefill_last_user_tps 1.998185",
		"pig_predictive_existing_prefill_last_user_tps_valid 1",
		"pig_predictive_existing_prefill_last_exploratory 1",
		`pig_predictive_admission_failures_total{phase="forward"} 3`,
		`pig_predictive_admission_failures_total{phase="semantic"} 4`,
		`pig_predictive_admission_failures_total{phase="completion"} 5`,
		`pig_predictive_admission_failures_total{phase="resource_release"} 7`,
		`pig_predictive_admission_failures_total{phase="terminal"} 6`,
		"pig_predictive_admission_prediction_duration_seconds_count 1",
		"pig_predictive_admission_estimator_duration_seconds_count 1",
		`pig_predictive_admission_prediction_duration_seconds_bucket{le="0.00025"} 0`,
		`pig_predictive_admission_prediction_duration_seconds_bucket{le="0.001"} 0`,
		`pig_predictive_admission_estimator_duration_seconds_bucket{le="0.00025"} 1`,
		`pig_predictive_admission_estimator_duration_seconds_bucket{le="0.001"} 1`,
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
		`pig_predictive_admission_shadow_pending_prefill_attribution_state_info{state="empty"} 1`,
		"pig_predictive_admission_prediction_duration_seconds_count 0",
		"pig_predictive_admission_estimator_duration_seconds_count 0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, got)
		}
	}
}

func TestWritePredictiveAdmissionBoundsUnknownShadowPrefillAttributionState(t *testing.T) {
	var out bytes.Buffer
	WritePredictiveAdmission(&out, PredictiveAdmissionInput{
		ShadowPendingPrefills:                2,
		ShadowPendingPrefillAttributionState: "request-derived-unbounded-value",
	})
	got := out.String()
	if !strings.Contains(got, `pig_predictive_admission_shadow_pending_prefill_attribution_state_info{state="incompatible"} 1`) ||
		strings.Contains(got, "request-derived-unbounded-value") {
		t.Fatalf("unknown shadow attribution state was not normalized:\n%s", got)
	}
}

func TestWritePredictiveAdmissionPreservesOnlyFixedShadowPrefillAttributionStates(t *testing.T) {
	for _, state := range []string{"empty", "single", "aggregate", "incompatible"} {
		t.Run(state, func(t *testing.T) {
			var out bytes.Buffer
			WritePredictiveAdmission(&out, PredictiveAdmissionInput{ShadowPendingPrefillAttributionState: state})
			want := `pig_predictive_admission_shadow_pending_prefill_attribution_state_info{state="` + state + `"} 1`
			if got := out.String(); !strings.Contains(got, want) {
				t.Fatalf("fixed shadow attribution state missing %q:\n%s", want, got)
			}
		})
	}
}
