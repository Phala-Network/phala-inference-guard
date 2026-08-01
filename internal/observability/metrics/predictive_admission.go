package metrics

import (
	"fmt"
	"io"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type PredictiveAdmissionInput struct {
	Mode                   string
	Attempts               uint64
	Fits                   uint64
	Risks                  uint64
	Unknown                uint64
	EnforcedRejects        uint64
	LastReason             string
	LastSource             string
	LastSamples            int
	IntakeOpen             bool
	Reservations           int
	RetiredReservations    int
	RetiredEvictions       uint64
	LearningAccepted       uint64
	LearningRejected       uint64
	LearningInvalidations  uint64
	LearningCells          int
	LearningGlobalSamples  int
	InputSizeAccepted      uint64
	InputSizeRejected      uint64
	InputSizeInvalidations uint64
	InputSizeStored        int
	InputSizeClasses       int
	InputSizeCold          uint64
	InputSizeLearned       uint64
	InputSizeLastSource    string
	InputSizeLastSamples   int
	InputSizeLastRawHigh   int64
	InputSizeLastUpper     int64
	TPSBackend             uint64
	TPSLocal               uint64
	TPSMissing             uint64
	TPSRejected            uint64
	ShadowObservations     PredictiveShadowObservationInput
	FailureClose           uint64
	FailureDecide          uint64
	FailureForward         uint64
	FailureSemantic        uint64
	FailureCompletion      uint64
	FailureTerminal        uint64
	PredictionDuration     *histogram.DurationHistogram
	EstimatorDuration      *histogram.DurationHistogram
}

type PredictiveShadowObservationInput struct {
	Active     int
	Created    uint64
	Terminated uint64
	Qualified  uint64
	Censored   uint64
	Dropped    uint64
}

func WritePredictiveAdmission(w io.Writer, input PredictiveAdmissionInput) {
	mode := input.Mode
	if mode != "shadow" && mode != "enforce" {
		mode = "off"
	}
	fmt.Fprintf(w, "pig_predictive_admission_mode_info{mode=%q} 1\n", mode)
	fmt.Fprintf(w, "pig_predictive_admission_enabled %d\n", num.BoolAsInt(mode != "off"))
	fmt.Fprintf(w, "pig_predictive_admission_enforce %d\n", num.BoolAsInt(mode == "enforce"))
	fmt.Fprintf(w, "pig_predictive_admission_attempts_total %d\n", input.Attempts)
	fmt.Fprintf(w, "pig_predictive_admission_decisions_total{decision=%q} %d\n", "fit", input.Fits)
	fmt.Fprintf(w, "pig_predictive_admission_decisions_total{decision=%q} %d\n", "risk", input.Risks)
	fmt.Fprintf(w, "pig_predictive_admission_decisions_total{decision=%q} %d\n", "unknown", input.Unknown)
	fmt.Fprintf(w, "pig_predictive_admission_enforced_rejects_total %d\n", input.EnforcedRejects)
	fmt.Fprintf(w, "pig_predictive_admission_last_decision_info{reason=%q,source=%q} 1\n", input.LastReason, input.LastSource)
	fmt.Fprintf(w, "pig_predictive_admission_last_samples %d\n", input.LastSamples)
	fmt.Fprintf(w, "pig_predictive_admission_intake_open %d\n", num.BoolAsInt(input.IntakeOpen))
	fmt.Fprintf(w, "pig_predictive_admission_reservations %d\n", input.Reservations)
	fmt.Fprintf(w, "pig_predictive_admission_retired_reservations %d\n", input.RetiredReservations)
	fmt.Fprintf(w, "pig_predictive_admission_retired_evictions_total %d\n", input.RetiredEvictions)
	fmt.Fprintf(w, "pig_predictive_learning_samples_total{result=%q} %d\n", "accepted", input.LearningAccepted)
	fmt.Fprintf(w, "pig_predictive_learning_samples_total{result=%q} %d\n", "rejected", input.LearningRejected)
	fmt.Fprintf(w, "pig_predictive_learning_invalidations_total %d\n", input.LearningInvalidations)
	fmt.Fprintf(w, "pig_predictive_learning_cells %d\n", input.LearningCells)
	fmt.Fprintf(w, "pig_predictive_learning_global_samples %d\n", input.LearningGlobalSamples)
	fmt.Fprintf(w, "pig_predictive_input_size_samples_total{result=%q} %d\n", "accepted", input.InputSizeAccepted)
	fmt.Fprintf(w, "pig_predictive_input_size_samples_total{result=%q} %d\n", "rejected", input.InputSizeRejected)
	fmt.Fprintf(w, "pig_predictive_input_size_invalidations_total %d\n", input.InputSizeInvalidations)
	fmt.Fprintf(w, "pig_predictive_input_size_samples_stored %d\n", input.InputSizeStored)
	fmt.Fprintf(w, "pig_predictive_input_size_classes %d\n", input.InputSizeClasses)
	fmt.Fprintf(w, "pig_predictive_input_size_estimates_total{source=%q} %d\n", "cold", input.InputSizeCold)
	fmt.Fprintf(w, "pig_predictive_input_size_estimates_total{source=%q} %d\n", "learned", input.InputSizeLearned)
	fmt.Fprintf(w, "pig_predictive_input_size_last_estimate_info{source=%q} 1\n", input.InputSizeLastSource)
	fmt.Fprintf(w, "pig_predictive_input_size_last_samples %d\n", input.InputSizeLastSamples)
	fmt.Fprintf(w, "pig_predictive_input_size_last_raw_high_tokens %d\n", input.InputSizeLastRawHigh)
	fmt.Fprintf(w, "pig_predictive_input_size_last_upper_tokens %d\n", input.InputSizeLastUpper)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "backend", input.TPSBackend)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "local", input.TPSLocal)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "missing", input.TPSMissing)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "rejected", input.TPSRejected)
	fmt.Fprintf(w, "pig_predictive_shadow_observations %d\n", input.ShadowObservations.Active)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "created", input.ShadowObservations.Created)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "terminated", input.ShadowObservations.Terminated)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "qualified", input.ShadowObservations.Qualified)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "censored", input.ShadowObservations.Censored)
	fmt.Fprintf(w, "pig_predictive_shadow_observations_total{result=%q} %d\n", "dropped", input.ShadowObservations.Dropped)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "close", input.FailureClose)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "decide", input.FailureDecide)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "forward", input.FailureForward)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "semantic", input.FailureSemantic)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "completion", input.FailureCompletion)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "terminal", input.FailureTerminal)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_prediction_duration_seconds", input.PredictionDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_estimator_duration_seconds", input.EstimatorDuration)
}

func writePredictiveDurationHistogram(w io.Writer, name string, value *histogram.DurationHistogram) {
	if value != nil {
		histogram.WriteDurationHistogram(w, name, value)
		return
	}
	empty := histogram.NewDurationHistogram()
	histogram.WriteDurationHistogram(w, name, &empty)
}
