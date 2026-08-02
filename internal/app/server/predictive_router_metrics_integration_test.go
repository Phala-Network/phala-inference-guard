package server

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	appdynamic "github.com/Phala-Network/phala-inference-guard/internal/app/dynamic"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type routerMetricsPredictiveShadow struct {
	telemetry predictiveAdmissionTelemetrySnapshot
}

func (*routerMetricsPredictiveShadow) DecideAndReserve(context.Context, string, predictiveShadowInput) predictiveShadowReservation {
	return nil
}

func (*routerMetricsPredictiveShadow) Close() error { return nil }

func (s *routerMetricsPredictiveShadow) PredictiveAdmissionTelemetry() predictiveAdmissionTelemetrySnapshot {
	return s.telemetry
}

func TestPredictiveRejectProjectsProtectionIntoRouterConsumedMetrics(t *testing.T) {
	dynamicController := appdynamic.New(appdynamic.Config{
		GlobalGreen:  50,
		GlobalYellow: 50,
		GlobalRed:    50,
	}, appdynamic.Dependencies{
		GlobalLimit: func() int { return 50 },
	})
	predictiveShadow := &routerMetricsPredictiveShadow{telemetry: predictiveAdmissionTelemetrySnapshot{
		Attempts: predictiveAttemptSnapshot{
			Attempts:    1,
			Risks:       1,
			LastReason:  domainpredictive.ReasonExistingTPSAtRisk,
			LastSource:  runtimepredictive.PredictionSourceCalibrated,
			LastSamples: 7,
		},
		Manager: runtimepredictive.Snapshot{
			IntakeOpen: true,
			Virtual: domainpredictive.VirtualStateInterval{
				Upper: domainpredictive.VirtualState{DecodeSequences: 1},
			},
		},
		RouterBackpressure: predictiveRouterBackpressureSnapshot{
			Active:      true,
			Reason:      domainpredictive.ReasonExistingTPSAtRisk,
			Source:      runtimepredictive.PredictionSourceCalibrated,
			Samples:     7,
			ActivatedAt: time.Unix(100, 0),
			Until:       time.Unix(102, 0),
			Hold:        2 * time.Second,
			Activations: 1,
		},
	}}
	srv := &proxyServer{
		cfg:               config{PredictiveAdmissionMode: "enforce"},
		dynamicController: dynamicController,
		predictiveShadow:  predictiveShadow,
	}
	srv.predictiveEnforcedRejects.Store(1)

	var out bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&out)
	got := out.String()
	for _, want := range []string{
		"pig_predictive_admission_enforced_rejects_total 1",
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_predictive_router_backpressure_predictive_running 1",
		`pig_predictive_router_backpressure_state_info{reason="existing_tps_at_risk",source="calibrated"} 1`,
		"pig_dynamic_observed_running_raw 0",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit_raw 50",
		"pig_dynamic_global_limit 1",
		"pig_dynamic_admission_limit 50",
		"pig_dynamic_router_backpressure_active 1",
		"pig_dynamic_router_backpressure_applied 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("combined predictive/dynamic metrics missing %q:\n%s", want, got)
		}
	}
}
