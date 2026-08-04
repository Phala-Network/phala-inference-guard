package metrics

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type PredictiveAdmissionInput struct {
	Mode             string
	Attempts         uint64
	Fits             uint64
	Risks            uint64
	Unknown          uint64
	ExploratoryFits  uint64
	ExploratoryRisks uint64
	// EnforcedRejects counts HTTP requests for which the proxy emitted an
	// enforced predictive rejection. Router protection is published earlier
	// from RouterBackpressure and must never be inferred from this counter.
	EnforcedRejects                         uint64
	LastReason                              string
	LastSource                              string
	LastSamples                             int
	LastExploratory                         bool
	LastRejectReason                        string
	LastRejectSource                        string
	LastRejectScope                         string
	LastRejectSamples                       int
	LastRejectAt                            time.Time
	IntakeOpen                              bool
	Reservations                            int
	VirtualDecodeSequences                  int
	ForwardedPendingPrefills                int
	ForwardedPendingPrefillTokens           int64
	ForwardedPendingPrefillAttributionValid bool
	ShadowPendingPrefills                   int
	ShadowPendingPrefillTokens              int64
	ShadowPendingPrefillAttributionValid    bool
	RetiredReservations                     int
	RetiredEvictions                        uint64
	LearningAccepted                        uint64
	LearningRejected                        uint64
	LearningInvalidations                   uint64
	LearningCells                           int
	LearningGlobalSamples                   int
	LearningExistingTPSSamples              uint64
	LearningNewTPSSamples                   uint64
	LearningAggregateThroughputSamples      uint64
	LearningAggregateThroughputCells        int
	LearningAdverseEvidenceMaxAge           time.Duration
	LearningExplorationBlockedUntil         time.Time
	LearningLastLoadPressureAt              time.Time
	LearningAdverseEvidenceEvents           uint64
	LearningHardExistingTPSAdverse          uint64
	LearningHardNewTPSAdverse               uint64
	LearningHardTPOTAdverse                 uint64
	LearningSoftExistingTPSMisses           uint64
	LearningSoftNewTPSMisses                uint64
	LearningSoftTPOTMisses                  uint64
	LearningExploratoryPredictions          uint64
	LearningExploratorySamples              uint64
	LearningWaitingPressureEvents           uint64
	LearningPreemptionPressureEvents        uint64
	InputSizeAccepted                       uint64
	InputSizeRejected                       uint64
	InputSizeInvalidations                  uint64
	InputSizeStored                         int
	InputSizeClasses                        int
	InputSizeCold                           uint64
	InputSizeLearned                        uint64
	InputSizeHintSamples                    int
	InputSizeHintInvalidations              uint64
	InputSizeHintUsed                       uint64
	InputSizeHintFallback                   uint64
	InputSizeHintMissing                    uint64
	InputSizeLastSource                     string
	InputSizeLastSamples                    int
	InputSizeLastRawHigh                    int64
	InputSizeLastUpper                      int64
	InputSizeLastHint                       int64
	InputSizeLastHintSamples                int
	InputSizeLastHintKnown                  bool
	InputSizeLastHintUsed                   bool
	TPSBackend                              uint64
	TPSLocal                                uint64
	TPSLocalCensored                        uint64
	TPSMissing                              uint64
	TPSRejected                             uint64
	QualifiedUserTPS                        telemetry.HistogramSample
	QualifiedTPOT                           telemetry.HistogramSample
	ShadowObservations                      PredictiveShadowObservationInput
	DeferredOutcomes                        PredictiveDeferredOutcomeInput
	ExistingPrefill                         PredictiveExistingPrefillInput
	FailureClose                            uint64
	FailureDecide                           uint64
	FailureForward                          uint64
	FailureForwardRejected                  uint64
	FailureSemantic                         uint64
	FailureCompletion                       uint64
	FailureResourceRelease                  uint64
	FailureTerminal                         uint64
	CompletionObserverAttached              uint64
	CompletionObserverClaimed               uint64
	CompletionObserverUsage                 uint64
	CompletionObserverTerminal              uint64
	PredictionDuration                      *histogram.DurationHistogram
	EstimatorDuration                       *histogram.DurationHistogram
	RouterBackpressure                      PredictiveRouterBackpressureInput
}

