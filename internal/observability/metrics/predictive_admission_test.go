package metrics

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestWritePredictiveAdmissionExposesTPSOnlyState(t *testing.T) {
	var output bytes.Buffer
	WritePredictiveAdmission(&output, PredictiveAdmissionInput{
		Mode:                              "enforce",
		Attempts:                          10,
		Fits:                              7,
		Risks:                             2,
		Unknown:                           1,
		EnforcedRejects:                   2,
		LastReason:                        "tps_reference",
		LastSource:                        "deterministic",
		LastRejectReason:                  "tps_reference",
		LastRejectSource:                  "deterministic",
		LastRejectScope:                   "load",
		LastRejectAt:                      time.Unix(100, 0),
		IntakeOpen:                        true,
		Reservations:                      2,
		VirtualDecodeSequences:            6,
		SequenceLiabilities:               3,
		ResidualDebts:                     1,
		AdmissionAction:                   "load_protect",
		AdmissionReason:                   "tps_reference",
		AdmissionPressureSource:           "tps",
		AdmissionDemandSource:             "request",
		AdmissionDecodeSequences:          2,
		AdmissionRunning:                  4,
		AdmissionWaiting:                  1,
		AdmissionEffectiveSequences:       6,
		AdmissionGenerationDelta:          50,
		AdmissionPreemptionDelta:          1,
		AdmissionAggregateTPS:             100,
		AdmissionMeanActiveTPS:            25,
		AdmissionMeanActiveTPSValid:       true,
		AdmissionProjectedRunning:         7,
		AdmissionProjectedWindowSequences: 3,
		AdmissionRunningLimit:             192,
		AdmissionRunningLimitSource:       "admin",
		AdmissionWindowConcurrency:        48,
		TPSReference:                      25,
		TPSWindowReady:                    true,
		TPSWindowQualifiedSamples:         120,
		TPSWindowQualifiedSequenceSamples: 100,
		TPSWindowQualifiedSequenceSeconds: 200,
		TPSWindowAggregate:                100,
		TPSWindowMeanActive:               25,
		TPSLatestQualified:                true,
		TPSLatestAggregate:                80,
		TPSLatestMeanActive:               20,
		TPSLatestSequenceSeconds:          2.5,
		TPSUnobservedSequences:            2,
		TPSDecisionResult:                 "protect",
		TPSDecisionSubreason:              "waiting",
		RouterBackpressure: PredictiveRouterBackpressureInput{
			Active:               true,
			Applied:              true,
			Scope:                "load",
			Reason:               "tps_reference",
			Source:               "deterministic",
			PredictiveRunning:    5,
			EffectiveRunning:     5,
			EffectiveGlobalLimit: 5,
		},
	})
	body := output.String()
	for _, want := range []string{
		"pig_predictive_admission_mode_info{mode=\"enforce\"} 1",
		"pig_predictive_admission_attempts_total 10",
		"pig_predictive_admission_sequence_liabilities 3",
		"pig_predictive_admission_residual_debts 1",
		`pig_predictive_tps_last_decision_info{action="load_protect",reason="tps_reference",pressure_source="tps",result="protect",subreason="waiting",demand_source="request"} 1`,
		"pig_predictive_tps_request_decode_sequences 2",
		"pig_predictive_tps_observed_running 4",
		"pig_predictive_tps_observed_waiting 1",
		"pig_predictive_tps_generation_delta 50",
		"pig_predictive_tps_preemption_delta 1",
		"pig_predictive_tps_reference 25.000000",
		"pig_predictive_tps_window_mean_active 25.000000",
		"pig_predictive_tps_latest_interval_mean_active 20.000000",
		"pig_predictive_projected_running 7",
		"pig_predictive_running_limit 192",
		`pig_predictive_running_limit_info{source="admin"} 1`,
		"pig_predictive_projected_window_sequences 3",
		"pig_predictive_window_concurrency_limit 48",
		"pig_predictive_router_backpressure_active 1",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("TPS-only metrics missing %q:\n%s", want, body)
		}
	}
	for _, retired := range []string{
		"pig_predictive_capability_",
		"pig_predictive_cache_",
		"prefill_tokens",
		"input_kv_tokens",
		"effective_kv_tokens",
		"maximum_sequence_input_tokens",
		"pig_predictive_tps_request_output_limit",
		"pig_predictive_tps_sequence_limit",
		"pig_predictive_tps_current_sequences",
		"pig_predictive_tps_post_admit_sequences",
		"pig_predictive_tps_qos_budget",
	} {
		if strings.Contains(body, retired) {
			t.Fatalf("TPS-only metrics retained %q:\n%s", retired, body)
		}
	}
}

func TestWritePredictiveAdmissionNormalizesUntrustedLabels(t *testing.T) {
	var output bytes.Buffer
	WritePredictiveAdmission(&output, PredictiveAdmissionInput{
		Mode:                    "request-mode",
		AdmissionAction:         "request-action",
		AdmissionReason:         "request-reason",
		AdmissionPressureSource: "request-pressure",
		AdmissionDemandSource:   "request-demand",
		TPSDecisionResult:       "request-result",
		TPSDecisionSubreason:    "request-subreason",
	})
	body := output.String()
	for _, forbidden := range []string{
		"request-mode",
		"request-action",
		"request-reason",
		"request-pressure",
		"request-demand",
		"request-result",
		"request-subreason",
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("unbounded label escaped normalization: %q", forbidden)
		}
	}
	if !strings.Contains(
		body,
		`pig_predictive_tps_last_decision_info{action="unknown",reason="unknown",pressure_source="none",result="unknown",subreason="unknown",demand_source="unknown"} 1`,
	) {
		t.Fatalf("normalized decision label missing:\n%s", body)
	}
}

func TestWritePredictiveAdmissionNilHistogramsRemainScrapeable(t *testing.T) {
	var output bytes.Buffer
	WritePredictiveAdmission(&output, PredictiveAdmissionInput{})
	body := output.String()
	for _, name := range []string{
		"pig_predictive_admission_prediction_duration_seconds_count 0",
		"pig_predictive_admission_body_read_duration_seconds_count 0",
		"pig_predictive_admission_shape_scan_duration_seconds_count 0",
		"pig_predictive_admission_pre_forward_duration_seconds_count 0",
	} {
		if !strings.Contains(body, name) {
			t.Fatalf("empty histogram missing %q:\n%s", name, body)
		}
	}
}
