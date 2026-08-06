package server

import (
	"bytes"
	"strings"
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/observability/metrics"
	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestPredictiveRouterBackpressureRenewsWithoutChangingActivationAndBoundsLogs(t *testing.T) {
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
	second := state.Observe(now.Add(time.Second), hold, changedReason, predictiveRouterBackpressurePolicy{})
	if second == nil || second.Kind != predictiveRouterBackpressureRenewed || second.Activation != first.Activation {
		t.Fatalf("first in-episode protection signal did not emit a renewal for the original activation: %+v", second)
	}
	if suppressed := state.Observe(now.Add(1500*time.Millisecond), hold, changedReason, predictiveRouterBackpressurePolicy{}); suppressed != nil {
		t.Fatalf("same-window renewal log was not bounded: %+v", suppressed)
	}
	bounded := state.Observe(now.Add(3100*time.Millisecond), hold, changedReason, predictiveRouterBackpressurePolicy{})
	if bounded == nil || bounded.Kind != predictiveRouterBackpressureRenewed || bounded.Suppressed != 1 {
		t.Fatalf("next bounded renewal did not expose one suppressed event: %+v", bounded)
	}
	snapshot := state.Snapshot(now.Add(3200 * time.Millisecond))
	if !snapshot.Active || snapshot.Activations != 1 || snapshot.Extensions != 3 || snapshot.RenewalLogs != 2 || snapshot.RenewalsSuppressed != 1 {
		t.Fatalf("active snapshot = %+v, want one activation, three extensions, and bounded renewal telemetry", snapshot)
	}
	if snapshot.Reason != domainpredictive.ReasonExistingTPSAtRisk ||
		snapshot.Source != runtimepredictive.PredictionSourceCalibrated || snapshot.Samples != 7 {
		t.Fatalf("in-episode signal changed the immutable activation identity: %+v", snapshot)
	}
	if !snapshot.ActivatedAt.Equal(now) {
		t.Fatalf("in-episode signal changed activation time: got=%s want=%s", snapshot.ActivatedAt, now)
	}
	wantUntil := now.Add(3100*time.Millisecond + hold)
	if !snapshot.Until.Equal(wantUntil) || !snapshot.LatestRejectAt.Equal(now.Add(3100*time.Millisecond)) {
		t.Fatalf("renewal deadline/latest reject = %s/%s, want %s/%s", snapshot.Until, snapshot.LatestRejectAt, wantUntil, now.Add(3100*time.Millisecond))
	}
	if snapshot := state.Snapshot(wantUntil.Add(time.Millisecond)); snapshot.Active || snapshot.Reason != "" || snapshot.Source != "" || snapshot.Samples != 0 {
		t.Fatalf("expired backpressure retained active reason/source/sample state: %+v", snapshot)
	}
	third := state.Observe(wantUntil.Add(time.Millisecond), hold, result, predictiveRouterBackpressurePolicy{})
	if third == nil {
		t.Fatal("first rejected probe after expiry did not start a new bounded episode")
	}
	if snapshot := state.Snapshot(wantUntil.Add(2 * time.Millisecond)); snapshot.Activations != 2 || snapshot.Extensions != 3 {
		t.Fatalf("new post-expiry episode telemetry = %+v, want two activations and three cumulative extensions", snapshot)
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
	renewalLine := predictiveRouterBackpressureLogLine(*bounded)
	for _, want := range []string{
		"event=renewed",
		"activation=1",
		"reason=new_tps_at_risk",
		"extensions=3",
		"suppressed=1",
		"latest_reject_at=1970-01-01T22:13:23.1Z",
		"until=1970-01-01T22:13:25.1Z",
	} {
		if !strings.Contains(renewalLine, want) {
			t.Fatalf("renewal log missing %q: %s", want, renewalLine)
		}
	}
	for _, forbidden := range []string{"model=", "user=", "request_id=", "prompt=", "body=", "bearer=", "token="} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Fatalf("activation log contains request or secret field %q: %s", forbidden, line)
		}
		if strings.Contains(strings.ToLower(renewalLine), forbidden) {
			t.Fatalf("renewal log contains request or secret field %q: %s", forbidden, renewalLine)
		}
	}
}

