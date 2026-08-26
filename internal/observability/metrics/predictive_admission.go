package metrics

import (
	"fmt"
	"io"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type PredictiveAdmissionInput struct {
	Mode                              string
	Attempts                          uint64
	Fits                              uint64
	Risks                             uint64
	Unknown                           uint64
	EnforcedRejects                   uint64
	LastReason                        string
	LastSource                        string
	LastRejectReason                  string
	LastRejectSource                  string
	LastRejectScope                   string
	LastRejectAt                      time.Time
	IntakeOpen                        bool
	Reservations                      int
	VirtualDecodeSequences            int
	SequenceLiabilities               int64
	ResidualDebts                     int64
	FailureClose                      uint64
	FailureDecide                     uint64
	FailureForward                    uint64
	FailureFirstByte                  uint64
	FailureTerminal                   uint64
	PredictionDuration                *histogram.DurationHistogram
	BodyReadDuration                  *histogram.DurationHistogram
	ShapeScanDuration                 *histogram.DurationHistogram
	PreForwardDuration                *histogram.DurationHistogram
	RouterBackpressure                PredictiveRouterBackpressureInput
	AdmissionAction                   string
	AdmissionReason                   string
	AdmissionPressureSource           string
	AdmissionDemandSource             string
	AdmissionDecodeSequences          int64
	AdmissionRunning                  int
	AdmissionWaiting                  int
	AdmissionEffectiveSequences       int
	AdmissionGenerationDelta          uint64
	AdmissionPreemptionDelta          uint64
	AdmissionAggregateTPS             float64
	AdmissionMeanActiveTPS            float64
	AdmissionMeanActiveTPSValid       bool
	AdmissionProjectedRunning         int64
	AdmissionProjectedWindowSequences int64
	AdmissionRunningLimit             int64
	AdmissionRunningLimitSource       string
	AdmissionWindowConcurrency        int64
	TPSReference                      float64
	TPSWindowReady                    bool
	TPSWindowQualifiedSamples         uint64
	TPSWindowQualifiedSequenceSamples uint64
	TPSWindowQualifiedSequenceSeconds float64
	TPSWindowAggregate                float64
	TPSWindowMeanActive               float64
	TPSLatestQualified                bool
	TPSLatestAggregate                float64
	TPSLatestMeanActive               float64
	TPSLatestSequenceSeconds          float64
	TPSUnobservedSequences            int64
	TPSDecisionResult                 string
	TPSDecisionSubreason              string
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
	mode := normalizedValue(input.Mode, []string{"shadow", "enforce"}, "unknown")
	backpressureReason := nonempty(input.RouterBackpressure.Reason, "none")
	backpressureSource := nonempty(input.RouterBackpressure.Source, "unknown")
	backpressureScope := nonempty(input.RouterBackpressure.Scope, "none")
	lastRejectReason := nonempty(input.LastRejectReason, "none")
	lastRejectSource := nonempty(input.LastRejectSource, "unknown")
	lastRejectScope := nonempty(input.LastRejectScope, "none")
	admissionAction := normalizedValue(
		input.AdmissionAction,
		[]string{"admit", "request_protect", "load_protect", "availability_protect"},
		"unknown",
	)
	admissionReason := normalizedValue(
		input.AdmissionReason,
		[]string{
			"open",
			"controller_unavailable",
			"observation_missing",
			"observation_invalid",
			"observation_stale",
			"invalid_request",
			"tps_reference",
			"running_limit",
			"window_concurrency",
			"runtime_identity_drift",
			"resource_exhausted",
			"counter_overflow",
			"closed",
		},
		"unknown",
	)
	admissionPressureSource := normalizedValue(
		input.AdmissionPressureSource,
		[]string{"none", "tps", "running", "window", "availability", "request"},
		"none",
	)
	demandSource := normalizedValue(
		input.AdmissionDemandSource,
		[]string{"request", "fallback"},
		"unknown",
	)
	decisionResult := normalizedValue(
		input.TPSDecisionResult,
		[]string{"disabled", "admit", "protect", "invalid"},
		"unknown",
	)
	decisionSubreason := normalizedValue(
		input.TPSDecisionSubreason,
		[]string{
			"disabled",
			"invalid_state",
			"waiting",
			"preemption",
			"warming",
			"no_current_evidence",
			"healthy_window",
			"recovered_current",
			"below_reference",
		},
		"unknown",
	)

	fmt.Fprintf(w, "pig_predictive_admission_mode_info{mode=%q} 1\n", mode)
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
	fmt.Fprintf(w, "pig_predictive_admission_sequence_liabilities %d\n", input.SequenceLiabilities)
	fmt.Fprintf(w, "pig_predictive_admission_residual_debts %d\n", input.ResidualDebts)

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

	fmt.Fprintf(w, "pig_predictive_tps_last_decision_info{action=%q,reason=%q,pressure_source=%q,result=%q,subreason=%q,demand_source=%q} 1\n", admissionAction, admissionReason, admissionPressureSource, decisionResult, decisionSubreason, demandSource)
	fmt.Fprintf(w, "pig_predictive_tps_request_decode_sequences %d\n", input.AdmissionDecodeSequences)
	fmt.Fprintf(w, "pig_predictive_tps_observed_running %d\n", input.AdmissionRunning)
	fmt.Fprintf(w, "pig_predictive_tps_observed_waiting %d\n", input.AdmissionWaiting)
	fmt.Fprintf(w, "pig_predictive_tps_effective_sequences %d\n", input.AdmissionEffectiveSequences)
	fmt.Fprintf(w, "pig_predictive_tps_generation_delta %d\n", input.AdmissionGenerationDelta)
	fmt.Fprintf(w, "pig_predictive_tps_preemption_delta %d\n", input.AdmissionPreemptionDelta)
	fmt.Fprintf(w, "pig_predictive_tps_current_interval_aggregate %.6f\n", input.AdmissionAggregateTPS)
	fmt.Fprintf(w, "pig_predictive_tps_current_interval_mean_active %.6f\n", input.AdmissionMeanActiveTPS)
	fmt.Fprintf(w, "pig_predictive_tps_current_interval_mean_active_valid %d\n", num.BoolAsInt(input.AdmissionMeanActiveTPSValid))
	fmt.Fprintf(w, "pig_predictive_projected_running %d\n", input.AdmissionProjectedRunning)
	fmt.Fprintf(w, "pig_predictive_running_limit %d\n", input.AdmissionRunningLimit)
	fmt.Fprintf(w, "pig_predictive_running_limit_info{source=%q} 1\n", nonempty(input.AdmissionRunningLimitSource, "unknown"))
	fmt.Fprintf(w, "pig_predictive_projected_window_sequences %d\n", input.AdmissionProjectedWindowSequences)
	fmt.Fprintf(w, "pig_predictive_window_concurrency_limit %d\n", input.AdmissionWindowConcurrency)
	fmt.Fprintf(w, "pig_predictive_tps_reference %.6f\n", input.TPSReference)
	fmt.Fprintf(w, "pig_predictive_tps_window_ready %d\n", num.BoolAsInt(input.TPSWindowReady))
	fmt.Fprintf(w, "pig_predictive_tps_window_qualified_samples %d\n", input.TPSWindowQualifiedSamples)
	fmt.Fprintf(w, "pig_predictive_tps_window_qualified_sequence_samples %d\n", input.TPSWindowQualifiedSequenceSamples)
	fmt.Fprintf(w, "pig_predictive_tps_window_qualified_sequence_seconds %.6f\n", input.TPSWindowQualifiedSequenceSeconds)
	fmt.Fprintf(w, "pig_predictive_tps_window_aggregate %.6f\n", input.TPSWindowAggregate)
	fmt.Fprintf(w, "pig_predictive_tps_window_mean_active %.6f\n", input.TPSWindowMeanActive)
	fmt.Fprintf(w, "pig_predictive_tps_latest_interval_qualified %d\n", num.BoolAsInt(input.TPSLatestQualified))
	fmt.Fprintf(w, "pig_predictive_tps_latest_interval_aggregate %.6f\n", input.TPSLatestAggregate)
	fmt.Fprintf(w, "pig_predictive_tps_latest_interval_mean_active %.6f\n", input.TPSLatestMeanActive)
	fmt.Fprintf(w, "pig_predictive_tps_latest_interval_sequence_seconds %.6f\n", input.TPSLatestSequenceSeconds)
	fmt.Fprintf(w, "pig_predictive_tps_unobserved_sequences %d\n", input.TPSUnobservedSequences)
	fmt.Fprintf(w, "pig_predictive_router_inspect_capacity %d\n", input.RouterBackpressure.InspectCapacity)

	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "close", input.FailureClose)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "decide", input.FailureDecide)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "forward", input.FailureForward)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "first_byte", input.FailureFirstByte)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "terminal", input.FailureTerminal)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_prediction_duration_seconds", input.PredictionDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_body_read_duration_seconds", input.BodyReadDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_shape_scan_duration_seconds", input.ShapeScanDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_pre_forward_duration_seconds", input.PreForwardDuration)
}

func nonempty(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func normalizedValue(value string, allowed []string, fallback string) string {
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return fallback
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
