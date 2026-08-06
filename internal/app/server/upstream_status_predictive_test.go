package server

import (
	"testing"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestPredictiveEnforceUpstreamStatusUsesRequestAwareProjectionOnly(t *testing.T) {
	tests := []struct {
		name      string
		telemetry predictiveAdmissionTelemetrySnapshot
		want      int
	}{
		{name: "open", want: upstreamStatusGreen},
		{
			name: "selective",
			telemetry: predictiveAdmissionTelemetrySnapshot{RouterBackpressure: predictiveRouterBackpressureSnapshot{
				Active: true, Scope: predictiveProtectionScopeLoad, InspectCapacity: 1,
				Reason: domainpredictive.ReasonRequestSizeAtPressure, Source: runtimepredictive.PredictionSourceDeterministic,
			}},
			want: upstreamStatusYellow,
		},
		{
			name: "hard load",
			telemetry: predictiveAdmissionTelemetrySnapshot{RouterBackpressure: predictiveRouterBackpressureSnapshot{
				Active: true, Scope: predictiveProtectionScopeLoad, InspectCapacity: 0,
				Reason: domainpredictive.ReasonKVOverBudget, Source: runtimepredictive.PredictionSourceDeterministic,
			}},
			want: upstreamStatusRed,
		},
		{
			name: "availability",
			telemetry: predictiveAdmissionTelemetrySnapshot{RouterBackpressure: predictiveRouterBackpressureSnapshot{
				Active: true, Scope: predictiveProtectionScopeAvailability, InspectCapacity: 0,
				Reason: domainpredictive.ReasonMetricsStale, Source: runtimepredictive.PredictionSourceUnavailable,
			}},
			want: upstreamStatusRed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := &proxyServer{
				cfg: config{PredictiveAdmissionMode: "enforce"},
				predictiveShadow: &routerMetricsPredictiveShadow{
					telemetry: test.telemetry,
				},
			}
			if got := srv.upstreamStatusCode(); got != test.want {
				t.Fatalf("enforce upstream status=%d, want %d from request-aware projection", got, test.want)
			}
		})
	}
}

func TestPredictiveEnforceUpstreamStatusFailsClosedWithoutProvider(t *testing.T) {
	srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}}
	if got := srv.upstreamStatusCode(); got != upstreamStatusRed {
		t.Fatalf("enforce status without predictive provider=%d, want red", got)
	}

	shadow := &proxyServer{cfg: config{PredictiveAdmissionMode: "shadow"}}
	if got := shadow.upstreamStatusCode(); got != upstreamStatusUnknown {
		t.Fatalf("shadow legacy status without QoS state=%d, want unknown", got)
	}
}