func TestPredictiveRouterBackpressureRenewsFromLatestLoadReject(t *testing.T) {
	now := time.Unix(90_000, 0)
	hold := 2 * time.Second
	result := runtimepredictive.CountAdmissionResult{
		Decision: domainpredictive.Decision{Reason: domainpredictive.ReasonExistingTPSAtRisk},
		Prediction: runtimepredictive.SchedulerPrediction{
			Source: runtimepredictive.PredictionSourceStatic,
			Features: runtimepredictive.SchedulerFeatures{
				ExistingDecodeSequences: 1,
				ExistingActiveKVUpper:   128,
			},
		},
	}

	var state predictiveRouterBackpressureState
	first := state.Observe(now, hold, result, predictiveRouterBackpressurePolicy{})
	if first == nil {
		t.Fatal("first load reject did not activate Router backpressure")
	}
	latestReject := now.Add(1500 * time.Millisecond)
	_ = state.Observe(latestReject, hold, result, predictiveRouterBackpressurePolicy{})

	betweenDeadlines := now.Add(2500 * time.Millisecond)
	snapshot := state.Snapshot(betweenDeadlines)
	wantUntil := latestReject.Add(hold)
	if !snapshot.Active || snapshot.Activation != first.Activation {
		t.Fatalf("renewed snapshot at %s = %+v, want original activation still active", betweenDeadlines, snapshot)
	}
	if !snapshot.Until.Equal(wantUntil) {
		t.Fatalf("renewed until = %s, want latest reject deadline %s", snapshot.Until, wantUntil)
	}
	if snapshot.Activations != 1 || snapshot.Extensions != 1 {
		t.Fatalf("renewed activation/extension counts = %d/%d, want 1/1", snapshot.Activations, snapshot.Extensions)
	}
	if state.Snapshot(wantUntil.Add(time.Nanosecond)).Active {
		t.Fatal("load backpressure remained active after the renewed deadline")
	}
}

func TestPredictiveRouterBackpressureCoversRouterCadenceJitterAndRecovers(t *testing.T) {
	started := time.Unix(95_000, 0)
	hold := 5 * time.Second
	rejectInterval := time.Second
	lastReject := started.Add(20 * time.Second)
	result := runtimepredictive.CountAdmissionResult{
		Decision: domainpredictive.Decision{Reason: domainpredictive.ReasonExistingTPSAtRisk},
		Prediction: runtimepredictive.SchedulerPrediction{
			Source: runtimepredictive.PredictionSourceCalibrated,
			Features: runtimepredictive.SchedulerFeatures{
				ExistingDecodeSequences: 1,
				ExistingActiveKVUpper:   256,
			},
		},
	}
	jitter := []time.Duration{-400 * time.Millisecond, 350 * time.Millisecond, -150 * time.Millisecond, 200 * time.Millisecond}
	for _, phase := range []time.Duration{0, 250 * time.Millisecond, 1900 * time.Millisecond} {
		t.Run(phase.String(), func(t *testing.T) {
			var state predictiveRouterBackpressureState
			nextReject := started
			processedRejects := 0
			for scrapeIndex := 0; ; scrapeIndex++ {
				scrapeAt := started.Add(phase + time.Duration(scrapeIndex)*2*time.Second + jitter[scrapeIndex%len(jitter)])
				if !scrapeAt.Before(lastReject.Add(hold)) {
					break
				}
				if scrapeAt.Before(started) {
					continue
				}
				for !nextReject.After(scrapeAt) && !nextReject.After(lastReject) {
					state.Observe(nextReject, hold, result, predictiveRouterBackpressurePolicy{})
					processedRejects++
					nextReject = nextReject.Add(rejectInterval)
				}
				if processedRejects == 0 {
					continue
				}
				snapshot := state.Snapshot(scrapeAt)
				if !snapshot.Active || snapshot.Activation != 1 || snapshot.Activations != 1 {
					t.Fatalf("scrape at %s saw publication gap after %d rejects: %+v", scrapeAt.Sub(started), processedRejects, snapshot)
				}
				capacity := predictiveRouterCapacity(
					"enforce",
					snapshot,
					1,
					runtimedynamic.Snapshot{GlobalLimit: 50, QOSLimit: 50},
				)
				if !capacity.BackpressureApplied || capacity.EffectiveRunning != 1 || capacity.EffectiveGlobalLimit != 1 {
					t.Fatalf("scrape at %s saw unprotected Router capacity: %+v", scrapeAt.Sub(started), capacity)
				}
			}
			for !nextReject.After(lastReject) {
				state.Observe(nextReject, hold, result, predictiveRouterBackpressurePolicy{})
				processedRejects++
				nextReject = nextReject.Add(rejectInterval)
			}
			if processedRejects != 21 {
				t.Fatalf("processed rejects = %d, want 21", processedRejects)
			}
			if snapshot := state.Snapshot(lastReject.Add(hold - time.Nanosecond)); !snapshot.Active || snapshot.Activations != 1 || snapshot.Extensions != 20 {
				t.Fatalf("publication did not cover the final pre-expiry instant: %+v", snapshot)
			}
			if snapshot := state.Snapshot(lastReject.Add(hold + time.Nanosecond)); snapshot.Active || snapshot.Activation != 0 {
				t.Fatalf("finite rejection stream did not recover after last_reject+hold: %+v", snapshot)
			}
		})
	}
}

