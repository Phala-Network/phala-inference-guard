package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
)

func TestWritePredictiveAdmissionExposesBoundedOperationalState(t *testing.T) {
	prediction := histogram.NewPredictiveDurationHistogram()
	estimator := histogram.NewPredictiveDurationHistogram()
	prediction.Observe(3 * time.Millisecond)
	estimator.Observe(4 * time.Microsecond)
	var out bytes.Buffer
	WritePredictiveAdmission(&out, PredictiveAdmissionInput{
		Mode:                   "enforce",
		Attempts:               9,
		Fits:                   5,
		Risks:                  3,
		Unknown:                1,
		EnforcedRejects:        4,
		LastReason:             "existing_tps_at_risk",
		LastSource:             "calibrated",
		LastSamples:            7,
		IntakeOpen:             true,
		Reservations:           2,
		RetiredReservations:    3,
		RetiredEvictions:       1,
		LearningAccepted:       11,
		LearningRejected:       2,
		LearningInvalidations:  3,
		LearningCells:          4,
		LearningGlobalSamples:  5,
		InputSizeAccepted:      12,
		InputSizeRejected:      2,
		InputSizeInvalidations: 1,
		InputSizeStored:        9,
		InputSizeClasses:       3,
		InputSizeCold:          8,
		InputSizeLearned:       4,
		InputSizeLastSource:    "learned",
		InputSizeLastSamples:   3,
		InputSizeLastRawHigh:   100,
		InputSizeLastUpper:     72,
		TPSBackend:             6,
		TPSLocal:               7,
		TPSMissing:             8,
		TPSRejected:            9,
		ShadowObservations: PredictiveShadowObservationInput{
			Active: 2, Created: 10, Terminated: 7, Qualified: 5, Censored: 2, Dropped: 1,
		},
		FailureClose:       1,
		FailureDecide:      2,
		FailureForward:     3,
		FailureSemantic:    4,
		FailureCompletion:  5,
		FailureTerminal:    6,
		PredictionDuration: &prediction,
		EstimatorDuration:  &estimator,
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
		`pig_predictive_admission_last_decision_info{reason="existing_tps_at_risk",source="calibrated"} 1`,
		"pig_predictive_admission_reservations 2",
		"pig_predictive_admission_intake_open 1",
		"pig_predictive_admission_retired_evictions_total 1",
		`pig_predictive_learning_samples_total{result="accepted"} 11`,
		"pig_predictive_learning_invalidations_total 3",
		"pig_predictive_learning_global_samples 5",
		`pig_predictive_input_size_samples_total{result="accepted"} 12`,
		`pig_predictive_input_size_estimates_total{source="learned"} 4`,
		`pig_predictive_input_size_last_estimate_info{source="learned"} 1`,
		"pig_predictive_input_size_last_upper_tokens 72",
		`pig_predictive_tps_outcomes_total{result="backend"} 6`,
		`pig_predictive_tps_outcomes_total{result="local"} 7`,
		`pig_predictive_tps_outcomes_total{result="missing"} 8`,
		`pig_predictive_tps_outcomes_total{result="rejected"} 9`,
		"pig_predictive_shadow_observations 2",
		`pig_predictive_shadow_observations_total{result="created"} 10`,
		`pig_predictive_shadow_observations_total{result="terminated"} 7`,
		`pig_predictive_shadow_observations_total{result="qualified"} 5`,
		`pig_predictive_shadow_observations_total{result="censored"} 2`,
		`pig_predictive_shadow_observations_total{result="dropped"} 1`,
		`pig_predictive_admission_failures_total{phase="forward"} 3`,
		`pig_predictive_admission_failures_total{phase="semantic"} 4`,
		`pig_predictive_admission_failures_total{phase="completion"} 5`,
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
		"pig_predictive_admission_prediction_duration_seconds_count 0",
		"pig_predictive_admission_estimator_duration_seconds_count 0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, got)
		}
	}
}
