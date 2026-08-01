package metrics

import (
	"fmt"
	"io"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type PredictiveAdmissionInput struct {
	Mode                  string
	Attempts              uint64
	Fits                  uint64
	Risks                 uint64
	Unknown               uint64
	EnforcedRejects       uint64
	LastReason            string
	LastSource            string
	LastSamples           int
	Reservations          int
	RetiredReservations   int
	RetiredEvictions      uint64
	LearningAccepted      uint64
	LearningRejected      uint64
	LearningInvalidations uint64
	LearningCells         int
	TPSBackend            uint64
	TPSLocal              uint64
	TPSMissing            uint64
	TPSRejected           uint64
	FailureClose          uint64
	FailureDecide         uint64
	FailureForward        uint64
	FailureSemantic       uint64
	FailureCompletion     uint64
	FailureTerminal       uint64
	PredictionDuration    *histogram.DurationHistogram
	RendererDuration      *histogram.DurationHistogram
	TokenizerDuration     *histogram.DurationHistogram
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
	fmt.Fprintf(w, "pig_predictive_admission_reservations %d\n", input.Reservations)
	fmt.Fprintf(w, "pig_predictive_admission_retired_reservations %d\n", input.RetiredReservations)
	fmt.Fprintf(w, "pig_predictive_admission_retired_evictions_total %d\n", input.RetiredEvictions)
	fmt.Fprintf(w, "pig_predictive_learning_samples_total{result=%q} %d\n", "accepted", input.LearningAccepted)
	fmt.Fprintf(w, "pig_predictive_learning_samples_total{result=%q} %d\n", "rejected", input.LearningRejected)
	fmt.Fprintf(w, "pig_predictive_learning_invalidations_total %d\n", input.LearningInvalidations)
	fmt.Fprintf(w, "pig_predictive_learning_cells %d\n", input.LearningCells)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "backend", input.TPSBackend)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "local", input.TPSLocal)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "missing", input.TPSMissing)
	fmt.Fprintf(w, "pig_predictive_tps_outcomes_total{result=%q} %d\n", "rejected", input.TPSRejected)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "close", input.FailureClose)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "decide", input.FailureDecide)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "forward", input.FailureForward)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "semantic", input.FailureSemantic)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "completion", input.FailureCompletion)
	fmt.Fprintf(w, "pig_predictive_admission_failures_total{phase=%q} %d\n", "terminal", input.FailureTerminal)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_prediction_duration_seconds", input.PredictionDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_renderer_duration_seconds", input.RendererDuration)
	writePredictiveDurationHistogram(w, "pig_predictive_admission_tokenizer_duration_seconds", input.TokenizerDuration)
}

func writePredictiveDurationHistogram(w io.Writer, name string, value *histogram.DurationHistogram) {
	if value != nil {
		histogram.WriteDurationHistogram(w, name, value)
		return
	}
	empty := histogram.NewDurationHistogram()
	histogram.WriteDurationHistogram(w, name, &empty)
}