func TestLoadAndAvailabilityProtectionOverlapKeepsCoherentActivationIdentity(t *testing.T) {
	now := time.Unix(80_500, 0)
	hold := 2 * time.Second
	result := runtimepredictive.CountAdmissionResult{
		Decision: domainpredictive.Decision{Reason: domainpredictive.ReasonExistingTPSAtRisk},
		Prediction: runtimepredictive.SchedulerPrediction{
			Source: runtimepredictive.PredictionSourceCalibrated,
			Features: runtimepredictive.SchedulerFeatures{
				ExistingDecodeSequences: 1,
				ExistingActiveKVUpper:   512,
			},
		},
	}
	var state predictiveRouterBackpressureState
	load := state.Observe(now, hold, result, predictiveRouterBackpressurePolicy{})
	if load == nil || load.Activation != 1 || load.Scope != predictiveProtectionScopeLoad {
		t.Fatalf("initial load activation = %+v", load)
	}
	availability := state.SetAvailability(now.Add(500*time.Millisecond), true)
	if availability == nil || availability.Activation != 2 || availability.Scope != predictiveProtectionScopeAvailability {
		t.Fatalf("availability overlap activation = %+v", availability)
	}
	if snapshot := state.Snapshot(now.Add(750 * time.Millisecond)); !snapshot.Active || snapshot.Activation != 2 || snapshot.Scope != predictiveProtectionScopeAvailability || snapshot.MinimumRunning != 1 {
		t.Fatalf("availability overlap snapshot = %+v", snapshot)
	}
	restoredLoad := state.SetAvailability(now.Add(time.Second), false)
	if restoredLoad == nil || restoredLoad.Activation != 3 || restoredLoad.Scope != predictiveProtectionScopeLoad || restoredLoad.Reason != domainpredictive.ReasonExistingTPSAtRisk {
		t.Fatalf("restored load activation = %+v", restoredLoad)
	}
	if snapshot := state.Snapshot(now.Add(1500 * time.Millisecond)); !snapshot.Active || snapshot.Activation != 3 || snapshot.Scope != predictiveProtectionScopeLoad || snapshot.Reason != domainpredictive.ReasonExistingTPSAtRisk {
		t.Fatalf("restored load snapshot = %+v", snapshot)
	}
	if snapshot := state.Snapshot(now.Add(hold + time.Millisecond)); snapshot.Active || snapshot.Activation != 0 || snapshot.Scope != "" {
		t.Fatalf("overlap state did not expire with the original fixed load window: %+v", snapshot)
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

func TestEveryLoadDependentQoSReasonActivatesRouterBackpressure(t *testing.T) {
	now := time.Unix(81_750, 0)
	for _, reason := range []domainpredictive.Reason{
		domainpredictive.ReasonExistingTPSAtRisk,
		domainpredictive.ReasonNewTPSAtRisk,
		domainpredictive.ReasonTPOTAtRisk,
		domainpredictive.ReasonThroughputFrontier,
		domainpredictive.ReasonWorkspaceAtRisk,
		domainpredictive.ReasonPreemptionAtRisk,
	} {
		t.Run(string(reason), func(t *testing.T) {
			var state predictiveRouterBackpressureState
			result := runtimepredictive.CountAdmissionResult{
				Decision: domainpredictive.Decision{Reason: reason},
				Prediction: runtimepredictive.SchedulerPrediction{
					Source:   runtimepredictive.PredictionSourceStatic,
					Features: runtimepredictive.SchedulerFeatures{ExistingDecodeSequences: 1},
				},
			}
			event := state.Observe(now, 2*time.Second, result, predictiveRouterBackpressurePolicy{})
			if event == nil || event.Scope != predictiveProtectionScopeLoad || event.Reason != reason {
				t.Fatalf("load-dependent reason did not publish a protection episode: %+v", event)
			}
		})
	}
}

func TestRequestRejectLogIsBoundedWithoutLosingSuppressionCount(t *testing.T) {
	now := time.Unix(81_900, 0)
	result := runtimepredictive.CountAdmissionResult{
		Decision:   domainpredictive.Decision{Reason: domainpredictive.ReasonNewTPSAtRisk},
		Prediction: runtimepredictive.SchedulerPrediction{Source: runtimepredictive.PredictionSourceStatic},
	}
	var state predictiveRequestRejectLogState
	first := state.Observe(now, 2*time.Second, result)
	if first == nil {
		t.Fatal("first request-scoped reject was not logged")
	}
	if duplicate := state.Observe(now.Add(time.Second), 2*time.Second, result); duplicate != nil {
		t.Fatalf("same-window request reject was not bounded: %+v", duplicate)
	}
	next := state.Observe(now.Add(3*time.Second), 2*time.Second, result)
	if next == nil || next.Suppressed != 1 {
		t.Fatalf("next bounded log did not report suppressed count: %+v", next)
	}
	line := predictiveRequestRejectLogLine(*next)
	for _, want := range []string{"event=request_rejected", "scope=request", "reason=new_tps_at_risk", "suppressed=1"} {
		if !strings.Contains(line, want) {
			t.Fatalf("request reject log missing %q: %s", want, line)
		}
	}
	for _, forbidden := range []string{"model=", "user=", "request_id=", "prompt=", "body=", "bearer=", "token="} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Fatalf("request reject log contains request or secret field %q: %s", forbidden, line)
		}
	}
}

