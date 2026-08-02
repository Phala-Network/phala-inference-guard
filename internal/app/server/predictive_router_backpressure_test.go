package server

import (
	"strings"
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/observability/metrics"
	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestPredictiveRouterBackpressureIsFixedAndDoesNotLogRepeatedSignals(t *testing.T) {
	now := time.Unix(80_000, 0)
	hold := 2 * time.Second
	result := runtimepredictive.CountAdmissionResult{
		Decision: domainpredictive.Decision{Reason: domainpredictive.ReasonExistingTPSAtRisk},
		Prediction: runtimepredictive.SchedulerPrediction{
			Source:  runtimepredictive.PredictionSourceCalibrated,
			Samples: 7,
			Features: runtimepredictive.SchedulerFeatures{
				ExistingDecodeSequences: 1,
				ExistingActiveKVUpper:   512,
			},
		},
	}
	var state predictiveRouterBackpressureState
	first := state.Observe(now, hold, result, predictiveRouterBackpressurePolicy{})
	if first == nil {
		t.Fatal("first load-dependent risk did not activate Router backpressure")
	}
	changedReason := result
	changedReason.Decision.Reason = domainpredictive.ReasonNewTPSAtRisk
	if second := state.Observe(now.Add(time.Second), hold, changedReason, predictiveRouterBackpressurePolicy{}); second != nil {
		t.Fatal("in-episode protection signal emitted another activation event")
	}
	snapshot := state.Snapshot(now.Add(1500 * time.Millisecond))
	if !snapshot.Active || snapshot.Activations != 1 || snapshot.Extensions != 1 {
		t.Fatalf("active snapshot = %+v, want one activation and one extension", snapshot)
	}
	if snapshot.Reason != domainpredictive.ReasonNewTPSAtRisk {
		t.Fatalf("in-episode diagnostics did not retain latest reason: %+v", snapshot)
	}
	if !snapshot.ActivatedAt.Equal(now) {
		t.Fatalf("in-episode signal changed activation time: got=%s want=%s", snapshot.ActivatedAt, now)
	}
	if !snapshot.Until.Equal(now.Add(hold)) {
		t.Fatalf("extension changed fixed expiry: got=%s want=%s", snapshot.Until, now.Add(hold))
	}
	if snapshot := state.Snapshot(now.Add(hold + time.Millisecond)); snapshot.Active || snapshot.Reason != "" || snapshot.Source != "" || snapshot.Samples != 0 {
		t.Fatalf("expired backpressure retained active reason/source/sample state: %+v", snapshot)
	}
	third := state.Observe(now.Add(hold+time.Millisecond), hold, result, predictiveRouterBackpressurePolicy{})
	if third == nil {
		t.Fatal("first rejected probe after expiry did not start a new bounded episode")
	}
	if snapshot := state.Snapshot(now.Add(hold + 2*time.Millisecond)); snapshot.Activations != 2 || snapshot.Extensions != 1 {
		t.Fatalf("new post-expiry episode telemetry = %+v, want two activations and one extension", snapshot)
	}
	line := predictiveRouterBackpressureLogLine(*first)
	for _, want := range []string{
		"event=activated",
		"reason=existing_tps_at_risk",
		"source=calibrated",
		"samples=7",
		"hold=2s",
		"activated_at=1970-01-01T22:13:20Z",
		"until=1970-01-01T22:13:22Z",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("activation log missing %q: %s", want, line)
		}
	}
	for _, forbidden := range []string{"prompt=", "body=", "bearer=", "token="} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Fatalf("activation log contains request or secret field %q: %s", forbidden, line)
		}
	}
}

func TestPredictiveRouterBackpressureExcludesIdleAndRequestSpecificRejects(t *testing.T) {
	now := time.Unix(81_000, 0)
	for _, test := range []struct {
		name     string
		reason   domainpredictive.Reason
		features runtimepredictive.SchedulerFeatures
	}{
		{name: "idle_new_tps", reason: domainpredictive.ReasonNewTPSAtRisk},
		{
			name:   "idle_stale_kv",
			reason: domainpredictive.ReasonExistingTPSAtRisk,
			features: runtimepredictive.SchedulerFeatures{
				ExistingPhysicalKVUpper: 10_000,
				ExistingActiveKVUpper:   10_000,
			},
		},
		{name: "unknown", reason: domainpredictive.ReasonRequestSizeUnknown, features: runtimepredictive.SchedulerFeatures{ExistingDecodeSequences: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var state predictiveRouterBackpressureState
			result := runtimepredictive.CountAdmissionResult{
				Decision:   domainpredictive.Decision{Reason: test.reason},
				Prediction: runtimepredictive.SchedulerPrediction{Features: test.features},
			}
			if event := state.Observe(now, time.Second, result, predictiveRouterBackpressurePolicy{}); event != nil {
				t.Fatalf("request-specific risk activated Router backpressure: %+v", event)
			}
			if snapshot := state.Snapshot(now); snapshot.Active {
				t.Fatalf("request-specific risk left active state: %+v", snapshot)
			}
		})
	}
}