type PredictiveRouterBackpressureInput struct {
	Active                                 bool
	Activation                             uint64
	Scope                                  string
	MinimumRunning                         int
	Applied                                bool
	Reason                                 string
	Source                                 string
	Samples                                int
	Exploratory                            bool
	AggregateCompletionTPSEstimate         float64
	PreviousAggregateCompletionTPSEstimate float64
	ActivatedAt                            time.Time
	Until                                  time.Time
	Hold                                   time.Duration
	Activations                            uint64
	Extensions                             uint64
	LatestRejectAt                         time.Time
	RenewalLogs                            uint64
	RenewalsSuppressed                     uint64
	PredictiveRunning                      int
	RawRunning                             int
	EffectiveRunning                       int
	RawGlobalLimit                         int
	EffectiveGlobalLimit                   int
}

type PredictiveShadowObservationInput struct {
	Active     int
	Created    uint64
	Terminated uint64
	Qualified  uint64
	Censored   uint64
	Dropped    uint64
}

type PredictiveDeferredOutcomeInput struct {
	Active     int
	Released   uint64
	Terminated uint64
	Qualified  uint64
	Censored   uint64
	Dropped    uint64
}

type PredictiveExistingPrefillInput struct {
	Accepted                 uint64
	Rejected                 uint64
	Censored                 uint64
	LastExistingUserTPS      float64
	LastExistingUserTPSValid bool
}