func TestFailureRejectLogRateLimitsEachFixedPhaseIndependently(t *testing.T) {
	now := time.Unix(81_950, 0)
	result := runtimepredictive.CountAdmissionResult{
		Decision:   domainpredictive.Decision{Reason: domainpredictive.ReasonPredictorProfileUnknown},
		Prediction: runtimepredictive.SchedulerPrediction{Source: runtimepredictive.PredictionSourceUnavailable},
	}
	var state predictiveRequestRejectLogState
	decisionPanic := state.ObservePhase(now, 2*time.Second, "decision_panic", result)
	forwardCommit := state.ObservePhase(now.Add(time.Millisecond), 2*time.Second, "forward_commit", result)
	if decisionPanic == nil || decisionPanic.Phase != "decision_panic" || forwardCommit == nil || forwardCommit.Phase != "forward_commit" {
		t.Fatalf("distinct failure phases suppressed one another: decision=%+v forward=%+v", decisionPanic, forwardCommit)
	}
	if duplicate := state.ObservePhase(now.Add(time.Second), 2*time.Second, "forward_commit", result); duplicate != nil {
		t.Fatalf("same failure phase was not bounded: %+v", duplicate)
	}
	if next := state.ObservePhase(now.Add(3*time.Second), 2*time.Second, "forward_commit", result); next == nil || next.Suppressed != 1 {
		t.Fatalf("phase-local suppression count was lost: %+v", next)
	}
}

