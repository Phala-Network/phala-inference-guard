package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestV0127AdapterMapsDecodeInterferenceToRequestScopedSizePressure(t *testing.T) {
	decision := requestAwareAdapterProtectedDecision(runtimepredictive.RequestAwareDecision{
		Action: runtimepredictive.RequestAwareSizeProtect,
		Reason: runtimepredictive.RequestAwareReason("decode_interference"),
	})
	if decision.Outcome != predictiveAdmissionOutcomeRequestReject ||
		decision.Reason != domainpredictive.ReasonRequestSizeAtPressure ||
		decision.Source != runtimepredictive.PredictionSourceDeterministic {
		t.Fatalf("Decode envelope mapping=%+v, want request-scoped size pressure", decision)
	}
}

func TestV0127HTTPDecodeEnvelopeRejectIsObservableWithoutRouterMislock(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "enforce")
	var decisionLogs []requestAwareDecisionLogEvent
	adapter.onDecision = func(event requestAwareDecisionLogEvent) {
		decisionLogs = append(decisionLogs, event)
	}
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer backend.Close()
	srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "enforce")
	defer srv.Close()

	oversized := serveRequestAwareHTTP(t, srv, strings.Repeat("a", 80*1024))
	if oversized.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("Decode envelope response/backend=%d/%d body=%q, want pre-forward 429/0",
			oversized.Code, backendCalls, oversized.Body.String())
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	decodeReason := runtimepredictive.RequestAwareReason("decode_interference")
	decodeSource := runtimepredictive.RequestAwarePressureSource("decode")
	if telemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		telemetry.RequestAware.Reason != decodeReason || telemetry.RequestAware.PressureSource != decodeSource ||
		telemetry.RequestAware.Pressure <= 1 || telemetry.RequestAware.EstimatedPrefillTokens <= 16*1024 ||
		telemetry.RequestAware.EstimatedPrefillTokens >= 64*1024 ||
		telemetry.RequestAware.LastDecisionPostAdmitPendingPrefillTokens != telemetry.RequestAware.EstimatedPrefillTokens ||
		telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.InspectCapacity != 0 ||
		telemetry.Attempts.LastRejectScope != predictiveProtectionScopeRequest ||
		srv.predictiveEnforcedRejects.Load() != 1 || srv.total429.Load() != 1 {
		t.Fatalf("Decode envelope telemetry=%+v enforced=%d total429=%d",
			telemetry, srv.predictiveEnforcedRejects.Load(), srv.total429.Load())
	}
	if len(decisionLogs) != 1 || !decisionLogs[0].Enforced ||
		decisionLogs[0].Action != runtimepredictive.RequestAwareSizeProtect ||
		decisionLogs[0].Reason != decodeReason || decisionLogs[0].HTTPReason != domainpredictive.ReasonRequestSizeAtPressure ||
		decisionLogs[0].Scope != predictiveProtectionScopeRequest || decisionLogs[0].PressureSource != decodeSource ||
		decisionLogs[0].Pressure <= 1 {
		t.Fatalf("Decode envelope decision logs=%+v", decisionLogs)
	}
	line := requestAwareDecisionLogLine(decisionLogs[0])
	for _, want := range []string{
		"enforced=true", "action=size_protect", "reason=decode_interference",
		"http_reason=request_size_at_pressure", "scope=request", "pressure_source=decode",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("Decode envelope log missing %q: %s", want, line)
		}
	}

	var rendered strings.Builder
	srv.writePredictiveAndDynamicMetrics(&rendered)
	for _, want := range []string{
		`pig_predictive_request_aware_last_decision_info{action="size_protect",reason="decode_interference",pressure_source="decode",prefill_class="regular"} 1`,
		"pig_predictive_request_aware_pressure ",
		"pig_predictive_admission_enforced_rejects_total 1",
		"pig_predictive_router_backpressure_active 0",
		"pig_predictive_router_inspect_capacity 0",
	} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("Decode envelope metrics missing %q\n%s", want, rendered.String())
		}
	}

	routerRequest := httptest.NewRequest(http.MethodGet, "/v1/upstream-status", nil)
	routerRequest.Header.Set("Authorization", "Bearer secret")
	routerResponse := httptest.NewRecorder()
	srv.ServeHTTP(routerResponse, routerRequest)
	if routerResponse.Code != http.StatusOK || strings.TrimSpace(routerResponse.Body.String()) != "0" {
		t.Fatalf("Router status=%d body=%q, want green zero", routerResponse.Code, routerResponse.Body.String())
	}

	fitting := serveRequestAwareHTTP(t, srv, "fit")
	if fitting.Code != http.StatusOK || backendCalls != 1 {
		t.Fatalf("fitting response/backend=%d/%d body=%q, want immediate 200/1",
			fitting.Code, backendCalls, fitting.Body.String())
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 {
		t.Fatalf("fitting request leaked reservation: %+v", snapshot)
	}
}

func TestV0127ShadowDecodeEnvelopeRemainsSideEffectFree(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "shadow")
	decision := adapter.Decide(context.Background(), "shadow-decode-envelope", requestAwareAdapterInput(20*1024, 0))
	if decision.Outcome != predictiveAdmissionOutcomeRequestReject ||
		decision.Reason != domainpredictive.ReasonRequestSizeAtPressure ||
		decision.Reservation != nil || manager.Snapshot().Reservations != 0 {
		t.Fatalf("shadow Decode envelope decision/manager=%+v/%+v, want would-reject without reservation", decision, manager.Snapshot())
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReason("decode_interference") ||
		telemetry.RequestAware.PressureSource != runtimepredictive.RequestAwarePressureSource("decode") ||
		telemetry.Attempts.Risks != 1 || telemetry.RouterBackpressure.Active {
		t.Fatalf("shadow Decode envelope telemetry=%+v", telemetry)
	}
}