func WritePredictiveAdmission(w io.Writer, input PredictiveAdmissionInput) {
	mode := input.Mode
	if mode != "shadow" && mode != "enforce" {
		mode = "off"
	}
	backpressureReason := input.RouterBackpressure.Reason
	if backpressureReason == "" {
		backpressureReason = "none"
	}
	backpressureSource := input.RouterBackpressure.Source
	if backpressureSource == "" {
		backpressureSource = "unknown"
	}
	lastRejectReason := input.LastRejectReason
	if lastRejectReason == "" {
		lastRejectReason = "none"
	}
	lastRejectSource := input.LastRejectSource
	if lastRejectSource == "" {
		lastRejectSource = "unknown"
	}
	lastRejectScope := input.LastRejectScope
	if lastRejectScope == "" {
		lastRejectScope = "none"
	}
	backpressureScope := input.RouterBackpressure.Scope
	if backpressureScope == "" {
		backpressureScope = "none"
	}
	fmt.Fprintf(w, "pig_predictive_admission_mode_info{mode=%q} 1\n", mode)
	fmt.Fprintf(w, "pig_predictive_admission_enabled %d\n", num.BoolAsInt(mode != "off"))
	fmt.Fprintf(w, "pig_predictive_admission_enforce %d\n", num.BoolAsInt(mode == "enforce"))
	fmt.Fprintf(w, "pig_predictive_admission_attempts_total %d\n", input.Attempts)
	fmt.Fprintf(w, "pig_predictive_admission_decisions_total{decision=%q} %d\n", "fit", input.Fits)
	fmt.Fprintf(w, "pig_predictive_admission_decisions_total{decision=%q} %d\n", "risk", input.Risks)
	fmt.Fprintf(w, "pig_predictive_admission_decisions_total{decision=%q} %d\n", "unknown", input.Unknown)
	fmt.Fprintf(w, "pig_predictive_admission_exploratory_decisions_total{decision=%q} %d\n", "fit", input.ExploratoryFits)
	fmt.Fprintf(w, "pig_predictive_admission_exploratory_decisions_total{decision=%q} %d\n", "risk", input.ExploratoryRisks)
	fmt.Fprintf(w, "pig_predictive_admission_enforced_rejects_total %d\n", input.EnforcedRejects)
	fmt.Fprintf(w, "pig_predictive_admission_last_decision_info{reason=%q,source=%q} 1\n", input.LastReason, input.LastSource)
	fmt.Fprintf(w, "pig_predictive_admission_last_samples %d\n", input.LastSamples)
	fmt.Fprintf(w, "pig_predictive_admission_last_exploratory %d\n", num.BoolAsInt(input.LastExploratory))
	fmt.Fprintf(w, "pig_predictive_admission_last_reject_info{reason=%q,source=%q,scope=%q} 1\n", lastRejectReason, lastRejectSource, lastRejectScope)
	fmt.Fprintf(w, "pig_predictive_admission_last_reject_samples %d\n", input.LastRejectSamples)
	fmt.Fprintf(w, "pig_predictive_admission_last_reject_at_seconds %.6f\n", predictiveMetricUnixSeconds(input.LastRejectAt))
	fmt.Fprintf(w, "pig_predictive_admission_intake_open %d\n", num.BoolAsInt(input.IntakeOpen))
	fmt.Fprintf(w, "pig_predictive_admission_reservations %d\n", input.Reservations)
	fmt.Fprintf(w, "pig_predictive_admission_virtual_decode_sequences %d\n", input.VirtualDecodeSequences)
	fmt.Fprintf(w, "pig_predictive_admission_forwarded_pending_prefills %d\n", input.ForwardedPendingPrefills)
	fmt.Fprintf(w, "pig_predictive_admission_forwarded_pending_prefill_tokens %d\n", input.ForwardedPendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_admission_forwarded_pending_prefill_attribution_valid %d\n", num.BoolAsInt(input.ForwardedPendingPrefillAttributionValid))
	fmt.Fprintf(w, "pig_predictive_admission_shadow_pending_prefills %d\n", input.ShadowPendingPrefills)
	fmt.Fprintf(w, "pig_predictive_admission_shadow_pending_prefill_tokens %d\n", input.ShadowPendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_admission_shadow_pending_prefill_attribution_valid %d\n", num.BoolAsInt(input.ShadowPendingPrefillAttributionValid))
	fmt.Fprintf(w, "pig_predictive_admission_retired_reservations %d\n", input.RetiredReservations)
	fmt.Fprintf(w, "pig_predictive_admission_retired_evictions_total %d\n", input.RetiredEvictions)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_active %d\n", num.BoolAsInt(input.RouterBackpressure.Active))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_applied %d\n", num.BoolAsInt(input.RouterBackpressure.Applied))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_state_info{scope=%q,reason=%q,source=%q} 1\n", backpressureScope, backpressureReason, backpressureSource)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_exploratory %d\n", num.BoolAsInt(input.RouterBackpressure.Exploratory))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_activation %d\n", input.RouterBackpressure.Activation)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_samples %d\n", input.RouterBackpressure.Samples)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_aggregate_completion_tps_estimate %.6f\n", input.RouterBackpressure.AggregateCompletionTPSEstimate)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_previous_aggregate_completion_tps_estimate %.6f\n", input.RouterBackpressure.PreviousAggregateCompletionTPSEstimate)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_activated_at_seconds %.6f\n", predictiveMetricUnixSeconds(input.RouterBackpressure.ActivatedAt))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_until_seconds %.6f\n", predictiveMetricUnixSeconds(input.RouterBackpressure.Until))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_hold_seconds %.6f\n", input.RouterBackpressure.Hold.Seconds())
	fmt.Fprintf(w, "pig_predictive_router_backpressure_activations_total %d\n", input.RouterBackpressure.Activations)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_extensions_total %d\n", input.RouterBackpressure.Extensions)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_latest_load_reject_at_seconds %.6f\n", predictiveMetricUnixSeconds(input.RouterBackpressure.LatestRejectAt))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_renewal_logs_total %d\n", input.RouterBackpressure.RenewalLogs)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_renewal_logs_suppressed_total %d\n", input.RouterBackpressure.RenewalsSuppressed)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_predictive_running %d\n", input.RouterBackpressure.PredictiveRunning)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_raw_running %d\n", input.RouterBackpressure.RawRunning)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_effective_running %d\n", input.RouterBackpressure.EffectiveRunning)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_raw_global_limit %d\n", input.RouterBackpressure.RawGlobalLimit)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_effective_global_limit %d\n", input.RouterBackpressure.EffectiveGlobalLimit)
	fmt.Fprintf(w, "pig_predictive_learning_samples_total{result=%q} %d\n", "accepted", input.LearningAccepted)
	fmt.Fprintf(w, "pig_predictive_learning_samples_total{result=%q} %d\n", "rejected", input.LearningRejected)
	fmt.Fprintf(w, "pig_predictive_learning_invalidations_total %d\n", input.LearningInvalidations)
	fmt.Fprintf(w, "pig_predictive_learning_cells %d\n", input.LearningCells)
	fmt.Fprintf(w, "pig_predictive_learning_global_samples %d\n", input.LearningGlobalSamples)
	fmt.Fprintf(w, "pig_predictive_learning_tps_samples_total{phase=%q} %d\n", "existing_prefill", input.LearningExistingTPSSamples)
	fmt.Fprintf(w, "pig_predictive_learning_tps_samples_total{phase=%q} %d\n", "new_decode", input.LearningNewTPSSamples)
	fmt.Fprintf(w, "pig_predictive_learning_aggregate_throughput_samples_total %d\n", input.LearningAggregateThroughputSamples)
	fmt.Fprintf(w, "pig_predictive_learning_aggregate_throughput_cells %d\n", input.LearningAggregateThroughputCells)
	fmt.Fprintf(w, "pig_predictive_learning_adverse_evidence_max_age_seconds %.6f\n", input.LearningAdverseEvidenceMaxAge.Seconds())
	fmt.Fprintf(w, "pig_predictive_learning_exploration_blocked_until_seconds %.6f\n", predictiveMetricUnixSeconds(input.LearningExplorationBlockedUntil))
	fmt.Fprintf(w, "pig_predictive_learning_last_load_pressure_at_seconds %.6f\n", predictiveMetricUnixSeconds(input.LearningLastLoadPressureAt))
	fmt.Fprintf(w, "pig_predictive_learning_adverse_evidence_events_total %d\n", input.LearningAdverseEvidenceEvents)
	fmt.Fprintf(w, "pig_predictive_learning_hard_adverse_total{dimension=%q} %d\n", "existing_tps", input.LearningHardExistingTPSAdverse)
	fmt.Fprintf(w, "pig_predictive_learning_hard_adverse_total{dimension=%q} %d\n", "new_tps", input.LearningHardNewTPSAdverse)
	fmt.Fprintf(w, "pig_predictive_learning_hard_adverse_total{dimension=%q} %d\n", "tpot", input.LearningHardTPOTAdverse)
	fmt.Fprintf(w, "pig_predictive_learning_soft_qos_misses_total{dimension=%q} %d\n", "existing_tps", input.LearningSoftExistingTPSMisses)
	fmt.Fprintf(w, "pig_predictive_learning_soft_qos_misses_total{dimension=%q} %d\n", "new_tps", input.LearningSoftNewTPSMisses)
	fmt.Fprintf(w, "pig_predictive_learning_soft_qos_misses_total{dimension=%q} %d\n", "tpot", input.LearningSoftTPOTMisses)
	fmt.Fprintf(w, "pig_predictive_learning_exploratory_predictions_total %d\n", input.LearningExploratoryPredictions)
	fmt.Fprintf(w, "pig_predictive_learning_exploratory_samples_total %d\n", input.LearningExploratorySamples)
	fmt.Fprintf(w, "pig_predictive_learning_load_pressure_events_total{kind=%q} %d\n", "waiting", input.LearningWaitingPressureEvents)
	fmt.Fprintf(w, "pig_predictive_learning_load_pressure_events_total{kind=%q} %d\n", "preemption", input.LearningPreemptionPressureEvents)
	fmt.Fprintf(w, "pig_predictive_input_size_samples_total{result=%q} %d\n", "accepted", input.InputSizeAccepted)
	fmt.Fprintf(w, "pig_predictive_input_size_samples_total{result=%q} %d\n", "rejected", input.InputSizeRejected)
	fmt.Fprintf(w, "pig_predictive_input_size_invalidations_total %d\n", input.InputSizeInvalidations)
	fmt.Fprintf(w, "pig_predictive_input_size_samples_stored %d\n", input.InputSizeStored)
	fmt.Fprintf(w, "pig_predictive_input_size_classes %d\n", input.InputSizeClasses)
	fmt.Fprintf(w, "pig_predictive_input_size_estimates_total{source=%q} %d\n", "cold", input.InputSizeCold)
	fmt.Fprintf(w, "pig_predictive_input_size_estimates_total{source=%q} %d\n", "learned", input.InputSizeLearned)
	fmt.Fprintf(w, "pig_predictive_input_size_hint_samples_stored %d\n", input.InputSizeHintSamples)
	fmt.Fprintf(w, "pig_predictive_input_size_hint_invalidations_total %d\n", input.InputSizeHintInvalidations)
	fmt.Fprintf(w, "pig_predictive_input_size_hint_estimates_total{result=%q} %d\n", "used", input.InputSizeHintUsed)
	fmt.Fprintf(w, "pig_predictive_input_size_hint_estimates_total{result=%q} %d\n", "fallback", input.InputSizeHintFallback)
	fmt.Fprintf(w, "pig_predictive_input_size_hint_estimates_total{result=%q} %d\n", "missing", input.InputSizeHintMissing)
	fmt.Fprintf(w, "pig_predictive_input_size_last_estimate_info{source=%q} 1\n", input.InputSizeLastSource)
	fmt.Fprintf(w, "pig_predictive_input_size_last_samples %d\n", input.InputSizeLastSamples)
	fmt.Fprintf(w, "pig_predictive_input_size_last_raw_high_tokens %d\n", input.InputSizeLastRawHigh)
	fmt.Fprintf(w, "pig_predictive_input_size_last_upper_tokens %d\n", input.InputSizeLastUpper)
	fmt.Fprintf(w, "pig_predictive_input_size_last_hint_tokens %d\n", input.InputSizeLastHint)
	fmt.Fprintf(w, "pig_predictive_input_size_last_hint_samples %d\n", input.InputSizeLastHintSamples)
	fmt.Fprintf(w, "pig_predictive_input_size_last_hint_known %d\n", num.BoolAsInt(input.InputSizeLastHintKnown))
	fmt.Fprintf(w, "pig_predictive_input_size_last_hint_used %d\n", num.BoolAsInt(input.InputSizeLastHintUsed))
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "backend_qualified", input.TPSBackend)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "local_corroborated", input.TPSLocal)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "local_censored", input.TPSLocalCensored)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "missing", input.TPSMissing)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "rejected", input.TPSRejected)
	writePredictiveHistogramSample(w, "pig_predictive_qualified_user_tps", input.QualifiedUserTPS)
	writePredictiveHistogramSample(w, "pig_predictive_qualified_tpot_seconds", input.QualifiedTPOT)
	fmt.Fprintf(w, "pig_predictive_completion_observer_events_total{event=%q} %d\n", "attached", input.CompletionObserverAttached)
	fmt.Fprintf(w, "pig_predictive_completion_observer_events_total{event=%q} %d\n", "claimed", input.CompletionObserverClaimed)
	fmt.Fprintf(w, "pig_predictive_completion_observer_events_total{event=%q} %d\n", "usage", input.CompletionObserverUsage)
	fmt.Fprintf(w, "pig_predictive_completion_observer_events_total{event=%q} %d\n", "terminal", input.CompletionObserverTerminal)
	fmt.Fprintf(w, "pig_predictive_shadow_observations %d\n", input.ShadowObservations.Active)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "created", input.ShadowObservations.Created)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "terminated", input.ShadowObservations.Terminated)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "qualified", input.ShadowObservations.Qualified)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "censored", input.ShadowObservations.Censored)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "dropped", input.ShadowObservations.Dropped)
	fmt.Fprintf(w, "pig_predictive_deferred_outcomes %d\n", input.DeferredOutcomes.Active)
	fmt.Fprintf(w, "pig_predictive_deferred_outcomes_total{result=%q} %d\n", "released", input.DeferredOutcomes.Released)
	fmt.Fprintf(w, "pig_predictive_deferred_outcomes_total{result=%q} %d\n", "terminated", input.DeferredOutcomes.Terminated)
	fmt.Fprintf(w, "pig_predictive_deferred_outcomes_total{result=%q} %d\n", "qualified", input.DeferredOutcomes.Qualified)
	fmt.Fprintf(w, "pig_predictive_deferred_outcomes_total{result=%q} %d\n", "censored", input.DeferredOutcomes.Censored)
	fmt.Fprintf(w, "pig_predictive_deferred_outcomes_total{result=%q} %d\n", "dropped", input.DeferredOutcomes.Dropped)
	fmt.Fprintf(w, "pig_predictive_existing_prefill_outcomes_total{result=%q} %d\n", "accepted", input.ExistingPrefill.Accepted)
	fmt.Fprintf(w, "pig_predictive_existing_prefill_outcomes_total{result=%q} %d\n", "rejected", input.ExistingPrefill.Rejected)
	fmt.Fprintf(w, "pig_predictive_existing_prefill_outcomes_total{result=%q} %d\n", "censored", input.ExistingPrefill.Censored)
	fmt.Fprintf(w, "pig_predictive_existing_prefill_last_user_tps %.6f\n", input.ExistingPrefill.LastExistingUserTPS)
	fmt.Fprintf(w, "pig_predictive_existing_prefill_last_user_tps_valid %d\n", num.BoolAsInt(input.ExistingPrefill.LastExistingUserTPSValid))
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "close", input.FailureClose)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "decide", input.FailureDecide)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "forward", input.FailureForward)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "forward_rejected", input.FailureForwardRejected)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "semantic", input.FailureSemantic)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "completion", input.FailureCompletion)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "resource_release", input.FailureResourceRelease)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "terminal", input.FailureTerminal)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_prediction_duration_seconds", input.PredictionDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_estimator_duration_seconds", input.EstimatorDuration)
}

func predictiveMetricUnixSeconds(value time.Time) float64 {
	if value.IsZero() {
		return 0
	}
	return float64(value.UnixNano()) / float64(time.Second)
}

func writePredictiveDurationHistogram(w io.Writer, name string, value *histogram.DurationHistogram) {
	if value != nil {
		histogram.WriteDurationHistogram(w, name, value)
		return
	}
	empty := histogram.NewPredictiveDurationHistogram()
	histogram.WriteDurationHistogram(w, name, &empty)
}

func writePredictiveHistogramSample(w io.Writer, name string, sample telemetry.HistogramSample) {
	fmt.Fprintf(w, "%s_count %d\n", name, sample.Count)
	fmt.Fprintf(w, "%s_sum %.6f\n", name, sample.Sum)
	for _, bucket := range sample.Buckets {
		fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, strconv.FormatFloat(bucket.UpperBound, 'f', -1, 64), bucket.Count)
	}
	fmt.Fprintf(w, "%s_bucket{le=%q} %d\n", name, "+Inf", sample.Count)
}