func TestPredictiveRouterCapacityClampsOnlyEnforceWithActiveLoad(t *testing.T) {
	snapshot := runtimedynamic.Snapshot{Running: 1, GlobalLimit: 50, QOSLimit: 50}
	active := predictiveRouterBackpressureSnapshot{Active: true, Activation: 1, Scope: predictiveProtectionScopeLoad}
	got := predictiveRouterCapacity("enforce", active, 2, snapshot)
	if !got.BackpressureApplied || got.EffectiveRunning != 2 || got.EffectiveGlobalLimit != 2 {
		t.Fatalf("enforce capacity = %+v, want full at two effective active requests", got)
	}
	if got.RawRunning != 1 || got.RawGlobalLimit != 50 {
		t.Fatalf("raw capacity was lost: %+v", got)
	}
	if got.PredictiveRunning != 2 {
		t.Fatalf("predictive load was not retained in the projection: %+v", got)
	}

	laggingDynamic := predictiveRouterCapacity("enforce", active, 3, runtimedynamic.Snapshot{GlobalLimit: 50, QOSLimit: 50})
	if !laggingDynamic.BackpressureApplied || laggingDynamic.RawRunning != 0 || laggingDynamic.PredictiveRunning != 3 || laggingDynamic.EffectiveRunning != 3 || laggingDynamic.EffectiveGlobalLimit != 3 {
		t.Fatalf("predictive load did not close the Router visibility gap while dynamic metrics lagged: %+v", laggingDynamic)
	}

	active.MinimumRunning = 1
	idleDuringLease := predictiveRouterCapacity("enforce", active, 0, runtimedynamic.Snapshot{GlobalLimit: 50, QOSLimit: 50})
	if !idleDuringLease.BackpressureApplied || idleDuringLease.PredictiveRunning != 1 || idleDuringLease.EffectiveRunning != 1 || idleDuringLease.EffectiveGlobalLimit != 1 {
		t.Fatalf("active load lease did not preserve Router-visible fullness through an idle scrape: %+v", idleDuringLease)
	}
	shadow := predictiveRouterCapacity("shadow", active, 2, snapshot)
	if shadow.BackpressureApplied || shadow.EffectiveRunning != 1 || shadow.EffectiveGlobalLimit != 50 {
		t.Fatalf("shadow mode altered Router capacity: %+v", shadow)
	}
	expired := predictiveRouterCapacity("enforce", predictiveRouterBackpressureSnapshot{}, 2, snapshot)
	if expired.BackpressureApplied || expired.PredictiveRunning != 0 || expired.EffectiveRunning != 1 || expired.EffectiveGlobalLimit != 0 {
		t.Fatalf("expired hold retained Router clamp: %+v", expired)
	}
	unavailable := predictiveRouterCapacity("enforce", active, 2, runtimedynamic.Snapshot{GlobalLimit: 0, QOSLimit: 0})
	if !unavailable.BackpressureApplied || unavailable.EffectiveRunning != 2 || unavailable.EffectiveGlobalLimit != 2 || unavailable.RawGlobalLimit != 0 {
		t.Fatalf("zero raw capacity did not project Router-blocking fullness while preserving raw state: %+v", unavailable)
	}
	availability := predictiveRouterCapacity("enforce", predictiveRouterBackpressureSnapshot{
		Active: true, Activation: 2, Scope: predictiveProtectionScopeAvailability, MinimumRunning: 1,
	}, 0, runtimedynamic.Snapshot{GlobalLimit: 50, QOSLimit: 50})
	if !availability.BackpressureApplied || availability.PredictiveRunning != 1 || availability.EffectiveRunning != 1 || availability.EffectiveGlobalLimit != 1 {
		t.Fatalf("availability protection did not publish a Router-blocking sentinel: %+v", availability)
	}
}