func TestPredictiveRouterBackpressureDistinguishesLoadDependentKVFromOversizedRequest(t *testing.T) {
	now := time.Unix(81_500, 0)
	policy := predictiveRouterBackpressurePolicy{PhysicalKVHard: 1_000, ActiveKVHard: 800}
	for _, test := range []struct {
		name      string
		reason    domainpredictive.Reason
		physical  int64
		active    int64
		wantEvent bool
	}{
		{name: "physical_load_dependent", reason: domainpredictive.ReasonKVOverBudget, physical: 900, active: 700, wantEvent: true},
		{name: "physical_request_oversized", reason: domainpredictive.ReasonKVOverBudget, physical: 1_001, active: 700},
		{name: "active_load_dependent", reason: domainpredictive.ReasonActiveKVOverBudget, physical: 900, active: 800, wantEvent: true},
		{name: "active_request_oversized", reason: domainpredictive.ReasonActiveKVOverBudget, physical: 900, active: 801},
	} {
		t.Run(test.name, func(t *testing.T) {
			var state predictiveRouterBackpressureState
			result := runtimepredictive.CountAdmissionResult{
				Decision: domainpredictive.Decision{Reason: test.reason},
				Prediction: runtimepredictive.SchedulerPrediction{Features: runtimepredictive.SchedulerFeatures{
					ExistingDecodeSequences: 1,
				}},
				Cost: runtimepredictive.CountRequestCost{
					ManifestID: "model-agnostic-test", BackendEpoch: "epoch-1",
					PhysicalKVUpper: test.physical, ActiveKVUpper: test.active,
				},
			}
			got := state.Observe(now, 2*time.Second, result, policy)
			if (got != nil) != test.wantEvent {
				t.Fatalf("KV backpressure event = %+v, wantEvent=%t", got, test.wantEvent)
			}
		})
	}
}

func TestPredictiveRouterCapacityClampsOnlyEnforceWithActiveLoad(t *testing.T) {
	snapshot := runtimedynamic.Snapshot{Running: 1, GlobalLimit: 50, QOSLimit: 50}
	got := predictiveRouterCapacity("enforce", true, 2, snapshot)
	if !got.BackpressureApplied || got.EffectiveRunning != 2 || got.EffectiveGlobalLimit != 2 {
		t.Fatalf("enforce capacity = %+v, want full at two effective active requests", got)
	}
	if got.RawRunning != 1 || got.RawGlobalLimit != 50 {
		t.Fatalf("raw capacity was lost: %+v", got)
	}
	if got.PredictiveRunning != 2 {
		t.Fatalf("predictive load was not retained in the projection: %+v", got)
	}

	laggingDynamic := predictiveRouterCapacity("enforce", true, 3, runtimedynamic.Snapshot{GlobalLimit: 50, QOSLimit: 50})
	if !laggingDynamic.BackpressureApplied || laggingDynamic.RawRunning != 0 || laggingDynamic.PredictiveRunning != 3 || laggingDynamic.EffectiveRunning != 3 || laggingDynamic.EffectiveGlobalLimit != 3 {
		t.Fatalf("predictive load did not close the Router visibility gap while dynamic metrics lagged: %+v", laggingDynamic)
	}

	idle := predictiveRouterCapacity("enforce", true, 0, runtimedynamic.Snapshot{GlobalLimit: 50, QOSLimit: 50})
	if idle.BackpressureApplied || idle.EffectiveGlobalLimit != 50 {
		t.Fatalf("idle backpressure self-locked capacity: %+v", idle)
	}
	shadow := predictiveRouterCapacity("shadow", true, 2, snapshot)
	if shadow.BackpressureApplied || shadow.EffectiveRunning != 1 || shadow.EffectiveGlobalLimit != 50 {
		t.Fatalf("shadow mode altered Router capacity: %+v", shadow)
	}
	expired := predictiveRouterCapacity("enforce", false, 2, snapshot)
	if expired.BackpressureApplied || expired.PredictiveRunning != 0 || expired.EffectiveRunning != 1 || expired.EffectiveGlobalLimit != 50 {
		t.Fatalf("expired hold retained Router clamp: %+v", expired)
	}
	unavailable := predictiveRouterCapacity("enforce", true, 2, runtimedynamic.Snapshot{GlobalLimit: 0, QOSLimit: 0})
	if !unavailable.BackpressureApplied || unavailable.EffectiveRunning != 2 || unavailable.EffectiveGlobalLimit != 2 || unavailable.RawGlobalLimit != 0 {
		t.Fatalf("zero raw capacity did not project Router-blocking fullness while preserving raw state: %+v", unavailable)
	}
}

