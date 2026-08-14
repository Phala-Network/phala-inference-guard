package metrics

import (
	"fmt"
	"io"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type PredictiveAdmissionInput struct {
	Mode                                   string
	CapabilityProfileSource                string
	CapabilityProfileSchema                string
	CapabilityInitializationReason         string
	CapabilityKVCapacityTokens             int64
	CapabilityKVBlockSize                  int64
	CapabilityKVHardLimitTokens            int64
	CapabilityMaxModelLenTokens            int64
	CapabilityMaximumAdmissibleInputTokens int64
	CapabilityPrefillRegularTokens         int64
	CapabilityPrefillExclusiveTokens       int64
	CapabilityPrefillQuiescentTokens       int64
	CapabilityPrefillContendedBudgetTokens int64
	CapabilityPrefillAggregateBudgetTokens int64
	Attempts                               uint64
	Fits                                   uint64
	Risks                                  uint64
	Unknown                                uint64
	// EnforcedRejects counts HTTP requests for which the proxy emitted an
	// enforced predictive rejection. Router protection is published earlier
	// from RouterBackpressure and must never be inferred from this counter.
	EnforcedRejects                        uint64
	LastReason                             string
	LastSource                             string
	LastRejectReason                       string
	LastRejectSource                       string
	LastRejectScope                        string
	LastRejectAt                           time.Time
	IntakeOpen                             bool
	Reservations                           int
	VirtualDecodeSequences                 int
	ForwardedPendingPrefills               int
	ForwardedPendingPrefillTokens          int64
	FailureClose                           uint64
	FailureDecide                          uint64
	FailureForward                         uint64
	FailureFirstByte                       uint64
	FailureTerminal                        uint64
	PredictionDuration                     *histogram.DurationHistogram
	BodyReadDuration                       *histogram.DurationHistogram
	EstimatorDuration                      *histogram.DurationHistogram
	PreForwardDuration                     *histogram.DurationHistogram
	RouterBackpressure                     PredictiveRouterBackpressureInput
	AdmissionAction                        string
	AdmissionReason                        string
	AdmissionPressureSource                string
	AdmissionSelectionInputTokens          int64
	AdmissionReservedTokens                int64
	AdmissionAllowanceTokens               int64
	AdmissionEffectiveKV                   int64
	AdmissionPostAdmitKV                   int64
	AdmissionRemainingKV                   int64
	AdmissionRunning                       int
	AdmissionWaiting                       int
	AdmissionEffectiveSequences            int
	AdmissionAggregateTPS                  float64
	AdmissionMeanActiveTPS                 float64
	AdmissionPrefillClass                  string
	AdmissionEstimatedPrefillTokens        int64
	AdmissionPendingPrefillSequences       int
	AdmissionPendingPrefillTokens          int64
	AdmissionPostAdmitPendingPrefillTokens int64
	AdmissionPendingExclusiveSequences     int
	AdmissionPendingQuiescentSequences     int
	TPSReference                           float64
	TPSWindowReady                         bool
	TPSWindowQualifiedSamples              uint64
	TPSWindowQualifiedSequenceSeconds      float64
	TPSWindowAggregate                     float64
	TPSWindowMeanActive                    float64
	TPSSequenceLimit                       int64
	TPSCurrentSequences                    int64
	TPSPostAdmitSequences                  int64
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
		mode = "unknown"
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
	admissionAction := normalizeAdmissionAction(input.AdmissionAction)
	admissionReason := normalizeAdmissionReason(input.AdmissionReason)
	admissionPressureSource := normalizeAdmissionPressureSource(input.AdmissionPressureSource)
	admissionPrefillClass := normalizeAdmissionPrefillClass(input.AdmissionPrefillClass)
	capabilitySource := normalizeCapabilityProfileSource(input.CapabilityProfileSource)
	capabilityReason := normalizeCapabilityInitializationReason(input.CapabilityInitializationReason)
	capabilitySchema := input.CapabilityProfileSchema
	if capabilitySchema == "" {
		capabilitySchema = "unknown"
	}
	fmt.Fprintf(w, "pig_predictive_admission_mode_info{mode=%q} 1\n", mode)
	fmt.Fprintf(w, "pig_predictive_capability_profile_info{schema=%q,source=%q,reason=%q} 1\n", capabilitySchema, capabilitySource, capabilityReason)
	fmt.Fprintf(w, "pig_predictive_capability_kv_capacity_tokens %d\n", input.CapabilityKVCapacityTokens)
	fmt.Fprintf(w, "pig_predictive_capability_kv_block_size %d\n", input.CapabilityKVBlockSize)
	fmt.Fprintf(w, "pig_predictive_capability_kv_hard_limit_tokens %d\n", input.CapabilityKVHardLimitTokens)
	fmt.Fprintf(w, "pig_predictive_capability_max_model_len_tokens %d\n", input.CapabilityMaxModelLenTokens)
	fmt.Fprintf(w, "pig_predictive_capability_maximum_admissible_input_tokens %d\n", input.CapabilityMaximumAdmissibleInputTokens)
	fmt.Fprintf(w, "pig_predictive_capability_prefill_regular_tokens %d\n", input.CapabilityPrefillRegularTokens)
	fmt.Fprintf(w, "pig_predictive_capability_prefill_exclusive_tokens %d\n", input.CapabilityPrefillExclusiveTokens)
	fmt.Fprintf(w, "pig_predictive_capability_prefill_quiescent_tokens %d\n", input.CapabilityPrefillQuiescentTokens)
	fmt.Fprintf(w, "pig_predictive_capability_prefill_contended_budget_tokens %d\n", input.CapabilityPrefillContendedBudgetTokens)
	fmt.Fprintf(w, "pig_predictive_capability_prefill_aggregate_budget_tokens %d\n", input.CapabilityPrefillAggregateBudgetTokens)
	fmt.Fprintf(w, "pig_predictive_admission_enabled %d\n", num.BoolAsInt(mode == "shadow" || mode == "enforce"))
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
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_info{action=%q,reason=%q,pressure_source=%q,prefill_class=%q} 1\n", admissionAction, admissionReason, admissionPressureSource, admissionPrefillClass)
	fmt.Fprintf(w, "pig_predictive_request_aware_pressure %.6f\n", 0.0)
	fmt.Fprintf(w, "pig_predictive_request_aware_selection_input_tokens %d\n", input.AdmissionSelectionInputTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_reserved_tokens %d\n", input.AdmissionReservedTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_allowance_tokens %d\n", input.AdmissionAllowanceTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_effective_kv_tokens %d\n", input.AdmissionEffectiveKV)
	fmt.Fprintf(w, "pig_predictive_request_aware_post_admit_kv_tokens %d\n", input.AdmissionPostAdmitKV)
	fmt.Fprintf(w, "pig_predictive_request_aware_remaining_kv_tokens %d\n", input.AdmissionRemainingKV)
	fmt.Fprintf(w, "pig_predictive_request_aware_running %d\n", input.AdmissionRunning)
	fmt.Fprintf(w, "pig_predictive_request_aware_waiting %d\n", input.AdmissionWaiting)
	fmt.Fprintf(w, "pig_predictive_request_aware_effective_sequences %d\n", input.AdmissionEffectiveSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_aggregate_tps_proxy %.6f\n", input.AdmissionAggregateTPS)
	fmt.Fprintf(w, "pig_predictive_request_aware_mean_active_tps_proxy %.6f\n", input.AdmissionMeanActiveTPS)
	fmt.Fprintf(w, "pig_predictive_request_aware_estimated_prefill_tokens %d\n", input.AdmissionEstimatedPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_pending_prefill_sequences %d\n", input.AdmissionPendingPrefillSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_pending_prefill_tokens %d\n", input.AdmissionPendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_post_admit_pending_prefill_tokens %d\n", input.AdmissionPostAdmitPendingPrefillTokens)
	longPrefills := input.AdmissionPendingExclusiveSequences + input.AdmissionPendingQuiescentSequences
	fmt.Fprintf(w, "pig_predictive_request_aware_pending_long_prefill_sequences %d\n", longPrefills)
	fmt.Fprintf(w, "pig_predictive_request_aware_pending_quiescent_prefill_sequences %d\n", input.AdmissionPendingQuiescentSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_pending_prefill_sequences %d\n", input.AdmissionPendingPrefillSequences)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_pending_prefill_tokens %d\n", input.AdmissionPendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_post_admit_pending_prefill_tokens %d\n", input.AdmissionPostAdmitPendingPrefillTokens)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_pending_long_prefill_sequences %d\n", longPrefills)
	fmt.Fprintf(w, "pig_predictive_request_aware_last_decision_pending_quiescent_prefill_sequences %d\n", input.AdmissionPendingQuiescentSequences)
	fmt.Fprintf(w, "pig_predictive_tps_reference %.6f\n", input.TPSReference)
	fmt.Fprintf(w, "pig_predictive_tps_window_ready %d\n", num.BoolAsInt(input.TPSWindowReady))
	fmt.Fprintf(w, "pig_predictive_tps_window_qualified_samples %d\n", input.TPSWindowQualifiedSamples)
	fmt.Fprintf(w, "pig_predictive_tps_window_qualified_sequence_seconds %.6f\n", input.TPSWindowQualifiedSequenceSeconds)
	fmt.Fprintf(w, "pig_predictive_tps_window_aggregate %.6f\n", input.TPSWindowAggregate)
	fmt.Fprintf(w, "pig_predictive_tps_window_mean_active %.6f\n", input.TPSWindowMeanActive)
	fmt.Fprintf(w, "pig_predictive_tps_sequence_limit %d\n", input.TPSSequenceLimit)
	fmt.Fprintf(w, "pig_predictive_tps_current_sequences %d\n", input.TPSCurrentSequences)
	fmt.Fprintf(w, "pig_predictive_tps_post_admit_sequences %d\n", input.TPSPostAdmitSequences)
	fmt.Fprintf(w, "pig_predictive_router_inspect_capacity %d\n", input.RouterBackpressure.InspectCapacity)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "close", input.FailureClose)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "decide", input.FailureDecide)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "forward", input.FailureForward)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "prefill", input.FailureFirstByte)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "terminal", input.FailureTerminal)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_prediction_duration_seconds", input.PredictionDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_body_read_duration_seconds", input.BodyReadDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_estimator_duration_seconds", input.EstimatorDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_pre_forward_duration_seconds", input.PreForwardDuration)
}

func normalizeCapabilityProfileSource(value string) string {
	switch value {
	case "explicit", "automatic":
		return value
	default:
		return "unknown"
	}
}

func normalizeCapabilityInitializationReason(value string) string {
	switch value {
	case "explicit_override", "metadata":
		return value
	default:
		return "unknown"
	}
}

func normalizeAdmissionAction(value string) string {
	switch value {
	case "admit", "size_protect", "hard_protect":
		return value
	default:
		return "unknown"
	}
}

func normalizeAdmissionReason(value string) string {
	switch value {
	case "open", "controller_unavailable", "observation_missing", "observation_invalid", "observation_stale", "invalid_request", "input_limit", "kv_capacity", "prefill_contention", "prefill_budget", "prefill_exclusive", "prefill_quiescent", "tps_reference", "capability_drift", "resource_exhausted", "counter_overflow", "closed":
		return value
	default:
		return "unknown"
	}
}

func normalizeAdmissionPressureSource(value string) string {
	switch value {
	case "none", "prefill", "tps":
		return value
	default:
		return "none"
	}
}

func normalizeAdmissionPrefillClass(value string) string {
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