func TestPredictiveRouterCapacityKeepsOneInspectSlotForSelectiveProtection(t *testing.T) {
	snapshot := runtimedynamic.Snapshot{Running: 1, GlobalLimit: 50, QOSLimit: 50}
	selective := predictiveRouterBackpressureSnapshot{
		Active: true, Activation: 1, Scope: predictiveProtectionScopeLoad, InspectCapacity: 1,
	}
	got := predictiveRouterCapacity("enforce", selective, 2, snapshot)
	if !got.BackpressureApplied || got.EffectiveRunning != 2 || got.EffectiveGlobalLimit != 3 || got.InspectCapacity != 1 {
		t.Fatalf("selective Router capacity=%+v, want two active plus one inspect slot", got)
	}
	hard := selective
	hard.InspectCapacity = 0
	got = predictiveRouterCapacity("enforce", hard, 2, snapshot)
	if !got.BackpressureApplied || got.EffectiveGlobalLimit != 2 || got.InspectCapacity != 0 {
		t.Fatalf("hard Router capacity=%+v, want full at two active requests", got)
	}
}

func TestPredictiveRouterCapacityEnforceUsesOnlyRequestAwareEffectiveAuthority(t *testing.T) {
	snapshot := runtimedynamic.Snapshot{Running: 2, Waiting: 7, GlobalLimit: 1, QOSLimit: 1}
	selective := predictiveRouterBackpressureSnapshot{
		Active: true, Activation: 1, Scope: predictiveProtectionScopeLoad, InspectCapacity: 1,
	}
	hard := selective
	hard.InspectCapacity = 0

	tests := []struct {
		name              string
		mode              string
		backpressure      predictiveRouterBackpressureSnapshot
		predictiveRunning int
		wantApplied       bool
		wantRunning       int
		wantLimit         int
		wantInspect       int
	}{
		{
			name: "enforce open neutralizes legacy dynamic limit",
			mode: "enforce", predictiveRunning: 9,
			wantRunning: 2, wantLimit: 0,
		},
		{
			name: "enforce selective publishes exactly one inspect slot",
			mode: "enforce", backpressure: selective, predictiveRunning: 3,
			wantApplied: true, wantRunning: 3, wantLimit: 4, wantInspect: 1,
		},
		{
			name: "enforce hard publishes no inspect slot",
			mode: "enforce", backpressure: hard, predictiveRunning: 3,
			wantApplied: true, wantRunning: 3, wantLimit: 3,
		},
		{
			name: "shadow preserves legacy dynamic projection",
			mode: "shadow", backpressure: selective, predictiveRunning: 3,
			wantRunning: 2, wantLimit: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := predictiveRouterCapacity(test.mode, test.backpressure, test.predictiveRunning, snapshot)
			if got.BackpressureApplied != test.wantApplied || got.EffectiveRunning != test.wantRunning ||
				got.EffectiveGlobalLimit != test.wantLimit || got.InspectCapacity != test.wantInspect {
				t.Fatalf("capacity=%+v, want applied=%t running=%d limit=%d inspect=%d", got, test.wantApplied, test.wantRunning, test.wantLimit, test.wantInspect)
			}
			if got.RawRunning != snapshot.Running || got.RawGlobalLimit != snapshot.GlobalLimit {
				t.Fatalf("raw dynamic observability was not preserved: %+v", got)
			}
		})
	}
}