func TestPredictiveRouterCapacityEventIsLoggedOncePerAppliedEpisode(t *testing.T) {
	var state predictiveRouterCapacityLogState
	input := metrics.PredictiveAdmissionInput{
		RouterBackpressure: metrics.PredictiveRouterBackpressureInput{
			Active:      true,
			Applied:     true,
			Reason:      "existing_tps_at_risk",
			Source:      "calibrated",
			Samples:     7,
			ActivatedAt: time.Unix(83_000, 0),
			Until:       time.Unix(83_002, 0),
			Activations: 1,
		},
	}
	capacity := predictiveRouterCapacityProjection{
		BackpressureActive:   true,
		BackpressureApplied:  true,
		PredictiveRunning:    1,
		RawRunning:           0,
		EffectiveRunning:     1,
		RawGlobalLimit:       50,
		EffectiveGlobalLimit: 1,
	}
	event := state.Claim(input, capacity)
	if event == nil {
		t.Fatal("first applied projection did not claim a capacity log event")
	}
	if duplicate := state.Claim(input, capacity); duplicate != nil {
		t.Fatalf("same episode claimed a duplicate capacity log event: %+v", duplicate)
	}
	line := predictiveRouterCapacityLogLine(*event)
	for _, want := range []string{
		"event=router_capacity_applied",
		"activation=1",
		"reason=existing_tps_at_risk",
		"source=calibrated",
		"samples=7",
		"predictive_running=1",
		"raw_running=0",
		"effective_running=1",
		"raw_global_limit=50",
		"effective_global_limit=1",
		"activated_at=1970-01-01T23:03:20Z",
		"until=1970-01-01T23:03:22Z",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("capacity log missing %q: %s", want, line)
		}
	}
	for _, forbidden := range []string{"prompt=", "body=", "bearer=", "token="} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Fatalf("capacity log contains request or secret field %q: %s", forbidden, line)
		}
	}

	input.RouterBackpressure.Activations = 2
	if next := state.Claim(input, capacity); next == nil || next.Activation != 2 {
		t.Fatalf("next applied episode did not claim a new capacity log event: %+v", next)
	}
	idle := capacity
	idle.BackpressureApplied = false
	input.RouterBackpressure.Activations = 3
	if event := state.Claim(input, idle); event != nil {
		t.Fatalf("unapplied episode claimed a capacity log event: %+v", event)
	}
}

func TestPredictiveRouterBackpressureHoldIsBounded(t *testing.T) {
	if got := predictiveRouterBackpressureHold(time.Millisecond); got != minimumPredictiveRouterBackpressureHold {
		t.Fatalf("short poll hold=%s want=%s", got, minimumPredictiveRouterBackpressureHold)
	}
	if got := predictiveRouterBackpressureHold(time.Hour); got != maximumPredictiveRouterBackpressureHold {
		t.Fatalf("long poll hold=%s want=%s", got, maximumPredictiveRouterBackpressureHold)
	}
	var state predictiveRouterBackpressureState
	result := runtimepredictive.CountAdmissionResult{
		Decision: domainpredictive.Decision{Reason: domainpredictive.ReasonExistingTPSAtRisk},
		Prediction: runtimepredictive.SchedulerPrediction{Features: runtimepredictive.SchedulerFeatures{
			ExistingDecodeSequences: 1,
		}},
	}
	event := state.Observe(time.Unix(82_000, 0), time.Hour, result, predictiveRouterBackpressurePolicy{})
	if event == nil || event.Hold != maximumPredictiveRouterBackpressureHold {
		t.Fatalf("direct state hold was not bounded: %+v", event)
	}
}

func TestFormatPredictiveStatusExposesProtectionAndRouterCapacity(t *testing.T) {
	line := formatPredictiveStatus(metrics.PredictiveAdmissionInput{
		Mode: "enforce", Attempts: 12, Fits: 4, Risks: 8, EnforcedRejects: 8,
		LastReason: "existing_tps_at_risk", LastSource: "calibrated", LastSamples: 6,
		Reservations: 1, VirtualDecodeSequences: 3, DeferredOutcomes: metrics.PredictiveDeferredOutcomeInput{Active: 2},
		RouterBackpressure: metrics.PredictiveRouterBackpressureInput{
			Active: true, Applied: true, Reason: "existing_tps_at_risk",
			RawRunning: 1, EffectiveRunning: 1, RawGlobalLimit: 50, EffectiveGlobalLimit: 1,
		},
	})
	for _, want := range []string{
		"predictive={mode=enforce", "attempts=12", "risk=8", "last=existing_tps_at_risk/calibrated/6",
		"reservations=1", "virtual_decode=3", "deferred=2", "router_bp=1/1/existing_tps_at_risk", "effective=1/1", "raw=1/50",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("predictive status missing %q: %s", want, line)
		}
	}
}
