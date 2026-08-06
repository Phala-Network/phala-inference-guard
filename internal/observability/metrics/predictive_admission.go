package metrics

import (
	"fmt"
	"io"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type PredictiveAdmissionInput struct {
	Mode     string
	Attempts uint64
	Fits     uint64
	Risks    uint64
	Unknown  uint64
	// EnforcedRejects counts HTTP requests for which the proxy emitted an
	// enforced predictive rejection. Router protection is published earlier
	// from RouterBackpressure and must never be inferred from this counter.
	EnforcedRejects                                          uint64
	LastReason                                               string
	LastSource                                               string
	LastRejectReason                                         string
	LastRejectSource                                         string
	LastRejectScope                                          string
	LastRejectAt                                             time.Time
	IntakeOpen                                               bool
	Reservations                                             int
	VirtualDecodeSequences                                   int
	ForwardedPendingPrefills                                 int
	ForwardedPendingPrefillTokens                            int64
	ForwardedPendingPrefillAttributionValid                  bool
	RetiredReservations                                      int
	RetiredEvictions                                         uint64
	FailureClose                                             uint64
	FailureDecide                                            uint64
	FailureForward                                           uint64
	FailureForwardRejected                                   uint64
	FailureSemantic                                          uint64
	FailureCompletion                                        uint64
	FailureResourceRelease                                   uint64
	FailureTerminal                                          uint64
	PredictionDuration                                       *histogram.DurationHistogram
	EstimatorDuration                                        *histogram.DurationHistogram
	RouterBackpressure                                       PredictiveRouterBackpressureInput
	RequestAwareAction                                       string
	RequestAwareReason                                       string
	RequestAwarePressureSource                               string
	RequestAwarePressure                                     float64
	RequestAwareSelectionInputTokens                         int64
	RequestAwareReservedTokens                               int64
	RequestAwareAllowanceTokens                              int64
	RequestAwareEffectiveKV                                  int64
	RequestAwarePostAdmitKV                                  int64
	RequestAwareRemainingKV                                  int64
	RequestAwareRunning                                      int
	RequestAwareWaiting                                      int
	RequestAwareEffectiveSequences                           int
	RequestAwareAggregateTPSProxy                            float64
	RequestAwareMeanActiveTPSProxy                           float64
	RequestAwareProjectedTPSProxy                            float64
	RequestAwareTPSForecastValid                             bool
	RequestAwarePrefillClass                                 string
	RequestAwareEstimatedPrefillTokens                       int64
	RequestAwarePendingPrefillSequences                      int
	RequestAwarePendingPrefillTokens                         int64
	RequestAwarePostAdmitPendingPrefillTokens                int64
	RequestAwarePendingLongPrefillSequences                  int
	RequestAwarePendingQuiescentPrefillSequences             int
	RequestAwareLastDecisionPendingPrefillSequences          int
	RequestAwareLastDecisionPendingPrefillTokens             int64
	RequestAwareLastDecisionPostAdmitPendingPrefillTokens    int64
	RequestAwareLastDecisionPendingLongPrefillSequences      int
	RequestAwareLastDecisionPendingQuiescentPrefillSequences int
}

type PredictiveRouterBackpressureInput struct {
	Active               bool
	Activation           uint64
	Scope                string
	MinimumRunning       int
	InspectCapacity      int
	Applied              bool
	Reason               string
	Source               string
	ActivatedAt          time.Time
	Activations          uint64
	LatestRejectAt       time.Time
	PredictiveRunning    int
	RawRunning           int
	EffectiveRunning     int
	RawGlobalLimit       int
	EffectiveGlobalLimit int
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
	requestAwareAction := normalizeRequestAwareAction(input.RequestAwareAction)
	requestAwareReason := normalizeRequestAwareReason(input.RequestAwareReason)
	requestAwarePressureSource := normalizeRequestAwarePressureSource(input.RequestAwarePressureSource)
	requestAwarePrefillClass := normalizeRequestAwarePrefillClass(input.RequestAwarePrefillClass)
	fmt.Fprintf(w, "pig_predictive_admission_mode_info{mode=%q} 1\n", mode)
	fmt.Fprintf(w, "pig_predictive_admission_enabled %d\n", num.BoolAsInt(mode != "off"))
	fmt.Fprintf(w, "pig_predictive_admission_enforce %d\n", num.BoolAsInt(mode == "enforce"))
	fmt.Fprintf(w, "pig_predictive_admission_attempts_total %d\n", input.Attempts)
	fmt.Fprintf(w, "pig_predictive_admission_decisions_total{decision=%q} %d\n", "fit", input.Fits)
	fmt.Fprintf(w, "pig_predictive_admission_decisions_total{decision=%q} %d\n", "risk", input.Risks)
	fmt.Fprintf(w, "pig_predictive_admission_decisions_total{decision=%q} %d\n", "unknown", input.Unknown)
	fmt.Fprintf(w, "pig_predictive_admission_enforced_rejects_total %d\n", input.EnforcedRejects)
	fmt.Fprintf(w, "pig_predictive_admission_last_decision_info{reason=%q,source=%q} 1\n", input.LastReason, input.LastSource)
	fmt.Fprintf(w, "pig_predictive_admission_last_reject_info{reason=%q,source=%q,scope=%q} 1\n", lastRejectReason, lastRejectSource, lastRejectScope)
	fmt.Fprintf(w, "pig_predictive_admission_last_reject_at_seconds %.6f\n", predictiveMetricUnixSeconds(input.LastRejectAt))
	fmt.Fprintf(w, "pig_predictive_admission_intake_open %d\n", num.BoolAsInt(input.IntakeOpen))
	fmt.Fprintf(w, "pig_predictive_admission_reservations %d\n", input.Reservations)
	fmt.Fprintf(w, "pig_predictive_admission_virtual_decode_sequences %d\n", input.VirtualDecodeSequences)
	fmt.Fprintf(w, "pig_predictive_admission_forwarded_pending_prefills %d\n", input.ForwardedPendingPrefills)
	fmt.Fprintf(w, "pig_predictive_admission_forwarded_pending_prefill_tokens %d\n", input.ForwardedPendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_admission_forwarded_pending_prefill_attribution_valid %d\n", num.BoolAsInt(input.ForwardedPendingPrefillAttributionValid))
	fmt.Fprintf(w, "pig_predictive_admission_retired_reservations %d\n", input.RetiredReservations)
	fmt.Fprintf(w, "pig_predictive_admission_retired_evictions_total %d\n", input.RetiredEvictions)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_active %d\n", num.BoolAsInt(input.RouterBackpressure.Active))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_applied %d\n", num.BoolAsInt(input.RouterBackpressure.Applied))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_state_info{scope=%q,reason=%q,source=%q} 1\n", backpressureScope, backpressureReason, backpressureSource)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_activation %d\n", input.RouterBackpressure.Activation)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_activated_at_seconds %.6f\n", predictiveMetricUnixSeconds(input.RouterBackpressure.ActivatedAt))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_activations_total %d\n", input.RouterBackpressure.Activations)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_latest_load_reject_at_seconds %.6f\n", predictiveMetricUnixSeconds(input.RouterBackpressure.LatestRejectAt))
	fmt.Fprintf(w, "pig_predictive_router_backpressure_predictive_running %d\n", input.RouterBackpressure.PredictiveRunning)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_raw_running %d\n", input.RouterBackpressure.RawRunning)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_effective_running %d\n", input.RouterBackpressure.EffectiveRunning)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_raw_global_limit %d\n", input.RouterBackpressure.RawGlobalLimit)
	fmt.Fprintf(w, "pig_predictive_router_backpressure_effective_global_limit %d\n", input.RouterBackpressure.EffectiveGlobalLimit)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_info{action=%q,reason=%q,pressure_source=%q,prefill_class=%q} 1\n", requestAwareAction, requestAwareReason, requestAwarePressureSource, requestAwarePrefillClass)
	fmt.Fprintf(w, "pig_predictive_request_aware_pressure %.6f\n", input.RequestAwarePressure)
	fmt.Fprintf(w, "pig_predictive_request_aware_selection_input_tokens %d\n", input.RequestAwareSelectionInputTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_reserved_tokens %d\n", input.RequestAwareReservedTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_allowance_tokens %d\n", input.RequestAwareAllowanceTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_effective_kv_tokens %d\n", input.RequestAwareEffectiveKV)
	fmt.Fprintf(w, "pig_predictive_request_aware_post_admit_kv_tokens %d\n", input.RequestAwarePostAdmitKV)
	fmt.Fprintf(w, "pig_predictive_request_aware_remaining_kv_tokens %d\n", input.RequestAwareRemainingKV)
	fmt.Fprintf(w, "pig_predictive_request_aware_running %d\n", input.RequestAwareRunning)
	fmt.Fprintf(w, "pig_predictive_request_aware_waiting %d\n", input.RequestAwareWaiting)
	fmt.Fprintf(w, "pig_predictive_request_aware_effective_sequences %d\n", input.RequestAwareEffectiveSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_aggregate_tps_proxy %.6f\n", input.RequestAwareAggregateTPSProxy)
	fmt.Fprintf(w, "pig_predictive_request_aware_mean_active_tps_proxy %.6f\n", input.RequestAwareMeanActiveTPSProxy)
	fmt.Fprintf(w, "pig_predictive_request_aware_projected_tps_proxy %.6f\n", input.RequestAwareProjectedTPSProxy)
	fmt.Fprintf(w, "pig_predictive_request_aware_tps_forecast_valid %d\n", num.BoolAsInt(input.RequestAwareTPSForecastValid))
	fmt.Fprintf(w, "pig_predictive_request_aware_estimated_prefill_tokens %d\n", input.RequestAwareEstimatedPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_pending_prefill_sequences %d\n", input.RequestAwarePendingPrefillSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_pending_prefill_tokens %d\n", input.RequestAwarePendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_post_admit_pending_prefill_tokens %d\n", input.RequestAwarePostAdmitPendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_pending_long_prefill_sequences %d\n", input.RequestAwarePendingLongPrefillSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_pending_quiescent_prefill_sequences %d\n", input.RequestAwarePendingQuiescentPrefillSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_pending_prefill_sequences %d\n", input.RequestAwareLastDecisionPendingPrefillSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_pending_prefill_tokens %d\n", input.RequestAwareLastDecisionPendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_post_admit_pending_prefill_tokens %d\n", input.RequestAwareLastDecisionPostAdmitPendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_pending_long_prefill_sequences %d\n", input.RequestAwareLastDecisionPendingLongPrefillSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_pending_quiescent_prefill_sequences %d\n", input.RequestAwareLastDecisionPendingQuiescentPrefillSequences)
	fmt.Fprintf(w, "pig_predictive_router_inspect_capacity %d\n", input.RouterBackpressure.InspectCapacity)
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

func normalizeRequestAwareAction(value string) string {
	switch value {
	case "admit", "size_protect", "hard_protect":
		return value
	default:
		return "unknown"
	}
}

func normalizeRequestAwareReason(value string) string {
	switch value {
	case "open", "within_budget", "request_size", "stale", "preemption", "kv", "prefill_budget", "prefill_concurrency", "prefill_exclusive", "prefill_busy", "duplicate", "unavailable", "invalid":
		return value
	default:
		return "unknown"
	}
}

func normalizeRequestAwarePressureSource(value string) string {
	switch value {
	case "none", "kv", "waiting", "tps", "prefill":
		return value
	default:
		return "none"
	}
}

func normalizeRequestAwarePrefillClass(value string) string {
	switch value {
	case "regular", "weighted", "exclusive", "quiescent":
		return value
	default:
		return "unknown"
	}
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