func TestPredictiveRouterMetricsSeparateRawWaitingFromEnforceProjection(t *testing.T) {
	snapshot := runtimedynamic.Snapshot{Running: 2, Waiting: 7, GlobalLimit: 1, QOSLimit: 1}
	write := func(mode string, backpressure predictiveRouterBackpressureSnapshot, predictiveRunning int) string {
		t.Helper()
		capacity := predictiveRouterCapacity(mode, backpressure, predictiveRunning, snapshot)
		var out bytes.Buffer
		metrics.WriteDynamic(&out, snapshot, metrics.DynamicConfig{}, nil, predictiveRouterCapacityMetrics(capacity))
		return out.String()
	}

	selective := predictiveRouterBackpressureSnapshot{
		Active: true, Activation: 1, Scope: predictiveProtectionScopeLoad, InspectCapacity: 1,
	}
	for _, test := range []struct {
		name              string
		mode              string
		backpressure      predictiveRouterBackpressureSnapshot
		predictiveRunning int
		want              []string
	}{
		{
			name: "enforce open",
			mode: "enforce",
			want: []string{
				"pig_dynamic_observed_waiting_raw 7",
				"pig_dynamic_observed_waiting 0",
				"pig_dynamic_global_limit_raw 1",
				"pig_dynamic_global_limit 0",
			},
		},
		{
			name: "enforce selective",
			mode: "enforce", backpressure: selective, predictiveRunning: 3,
			want: []string{
				"pig_dynamic_observed_waiting_raw 7",
				"pig_dynamic_observed_waiting 0",
				"pig_dynamic_observed_running 3",
				"pig_dynamic_global_limit 4",
			},
		},
		{
			name: "shadow remains legacy compatible",
			mode: "shadow", backpressure: selective, predictiveRunning: 3,
			want: []string{
				"pig_dynamic_observed_waiting_raw 7",
				"pig_dynamic_observed_waiting 7",
				"pig_dynamic_observed_running 2",
				"pig_dynamic_global_limit 1",
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := write(test.mode, test.backpressure, test.predictiveRunning)
			for _, want := range test.want {
				if !strings.Contains(got, want) {
					t.Fatalf("Router metrics missing %q:\n%s", want, got)
				}
			}
		})
	}
}

func TestPredictiveRouterBackpressureLoadLeasePublishesFiniteIdleSentinel(t *testing.T) {
	now := time.Unix(82_500, 0)
	var state predictiveRouterBackpressureState
	result := runtimepredictive.CountAdmissionResult{
		Decision: domainpredictive.Decision{Reason: domainpredictive.ReasonNewTPSAtRisk},
		Prediction: runtimepredictive.SchedulerPrediction{
			Source: runtimepredictive.PredictionSourceCalibrated,
			Features: runtimepredictive.SchedulerFeatures{
				ExistingDecodeSequences: 1,
			},
		},
	}
	if event := state.Observe(now, 2*time.Second, result, predictiveRouterBackpressurePolicy{}); event == nil {
		t.Fatal("load reject did not create a protection lease")
	}
	active := state.Snapshot(now.Add(time.Second))
	if !active.Active || active.Scope != predictiveProtectionScopeLoad || active.MinimumRunning != 1 {
		t.Fatalf("active load lease snapshot did not publish a minimum running sentinel: %+v", active)
	}
	capacity := predictiveRouterCapacity("enforce", active, 0, runtimedynamic.Snapshot{GlobalLimit: 50, QOSLimit: 50})
	if !capacity.BackpressureApplied || capacity.EffectiveRunning != 1 || capacity.EffectiveGlobalLimit != 1 {
		t.Fatalf("idle scrape punched through the finite load lease: %+v", capacity)
	}
	expired := state.Snapshot(now.Add(2 * time.Second))
	if expired.Active || expired.MinimumRunning != 0 {
		t.Fatalf("expired load lease retained its sentinel: %+v", expired)
	}
	restored := predictiveRouterCapacity("enforce", expired, 0, runtimedynamic.Snapshot{GlobalLimit: 50, QOSLimit: 50})
	if restored.BackpressureApplied || restored.EffectiveRunning != 0 || restored.EffectiveGlobalLimit != 0 || restored.RawGlobalLimit != 50 {
		t.Fatalf("expired load lease self-locked Router capacity: %+v", restored)
	}
}

