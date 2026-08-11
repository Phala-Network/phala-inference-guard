package server

import (
	"context"
	"strings"
	"testing"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestPrefillQoSRequestProtectionIsObservableWithoutRouterLock(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "enforce")
	var decisionLogs []requestAwareDecisionLogEvent
	adapter.onDecision = func(event requestAwareDecisionLogEvent) {
		decisionLogs = append(decisionLogs, event)
	}

	protected := adapter.Decide(context.Background(), "weighted-contended", requestAwareAdapterInput(96*1024, 0))
	if protected.Outcome != predictiveAdmissionOutcomeRequestReject || protected.Reservation != nil ||
		protected.Reason != domainpredictive.ReasonRequestSizeAtPressure {
		t.Fatalf("weighted contended decision=%+v, want request-scoped protection", protected)
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
		telemetry.RequestAware.PressureSource != runtimepredictive.RequestAwarePressurePrefill ||
		telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.InspectCapacity != 0 ||
		telemetry.Attempts.LastRejectScope != predictiveProtectionScopeRequest {
		t.Fatalf("request-scoped Prefill telemetry=%+v", telemetry)
	}
	if len(decisionLogs) != 1 || !decisionLogs[0].Enforced ||
		decisionLogs[0].Scope != predictiveProtectionScopeRequest ||
		decisionLogs[0].HTTPReason != domainpredictive.ReasonRequestSizeAtPressure {
		t.Fatalf("request-scoped Prefill logs=%+v", decisionLogs)
	}
	line := requestAwareDecisionLogLine(decisionLogs[0])
	for _, want := range []string{
		"enforced=true", "action=size_protect", "reason=prefill_busy",
		"http_reason=request_size_at_pressure", "scope=request", "pressure_source=prefill",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("Prefill QoS log missing %q: %s", want, line)
		}
	}

	fitting := adapter.Decide(context.Background(), "regular-after-weighted-reject", requestAwareAdapterInput(8*1024, 0))
	if fitting.Outcome != predictiveAdmissionOutcomeForward || fitting.Reservation == nil {
		t.Fatalf("fitting decision=%+v, want immediate forward", fitting)
	}
	if !fitting.Reservation.Terminate(runtimepredictive.TerminalExpired) || manager.Snapshot().Reservations != 0 {
		t.Fatalf("fitting lifecycle leaked: %+v", manager.Snapshot())
	}
}

func TestShadowPrefillQoSProtectionRemainsSideEffectFree(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "shadow")
	decision := adapter.Decide(context.Background(), "shadow-weighted-contended", requestAwareAdapterInput(96*1024, 0))
	if decision.Outcome != predictiveAdmissionOutcomeRequestReject ||
		decision.Reason != domainpredictive.ReasonRequestSizeAtPressure ||
		decision.Reservation != nil || manager.Snapshot().Reservations != 0 {
		t.Fatalf("shadow Prefill QoS decision/manager=%+v/%+v", decision, manager.Snapshot())
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
		telemetry.RequestAware.PressureSource != runtimepredictive.RequestAwarePressurePrefill ||
		telemetry.Attempts.Risks != 1 || telemetry.RouterBackpressure.Active {
		t.Fatalf("shadow Prefill QoS telemetry=%+v", telemetry)
	}
}

func TestRequestAwareInspectCostIsCanonicalMinimumProductionProbe(t *testing.T) {
	const blockSize = int64(16)
	cost := requestAwareInspectCost("canonical-inspect", blockSize)
	if cost.ManifestID != "canonical-inspect" || cost.InputTokens != 1 ||
		cost.UncachedPrefillUpper != 1 ||
		cost.DecodeHorizonUpper != runtimepredictive.RequestAwareCanonicalDecodeHorizonTokens ||
		cost.ActiveContextTokensUpper != 1+runtimepredictive.RequestAwareCanonicalDecodeHorizonTokens ||
		cost.KV.PhysicalKVUpper != 272 || cost.KV.ActiveKVUpper != 272 ||
		cost.FutureKV.PhysicalKVUpper != 256 || cost.FutureKV.ActiveKVUpper != 256 {
		t.Fatalf("canonical inspect cost=%+v", cost)
	}
}

func TestRequestAwareAdapterDoesNotInferMissingManagerProtectionScope(t *testing.T) {
	result := runtimepredictive.RequestAwareManagerResult{
		Decision: runtimepredictive.RequestAwareDecision{
			Action: runtimepredictive.RequestAwareSizeProtect,
			Reason: runtimepredictive.RequestAwareReasonPrefillBusy,
		},
	}
	decision := requestAwareAdapterProtectedDecision(result)
	if decision.Outcome != predictiveAdmissionOutcomeAvailabilityProtection ||
		decision.Reason != domainpredictive.ReasonPredictorProfileUnknown ||
		decision.Source != runtimepredictive.PredictionSourceUnavailable {
		t.Fatalf("missing Manager scope decision=%+v, want explicit availability failure", decision)
	}
	if scope := requestAwareServerProtectionScope(result.ProtectionScope); scope != predictiveProtectionScopeAvailability {
		t.Fatalf("missing Manager scope normalized to %q, want availability", scope)
	}
	line := requestAwareDecisionLogLine(requestAwareDecisionLogEvent{
		Action: result.Decision.Action,
		Reason: result.Decision.Reason,
	})
	if !strings.Contains(line, "scope=availability") {
		t.Fatalf("missing Manager log scope was hidden: %s", line)
	}
}