func TestPredictiveRouterCapacityEventIsLoggedOncePerAppliedEpisode(t *testing.T) {
	var state predictiveRouterCapacityLogState
	input := metrics.PredictiveAdmissionInput{
		RouterBackpressure: metrics.PredictiveRouterBackpressureInput{
			Active:      true,
			Activation:  1,
			Scope:       string(predictiveProtectionScopeLoad),
			Applied:     true,
			Reason:      "existing_tps_at_risk",
			Source:      "calibrated",
			ActivatedAt: time.Unix(83_000, 0),
			Activations: 1,
		},
	}
	capacity := predictiveRouterCapacityProjection{
		Activation:           1,
		Scope:                predictiveProtectionScopeLoad,
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
		"scope=load",
		"reason=existing_tps_at_risk",
		"source=calibrated",
		"predictive_running=1",
		"raw_running=0",
		"effective_running=1",
		"raw_global_limit=50",
		"effective_global_limit=1",
		"activated_at=1970-01-01T23:03:20Z",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("capacity log missing %q: %s", want, line)
		}
	}
	for _, forbidden := range []string{"model=", "user=", "request_id=", "prompt=", "body=", "bearer=", "token="} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Fatalf("capacity log contains request or secret field %q: %s", forbidden, line)
		}
	}

	input.RouterBackpressure.Activations = 2
	capacity.Activation = 2
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
	if got := normalizePredictiveRouterBackpressureHold(time.Millisecond); got != minimumPredictiveRouterBackpressureHold {
		t.Fatalf("short direct hold=%s want=%s", got, minimumPredictiveRouterBackpressureHold)
	}
	if got := normalizePredictiveRouterBackpressureHold(time.Hour); got != maximumPredictiveRouterBackpressureHold {
		t.Fatalf("long direct hold=%s want=%s", got, maximumPredictiveRouterBackpressureHold)
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
		LastReason: "request_size", LastSource: "deterministic",
		Reservations: 1, VirtualDecodeSequences: 3,
		ForwardedPendingPrefills: 1, ForwardedPendingPrefillTokens: 300, ForwardedPendingPrefillAttributionValid: true,
		RetiredReservations: 2, RetiredEvictions: 1,
		RequestAwareAction: "size_protect", RequestAwareReason: "request_size", RequestAwarePressureSource: "tps",
		RequestAwarePressure: 0.6, RequestAwareSelectionInputTokens: 1500, RequestAwareReservedTokens: 1600,
		RequestAwareAllowanceTokens: 1333, RequestAwareEffectiveKV: 7000, RequestAwarePostAdmitKV: 8600, RequestAwareRemainingKV: 2000,
		RequestAwareRunning: 4, RequestAwareWaiting: 1, RequestAwareEffectiveSequences: 4,
		RequestAwareAggregateTPSProxy: 80, RequestAwareMeanActiveTPSProxy: 20, RequestAwareProjectedTPSProxy: 16, RequestAwareTPSForecastValid: true,
		RouterBackpressure: metrics.PredictiveRouterBackpressureInput{
			Active: true, Applied: true, Scope: "load", Reason: "request_size", InspectCapacity: 1,
			Activation: 2, LatestRejectAt: time.Unix(84_000, 0),
			RawRunning: 1, EffectiveRunning: 1, RawGlobalLimit: 50, EffectiveGlobalLimit: 1,
		},
	})
	for _, want := range []string{
		"predictive={mode=enforce", "attempts=12", "risk=8", "last=request_size/deterministic",
		"last_reject=none/unknown/none", "reservations=1", "virtual_decode=3", "pending_prefill=1/300/1", "retired=2/1",
		"request_aware=size_protect/request_size/tps", "pressure=0.600", "size=1500/1600/1333", "kv=7000/8600/2000", "load=4/1/4", "tps=80.000/20.000/16.000/1",
		"router_bp=1/1/load/request_size", "inspect=1", "activation=2", "effective=1/1", "raw=1/50",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("predictive status missing %q: %s", want, line)
		}
	}
}
