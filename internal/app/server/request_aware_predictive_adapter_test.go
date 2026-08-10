package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type staticRequestAwareSnapshot struct {
	input runtimepredictive.RequestAwareInput
}

func (s staticRequestAwareSnapshot) RequestAwareInput(time.Time) runtimepredictive.RequestAwareInput {
	return s.input
}

type splitRequestAwareTelemetrySnapshot struct {
	requestInput  runtimepredictive.RequestAwareInput
	observer      predictiveObserverSnapshot
	requestReads  int
	observerReads int
}

func (s *splitRequestAwareTelemetrySnapshot) RequestAwareInput(time.Time) runtimepredictive.RequestAwareInput {
	s.requestReads++
	return s.requestInput
}

func (s *splitRequestAwareTelemetrySnapshot) Snapshot(time.Time) predictiveObserverSnapshot {
	s.observerReads++
	return s.observer
}

func TestV0128RequestAwareTelemetryObservationPreservesSequence(t *testing.T) {
	now := time.Unix(2_500, 0)
	provider := &splitRequestAwareTelemetrySnapshot{
		requestInput: runtimepredictive.RequestAwareInput{ObservationSequence: 99},
		observer: predictiveObserverSnapshot{
			ObservedAt: now, MetricsFresh: true, IdentityValid: true,
			ObservationSequence: 17, CapacityTokens: 10_000, UsedTokens: 5_000, Running: 4,
		},
	}
	input, observer, valid := requestAwareTelemetryObservation(provider, now)
	if !valid || input.ObservationSequence != 17 || observer.ObservationSequence != 17 ||
		provider.requestReads != 0 || provider.observerReads != 1 {
		t.Fatalf("telemetry observation input/snapshot/reads=%+v/%+v/%d/%d, want paired sequence 17 from one snapshot",
			input, observer, provider.requestReads, provider.observerReads)
	}
}

func TestRequestAwareAdapterRequestSizeChangesPreForwardDecision(t *testing.T) {
	smallAdapter, smallManager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 0, "enforce")
	largeAdapter, largeManager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 0, "enforce")

	small := smallAdapter.Decide(context.Background(), "small", requestAwareAdapterInput(8*1024, 100))
	large := largeAdapter.Decide(context.Background(), "large", requestAwareAdapterInput(650*1024, 100))
	if small.Outcome != predictiveAdmissionOutcomeForward || small.Reservation == nil {
		t.Fatalf("small pre-forward decision=%+v, want forward with reservation", small)
	}
	if large.Outcome != predictiveAdmissionOutcomeRequestReject || large.Reservation != nil ||
		large.Reason != domainpredictive.ReasonRequestSizeAtPressure {
		t.Fatalf("large pre-forward decision=%+v, want request-scoped size protection", large)
	}
	if smallManager.Snapshot().Reservations != 1 || largeManager.Snapshot().Reservations != 0 {
		t.Fatalf("reservation causality small=%d large=%d", smallManager.Snapshot().Reservations, largeManager.Snapshot().Reservations)
	}
}

func TestRequestAwareAdapterOpenForwardsHardFitLargeRequest(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	decision := adapter.Decide(context.Background(), "open-large", requestAwareAdapterInput(2_500, 500))
	if decision.Outcome != predictiveAdmissionOutcomeForward || decision.Reservation == nil || manager.Snapshot().Reservations != 1 {
		t.Fatalf("open large decision=%+v reservations=%d", decision, manager.Snapshot().Reservations)
	}
}

func TestRequestAwareAdapterWaitingRemainsRequestSizeAwareBeforeForward(t *testing.T) {
	smallAdapter, _ := newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 1, "enforce")
	largeAdapter, _ := newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 1, "enforce")
	small := smallAdapter.Decide(context.Background(), "waiting-small", requestAwareAdapterInput(8*1024, 100))
	large := largeAdapter.Decide(context.Background(), "waiting-large", requestAwareAdapterInput(100*1024, 100))
	if small.Outcome != predictiveAdmissionOutcomeForward || small.Reservation == nil ||
		large.Outcome != predictiveAdmissionOutcomeLoadProtection || large.Reservation != nil {
		t.Fatalf("waiting pre-forward decisions small=%+v large=%+v, want regular forward and weighted protection", small, large)
	}
	smallTelemetry := smallAdapter.PredictiveAdmissionTelemetry().RequestAware
	largeTelemetry := largeAdapter.PredictiveAdmissionTelemetry().RequestAware
	if smallTelemetry.Reason != runtimepredictive.RequestAwareReasonOpen ||
		smallTelemetry.PressureSource != runtimepredictive.RequestAwarePressureNone ||
		largeTelemetry.Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
		largeTelemetry.PressureSource != runtimepredictive.RequestAwarePressurePrefill {
		t.Fatalf("waiting telemetry small=%+v large=%+v", smallTelemetry, largeTelemetry)
	}
	if !small.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("waiting-small reservation did not release")
	}
}

func TestRequestAwareAdapterReservationUsesExistingLifecycle(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	decision := adapter.Decide(context.Background(), "lifecycle", requestAwareAdapterInput(500, 100))
	if decision.Reservation == nil || !decision.Reservation.MarkForwarded() ||
		!decision.Reservation.MarkPrefillComplete() ||
		!decision.Reservation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatalf("adapter lifecycle decision=%+v", decision)
	}
	if manager.Snapshot().Reservations != 0 {
		t.Fatalf("adapter lifecycle leaked reservation: %+v", manager.Snapshot())
	}
}

func TestV0125RequestAwareAdapterProjectsWeightedPrefillBeforeNextRequest(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "enforce")
	setRequestAwareAdapterObservation(t, adapter, manager, 0, 0, 0)
	weighted := adapter.Decide(
		context.Background(), "weighted-router-projection", requestAwareAdapterInput(195*1024, 0),
	)
	if weighted.Outcome != predictiveAdmissionOutcomeForward || weighted.Reservation == nil ||
		!weighted.Reservation.MarkForwarded() {
		t.Fatalf("weighted setup=%+v, want forwarded reservation", weighted)
	}
	protected := adapter.PredictiveAdmissionTelemetry()
	if !protected.RouterBackpressure.Active || protected.RouterBackpressure.InspectCapacity != 0 ||
		protected.RequestAware.PendingLongPrefillSequences != 1 {
		t.Fatalf("weighted Router projection=%+v request=%+v, want active zero-capacity before next request", protected.RouterBackpressure, protected.RequestAware)
	}
	if !weighted.Reservation.MarkPrefillComplete() {
		t.Fatal("weighted Prefill did not complete")
	}
	recovered := adapter.PredictiveAdmissionTelemetry()
	if recovered.RouterBackpressure.Active || recovered.RouterBackpressure.InspectCapacity != 0 ||
		recovered.RequestAware.PendingLongPrefillSequences != 0 {
		t.Fatalf("post-Prefill Router projection=%+v request=%+v, want immediate recovery", recovered.RouterBackpressure, recovered.RequestAware)
	}
	regular := adapter.Decide(
		context.Background(), "regular-after-weighted", requestAwareAdapterInput(8*1024, 0),
	)
	if regular.Outcome != predictiveAdmissionOutcomeForward || regular.Reservation == nil {
		t.Fatalf("regular after weighted=%+v, want forward", regular)
	}
	if !regular.Reservation.Terminate(runtimepredictive.TerminalCompleted) ||
		!weighted.Reservation.Terminate(runtimepredictive.TerminalCompleted) ||
		manager.Snapshot().Reservations != 0 {
		t.Fatalf("weighted Router lifecycle leaked: %+v", manager.Snapshot())
	}
}

func TestV0125RequestAwareAdapterRejectsRegularDuringWeightedPrefill(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "enforce")
	setRequestAwareAdapterObservation(t, adapter, manager, 0, 0, 0)
	weighted := adapter.Decide(
		context.Background(), "weighted-business-gate", requestAwareAdapterInput(195*1024, 0),
	)
	if weighted.Reservation == nil || !weighted.Reservation.MarkForwarded() {
		t.Fatalf("weighted setup=%+v, want forwarded reservation", weighted)
	}
	regular := adapter.Decide(
		context.Background(), "regular-during-weighted", requestAwareAdapterInput(8*1024, 0),
	)
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if regular.Outcome != predictiveAdmissionOutcomeLoadProtection || regular.Reservation != nil ||
		telemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonPrefillBusy {
		t.Fatalf("regular during weighted decision/telemetry=%+v/%+v, want pre-forward Prefill protection", regular, telemetry.RequestAware)
	}
	if !weighted.Reservation.MarkPrefillComplete() {
		t.Fatal("weighted Prefill did not complete")
	}
	postPrefill := adapter.PredictiveAdmissionTelemetry()
	if postPrefill.RouterBackpressure.Active || postPrefill.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("post-Prefill Router projection=%+v, want immediate recovery after represented 429", postPrefill.RouterBackpressure)
	}
	recovery := adapter.Decide(
		context.Background(), "regular-immediate-recovery", requestAwareAdapterInput(8*1024, 0),
	)
	if recovery.Outcome != predictiveAdmissionOutcomeForward || recovery.Reservation == nil {
		t.Fatalf("regular immediate recovery=%+v, want forward", recovery)
	}
	if !recovery.Reservation.Terminate(runtimepredictive.TerminalCompleted) ||
		!weighted.Reservation.Terminate(runtimepredictive.TerminalCompleted) ||
		manager.Snapshot().Reservations != 0 {
		t.Fatalf("weighted business lifecycle leaked: %+v", manager.Snapshot())
	}
}

func TestV0125RequestAwareAdapterPrefillCompletionSupersedesRecentRejectProjection(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "enforce")
	setRequestAwareAdapterObservation(t, adapter, manager, 0, 0, 0)
	now := time.Unix(100, 0)
	adapter.now = func() time.Time { return now }
	weighted := adapter.Decide(
		context.Background(), "weighted-reject-generation", requestAwareAdapterInput(195*1024, 0),
	)
	if weighted.Outcome != predictiveAdmissionOutcomeForward || weighted.Reservation == nil ||
		!weighted.Reservation.MarkForwarded() {
		t.Fatalf("weighted reject-generation setup=%+v", weighted)
	}
	rejected := adapter.Decide(
		context.Background(), "oversized-during-weighted", requestAwareAdapterInput(4*1024*1024, 0),
	)
	if rejected.Outcome != predictiveAdmissionOutcomeLoadProtection || rejected.Reservation != nil {
		t.Fatalf("reject-generation decision=%+v, want represented load protection", rejected)
	}
	if protected := adapter.PredictiveAdmissionTelemetry(); !protected.RouterBackpressure.Active {
		t.Fatalf("recent reject was not projected before lifecycle recovery: %+v", protected.RouterBackpressure)
	}
	if !weighted.Reservation.MarkPrefillComplete() {
		t.Fatal("weighted reject-generation Prefill did not complete")
	}
	recovered := adapter.PredictiveAdmissionTelemetry()
	if recovered.RouterBackpressure.Active || recovered.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("post-Prefill Router projection=%+v, want lifecycle state change to supersede stale reject hold", recovered.RouterBackpressure)
	}
	if !weighted.Reservation.Terminate(runtimepredictive.TerminalCompleted) || manager.Snapshot().Reservations != 0 {
		t.Fatalf("weighted reject-generation lifecycle leaked: %+v", manager.Snapshot())
	}
}

func TestV0125RequestAwareAdapterTerminalAndRebaseSupersedeRecentRejectProjection(t *testing.T) {
	for _, test := range []struct {
		name    string
		release func(*runtimepredictive.Manager, predictiveShadowReservation) bool
	}{
		{
			name: "terminal",
			release: func(_ *runtimepredictive.Manager, reservation predictiveShadowReservation) bool {
				return reservation.Terminate(runtimepredictive.TerminalClientCancelled)
			},
		},
		{
			name: "epoch rebase",
			release: func(manager *runtimepredictive.Manager, _ predictiveShadowReservation) bool {
				return manager.RebaseEpoch(domainpredictive.VirtualState{}) == nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "enforce")
			setRequestAwareAdapterObservation(t, adapter, manager, 0, 0, 0)
			now := time.Unix(100, 0)
			adapter.now = func() time.Time { return now }
			weighted := adapter.Decide(
				context.Background(), "weighted-"+test.name, requestAwareAdapterInput(195*1024, 0),
			)
			if weighted.Reservation == nil || !weighted.Reservation.MarkForwarded() {
				t.Fatalf("weighted setup=%+v", weighted)
			}
			rejected := adapter.Decide(
				context.Background(), "regular-"+test.name, requestAwareAdapterInput(8*1024, 0),
			)
			if rejected.Outcome != predictiveAdmissionOutcomeLoadProtection || rejected.Reservation != nil {
				t.Fatalf("regular during weighted=%+v, want represented load protection", rejected)
			}
			if protected := adapter.PredictiveAdmissionTelemetry(); !protected.RouterBackpressure.Active ||
				protected.RouterBackpressure.InspectCapacity != 0 {
				t.Fatalf("setup Router projection=%+v, want hard protection", protected.RouterBackpressure)
			}
			if !test.release(manager, weighted.Reservation) {
				t.Fatalf("%s did not release weighted reservation", test.name)
			}
			recovered := adapter.PredictiveAdmissionTelemetry()
			if recovered.RouterBackpressure.Active || recovered.RouterBackpressure.InspectCapacity != 0 ||
				manager.Snapshot().Reservations != 0 {
				t.Fatalf("post-%s Router/Manager=%+v/%+v, want immediate open without leak",
					test.name, recovered.RouterBackpressure, manager.Snapshot())
			}
		})
	}
}

func TestRequestAwareAdapterFreshCompletionSnapshotDoesNotMislockIdleBackend(t *testing.T) {
	manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{
		PhysicalKVUpper:     2_000,
		ActiveKVUpper:       2_000,
		ActiveContextTokens: 2_000,
	})
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		HardKVLimitTokens:            8_992,
		BlockSize:                    16,
		PrefillRegularTokens:         runtimepredictive.DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       runtimepredictive.DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       runtimepredictive.DefaultRequestAwarePrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: runtimepredictive.DefaultRequestAwarePrefillAggregateBudgetTokens,
	})
	if err != nil {
		t.Fatalf("NewRequestAwarePolicy: %v", err)
	}
	adapter, err := newRequestAwarePredictiveAdapter(requestAwarePredictiveAdapterConfig{
		Manager:           manager,
		Policy:            policy,
		CapabilityProfile: requestAwareTestCapabilityProfile(10_000, 16, 8_992),
		CapabilityReason:  "test",
		Snapshot: staticRequestAwareSnapshot{input: runtimepredictive.RequestAwareInput{
			MetricsFresh:       true,
			IdentityValid:      true,
			CapacityTokens:     10_000,
			Running:            0,
			EffectiveSequences: 0,
			AggregateTPSProxy:  40,
			MeanActiveTPSProxy: 20,
			TPSValid:           true,
		}},
		ManifestID: "request-aware-http-test",
		BlockSize:  16,
		Mode:       "enforce",
		Now:        func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatalf("newRequestAwarePredictiveAdapter: %v", err)
	}

	decision := adapter.Decide(context.Background(), "idle-after-completion", requestAwareAdapterInput(2_500, 500))
	if decision.Outcome != predictiveAdmissionOutcomeForward || decision.Reservation == nil {
		t.Fatalf("idle completion-window adapter decision=%+v, want forward with reservation", decision)
	}
	telemetry := adapter.PredictiveAdmissionTelemetry().RequestAware
	if telemetry.Action != runtimepredictive.RequestAwareAdmit || telemetry.Reason != runtimepredictive.RequestAwareReasonOpen ||
		telemetry.Pressure != 0 {
		t.Fatalf("idle completion-window telemetry=%+v, want TPS-neutral admit/open", telemetry)
	}
	if !decision.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("idle completion-window reservation did not release")
	}
}

func TestRequestAwareAdapterRejectsPolicyCapabilityMismatch(t *testing.T) {
	manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{})
	policy := newLargeRequestAwareServerTestPolicy(t)
	_, err := newRequestAwarePredictiveAdapter(requestAwarePredictiveAdapterConfig{
		Manager:           manager,
		Policy:            policy,
		CapabilityProfile: requestAwareTestCapabilityProfile(10_000, 16, 8_992),
		Snapshot: staticRequestAwareSnapshot{input: runtimepredictive.RequestAwareInput{
			MetricsFresh:  true,
			IdentityValid: true,
		}},
		ManifestID: "request-aware-http-test",
		BlockSize:  16,
		Mode:       "enforce",
	})
	if err == nil {
		t.Fatal("adapter accepted policy/profile mismatch")
	}
}

func TestRequestAwareAdapterCloseBeforeForwardCommitRejectsAndReleasesReservation(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	snapshot := adapter.snapshot.(staticRequestAwareSnapshot).input
	snapshot.CapacityTokens = 4 * 1024 * 1024
	snapshot.TPSValid = false
	adapter.snapshot = staticRequestAwareSnapshot{input: snapshot}
	adapter.policy = newLargeRequestAwareServerTestPolicy(t)
	setRequestAwareAdapterObservation(t, adapter, manager, 5_000, 0, 0)
	input := requestAwareAdapterInput(690*1024, 0)
	input.Cost.ApproximateInputTokens = 285 * 1024
	decision := adapter.Decide(context.Background(), "close-before-forward", input)
	if decision.Reservation == nil || manager.Snapshot().Reservations != 1 {
		t.Fatalf("setup decision=%+v manager=%+v", decision, manager.Snapshot())
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("close request-aware adapter: %v", err)
	}
	if decision.Reservation.MarkForwarded() {
		t.Fatal("reservation committed forward after adapter Close linearized first")
	}
	if !decision.Reservation.Terminate(runtimepredictive.TerminalExpired) ||
		decision.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("close-rejected divergent reservation did not release exact once through terminal lifecycle")
	}
	managerSnapshot := manager.Snapshot()
	if managerSnapshot.IntakeOpen || managerSnapshot.Reservations != 0 {
		t.Fatalf("close-before-forward lifecycle leaked or reopened intake: %+v", managerSnapshot)
	}
}

func TestRequestAwareAdapterUnknownLexicalHintFallsBackToSafetyUpper(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 0, 0)
	snapshot := adapter.snapshot.(staticRequestAwareSnapshot).input
	snapshot.CapacityTokens = 4 * 1024 * 1024
	snapshot.TPSValid = false
	adapter.snapshot = staticRequestAwareSnapshot{input: snapshot}
	adapter.policy = newLargeRequestAwareServerTestPolicy(t)
	input := requestAwareAdapterInput(650*1024, 0)
	input.Cost.ApproximateInputTokens = 0
	input.Cost.ApproximateInputTokensKnown = false

	decision := adapter.Decide(context.Background(), "unknown-lexical-hint", input)
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if decision.Outcome != predictiveAdmissionOutcomeRequestReject || decision.Reservation != nil ||
		telemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonDecodeInterference ||
		telemetry.RequestAware.PressureSource != runtimepredictive.RequestAwarePressureDecode ||
		telemetry.RequestAware.PrefillClass != runtimepredictive.RequestAwarePrefillQuiescent ||
		telemetry.RequestAware.SelectionInputTokens != 650*1024 ||
		telemetry.RequestAware.EstimatedPrefillTokens != 650*1024 ||
		manager.Snapshot().Reservations != 0 {
		t.Fatalf("unknown lexical fallback decision/telemetry/manager=%+v/%+v/%+v", decision, telemetry, manager.Snapshot())
	}
}

func TestRequestAwareAdapterChargesKnownMultimodalWorkToPrefillAdmission(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 0, 1)
	input := requestAwareAdapterInput(8*1024, 0)
	input.Cost.ApproximateInputTokens = 256
	input.Cost.ModalityCount = 1

	decision := adapter.Decide(context.Background(), "multimodal-prefill", input)
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if decision.Outcome != predictiveAdmissionOutcomeForward || decision.Reservation == nil ||
		decision.Reason != domainpredictive.ReasonFit ||
		telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonOpen ||
		telemetry.RequestAware.SelectionInputTokens != 8*1024 ||
		telemetry.RequestAware.EstimatedPrefillTokens != 8*1024 ||
		manager.Snapshot().Reservations != 1 {
		t.Fatalf("multimodal decision/telemetry/manager=%+v/%+v/%+v, want regular Prefill admission", decision, telemetry, manager.Snapshot())
	}
	if !decision.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("multimodal reservation did not release")
	}
}

func TestRequestAwareAdapterCostPromotesLargerLexicalHintIntoKVUpper(t *testing.T) {
	selection, cost, valid := requestAwareAdapterCost("request-aware-http-test", 16, predictiveShadowInput{
		Cost: kvadmission.Cost{
			Supported:                   true,
			EstimatedInputHigh:          100,
			ApproximateInputTokens:      200,
			ApproximateInputTokensKnown: true,
			BoundedDecodeTokens:         10,
		},
	})
	if !valid || selection != 200 || cost.InputTokens != 200 ||
		cost.UncachedPrefillUpper != 200 || cost.KV.ActiveKVUpper != 224 ||
		cost.FutureKV.ActiveKVUpper != 16 || cost.ActiveContextTokensUpper != 210 {
		t.Fatalf("lexical KV safety cost=%+v selection=%d valid=%t", cost, selection, valid)
	}
}

func TestRequestAwareAdapterPendingTelemetryReportsCurrentStateAfterLastDecisionDrains(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 0, 0)
	snapshot := adapter.snapshot.(staticRequestAwareSnapshot).input
	snapshot.CapacityTokens = 4 * 1024 * 1024
	snapshot.TPSValid = false
	adapter.snapshot = staticRequestAwareSnapshot{input: snapshot}
	adapter.policy = newLargeRequestAwareServerTestPolicy(t)
	input := requestAwareAdapterInput(4*1024, 0)

	first := adapter.Decide(context.Background(), "telemetry-current-first", input)
	reconcileRequestAwareAdapterManager(t, manager, 0, 4, 0)
	second := adapter.Decide(context.Background(), "telemetry-current-second", input)
	if first.Reservation == nil || second.Reservation == nil || manager.Snapshot().Reservations != 2 {
		t.Fatalf("setup decisions/manager=%+v/%+v/%+v, want two live reservations", first, second, manager.Snapshot())
	}
	beforeDrain := adapter.PredictiveAdmissionTelemetry().RequestAware
	if beforeDrain.PendingPrefillSequences != 2 || beforeDrain.PendingPrefillTokens != 8*1024 ||
		beforeDrain.PostAdmitPendingPrefillTokens != 8*1024 {
		t.Fatalf("current pending telemetry before drain=%+v, want 2/8K", beforeDrain)
	}
	if beforeDrain.LastDecisionPendingPrefillSequences != 1 ||
		beforeDrain.LastDecisionPendingPrefillTokens != 4*1024 ||
		beforeDrain.LastDecisionPostAdmitPendingPrefillTokens != 8*1024 {
		t.Fatalf("last-decision pending telemetry before drain=%+v, want 1/4K/8K", beforeDrain)
	}

	if !first.Reservation.Terminate(runtimepredictive.TerminalExpired) ||
		!second.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("pending telemetry setup reservations did not drain")
	}
	afterDrain := adapter.PredictiveAdmissionTelemetry().RequestAware
	if afterDrain.PendingPrefillSequences != 0 || afterDrain.PendingPrefillTokens != 0 ||
		afterDrain.PostAdmitPendingPrefillTokens != 0 ||
		afterDrain.PendingLongPrefillSequences != 0 || afterDrain.PendingQuiescentPrefillSequences != 0 ||
		manager.Snapshot().Reservations != 0 {
		t.Fatalf("current pending telemetry after drain=%+v manager=%+v, want zero live demand", afterDrain, manager.Snapshot())
	}
	if afterDrain.Action != runtimepredictive.RequestAwareAdmit ||
		afterDrain.EstimatedPrefillTokens != 4*1024 ||
		afterDrain.LastDecisionPendingPrefillSequences != 1 ||
		afterDrain.LastDecisionPendingPrefillTokens != 4*1024 ||
		afterDrain.LastDecisionPostAdmitPendingPrefillTokens != 8*1024 {
		t.Fatalf("last-decision diagnostics were not preserved after current state drained: %+v", afterDrain)
	}
}

func TestRequestAwareAdapterShadowIsSideEffectFree(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 0, "shadow")
	before := manager.Snapshot()
	small := adapter.Decide(context.Background(), "shadow-small", requestAwareAdapterInput(8*1024, 100))
	large := adapter.Decide(context.Background(), "shadow-large", requestAwareAdapterInput(650*1024, 100))
	after := manager.Snapshot()
	if small.Outcome != predictiveAdmissionOutcomeForward || small.Reservation != nil {
		t.Fatalf("shadow small decision=%+v, want forward without reservation", small)
	}
	if large.Outcome != predictiveAdmissionOutcomeRequestReject || large.Reservation != nil ||
		large.Reason != domainpredictive.ReasonRequestSizeAtPressure {
		t.Fatalf("shadow large decision=%+v, want would-protect without reservation", large)
	}
	if after.Reservations != before.Reservations || after.EventSequence != before.EventSequence || after.Virtual != before.Virtual {
		t.Fatalf("shadow changed Manager state: before=%+v after=%+v", before, after)
	}
}

func TestRequestAwareAdapterTelemetryPublishesSelectiveVerdictAndInspectCapacity(t *testing.T) {
	adapter, _ := newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 0, "enforce")
	decision := adapter.Decide(context.Background(), "telemetry-large", requestAwareAdapterInput(650*1024, 100))
	if decision.Outcome != predictiveAdmissionOutcomeRequestReject {
		t.Fatalf("setup decision=%+v, want size protection", decision)
	}
	snapshot := adapter.PredictiveAdmissionTelemetry()
	if snapshot.Attempts.Attempts != 1 || snapshot.Attempts.Risks != 1 ||
		snapshot.Attempts.LastRejectReason != domainpredictive.ReasonRequestSizeAtPressure {
		t.Fatalf("selective attempts telemetry=%+v", snapshot.Attempts)
	}
	if snapshot.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		snapshot.RequestAware.Reason != runtimepredictive.RequestAwareReasonDecodeInterference ||
		snapshot.RequestAware.PressureSource != runtimepredictive.RequestAwarePressureDecode ||
		snapshot.RequestAware.SelectionInputTokens != 650*1024 || snapshot.RequestAware.ReservedTokens != 665_712 ||
		snapshot.RequestAware.EffectiveKV != 128*1024 || snapshot.RequestAware.PostAdmitKV != 796_784 ||
		snapshot.RequestAware.RemainingKV != 3_643_792 || snapshot.RequestAware.AllowanceTokens != 0 ||
		snapshot.RequestAware.Running != 4 || snapshot.RequestAware.Waiting != 0 ||
		snapshot.RequestAware.EffectiveSequences != 4 || snapshot.RequestAware.AggregateTPSProxy != 80 ||
		snapshot.RequestAware.MeanActiveTPSProxy != 20 {
		t.Fatalf("selective request-aware telemetry=%+v", snapshot.RequestAware)
	}
	if snapshot.RouterBackpressure.Active || snapshot.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("selective Router telemetry=%+v, want request-scoped reject with fitting intake open", snapshot.RouterBackpressure)
	}
}

func TestRequestAwareAdapterTelemetryPublishesHardProtectionWithZeroInspectCapacity(t *testing.T) {
	adapter, _ := newRequestAwareAdapterTestFixture(t, 8_990, 0)
	decision := adapter.Decide(context.Background(), "telemetry-hard", requestAwareAdapterInput(16, 0))
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection || decision.Reason != domainpredictive.ReasonKVOverBudget {
		t.Fatalf("setup decision=%+v, want hard KV protection", decision)
	}
	snapshot := adapter.PredictiveAdmissionTelemetry()
	if snapshot.RequestAware.Action != runtimepredictive.RequestAwareHardProtect ||
		!snapshot.RouterBackpressure.Active || snapshot.RouterBackpressure.InspectCapacity != 0 ||
		snapshot.RouterBackpressure.Reason != domainpredictive.ReasonKVOverBudget {
		t.Fatalf("hard protection telemetry request=%+v Router=%+v", snapshot.RequestAware, snapshot.RouterBackpressure)
	}
}

func TestRequestAwareAdapterCurrentHardProtectionSupersedesEqualStrengthRecentReject(t *testing.T) {
	now := time.Unix(100, 0)
	adapter := &requestAwarePredictiveAdapter{
		attempts: predictiveAttemptSnapshot{
			LastRejectReason: domainpredictive.ReasonMetricsStale,
			LastRejectSource: runtimepredictive.PredictionSourceUnavailable,
			LastRejectScope:  predictiveProtectionScopeAvailability,
			LastRejectAt:     now,
		},
	}
	current := requestAwareHardRouterBackpressure(
		domainpredictive.ReasonKVOverBudget,
		runtimepredictive.PredictionSourceDeterministic,
		predictiveProtectionScopeLoad,
	)
	got := adapter.transitionRouterBackpressureLocked(now, current)
	if got.Scope != current.Scope || got.Reason != current.Reason || got.Source != current.Source ||
		!got.Active || got.InspectCapacity != 0 {
		t.Fatalf("current hard projection=%+v, want current equal-strength verdict=%+v to supersede recent availability verdict", got, current)
	}
}

func TestRequestAwareAdapterTelemetryProjectsRecentRequestSpecificHardProtection(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	now := time.Unix(100, 0)
	adapter.now = func() time.Time { return now }

	decision := adapter.Decide(context.Background(), "telemetry-request-specific-hard", requestAwareAdapterInput(4_000, 0))
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection || decision.Reason != domainpredictive.ReasonKVOverBudget {
		t.Fatalf("request-specific decision=%+v, want hard KV protection", decision)
	}
	beforeScrape := manager.Snapshot()
	protected := adapter.PredictiveAdmissionTelemetry()
	afterScrape := manager.Snapshot()
	if !protected.RouterBackpressure.Active || protected.RouterBackpressure.InspectCapacity != 1 ||
		protected.RouterBackpressure.Scope != predictiveProtectionScopeLoad ||
		protected.RouterBackpressure.Reason != domainpredictive.ReasonKVOverBudget ||
		protected.RouterBackpressure.Source != runtimepredictive.PredictionSourceDeterministic ||
		!protected.RouterBackpressure.LatestRejectAt.Equal(now) {
		t.Fatalf("request-specific Router telemetry=%+v, want recent selective KV protection", protected.RouterBackpressure)
	}
	if beforeScrape != afterScrape || protected.Attempts.Attempts != 1 || protected.Attempts.Risks != 1 {
		t.Fatalf("telemetry scrape mutated business state: before=%+v after=%+v attempts=%+v", beforeScrape, afterScrape, protected.Attempts)
	}
	compatibility := predictiveRouterCompatibility("enforce", protected)
	if compatibility.ObservedRunningRaw != 0 || compatibility.ObservedWaitingRaw != 0 ||
		compatibility.ObservedRunning != 1 || compatibility.ObservedWaiting != 0 ||
		compatibility.GlobalLimitRaw != 0 || compatibility.GlobalLimit != 2 {
		t.Fatalf("request-specific compatibility projection=%+v, want raw 0/0 and effective 1/0/2", compatibility)
	}

	now = now.Add(requestAwareRouterRejectProjectionHold - time.Nanosecond)
	if held := adapter.PredictiveAdmissionTelemetry(); !held.RouterBackpressure.Active || held.RouterBackpressure.InspectCapacity != 1 {
		t.Fatalf("request-specific Router telemetry cleared before bounded hold: %+v", held.RouterBackpressure)
	}
	now = now.Add(time.Nanosecond)
	recovered := adapter.PredictiveAdmissionTelemetry()
	if recovered.RouterBackpressure.Active || recovered.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("request-specific Router telemetry=%+v, want fresh open at hold boundary", recovered.RouterBackpressure)
	}
	if recovered.Attempts.Attempts != 1 || recovered.Attempts.Risks != 1 || manager.Snapshot() != beforeScrape {
		t.Fatalf("bounded recovery mutated business accounting: telemetry=%+v manager=%+v", recovered.Attempts, manager.Snapshot())
	}
}

func TestRequestAwareAdapterShadowDoesNotProjectRequestSpecificHardProtection(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixtureWithMode(t, 5_000, 0, "shadow")
	now := time.Unix(100, 0)
	adapter.now = func() time.Time { return now }

	decision := adapter.Decide(context.Background(), "shadow-request-specific-hard", requestAwareAdapterInput(4_000, 0))
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection || decision.Reason != domainpredictive.ReasonKVOverBudget {
		t.Fatalf("shadow request-specific decision=%+v, want counterfactual hard KV protection", decision)
	}
	snapshot := adapter.PredictiveAdmissionTelemetry()
	if snapshot.RouterBackpressure.Active || snapshot.RouterBackpressure.InspectCapacity != 0 ||
		!snapshot.RouterBackpressure.LatestRejectAt.IsZero() || snapshot.Attempts.Attempts != 1 ||
		snapshot.Attempts.Risks != 1 || !snapshot.Attempts.LastRejectAt.IsZero() || manager.Snapshot().Reservations != 0 {
		t.Fatalf("shadow request-specific telemetry/manager=%+v/%+v, want non-authoritative", snapshot, manager.Snapshot())
	}
}

func TestRecentRequestAwareRejectProjectionIsBoundedAndScopeAware(t *testing.T) {
	now := time.Unix(200, 0)
	base := predictiveAttemptSnapshot{
		LastRejectReason: domainpredictive.ReasonKVOverBudget,
		LastRejectSource: runtimepredictive.PredictionSourceDeterministic,
		LastRejectScope:  predictiveProtectionScopeLoad,
		LastRejectAt:     now,
	}

	load, ok := recentRequestAwareRejectProjection(now, base)
	if !ok || !load.Active || load.InspectCapacity != 1 ||
		load.Scope != predictiveProtectionScopeLoad || load.Reason != domainpredictive.ReasonKVOverBudget {
		t.Fatalf("recent load projection=%+v/%v, want selective load protection", load, ok)
	}
	availabilityInput := base
	availabilityInput.LastRejectScope = predictiveProtectionScopeAvailability
	availability, ok := recentRequestAwareRejectProjection(now, availabilityInput)
	if !ok || !availability.Active || availability.InspectCapacity != 0 ||
		availability.Scope != predictiveProtectionScopeAvailability {
		t.Fatalf("recent availability projection=%+v/%v, want hard availability protection", availability, ok)
	}
	requestInput := base
	requestInput.LastRejectScope = predictiveProtectionScopeRequest
	if projection, ok := recentRequestAwareRejectProjection(now, requestInput); ok || projection.Active {
		t.Fatalf("request-scoped reject projection=%+v/%v, want no Router capacity change", projection, ok)
	}
	expiredInput := base
	expiredInput.LastRejectAt = now.Add(-requestAwareRouterRejectProjectionHold)
	if projection, ok := recentRequestAwareRejectProjection(now, expiredInput); ok || projection.Active {
		t.Fatalf("expired reject projection=%+v/%v, want open", projection, ok)
	}
	futureInput := base
	futureInput.LastRejectAt = now.Add(time.Nanosecond)
	if projection, ok := recentRequestAwareRejectProjection(now, futureInput); ok || projection.Active {
		t.Fatalf("future reject projection=%+v/%v, want open", projection, ok)
	}
}

func TestV0121RequestAwareTelemetryUsesOneObserverGenerationForRouterProjection(t *testing.T) {
	manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{
		PhysicalKVUpper:     5_000,
		ActiveKVUpper:       5_000,
		DecodeSequences:     4,
		ActiveContextTokens: 5_000,
	})
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		HardKVLimitTokens:            8_992,
		BlockSize:                    16,
		PrefillRegularTokens:         runtimepredictive.DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       runtimepredictive.DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       runtimepredictive.DefaultRequestAwarePrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: runtimepredictive.DefaultRequestAwarePrefillAggregateBudgetTokens,
	})
	if err != nil {
		t.Fatalf("NewRequestAwarePolicy: %v", err)
	}
	now := time.Unix(3_000, 0)
	provider := &splitRequestAwareTelemetrySnapshot{
		requestInput: runtimepredictive.RequestAwareInput{},
		observer: predictiveObserverSnapshot{
			ObservedAt: now, MetricsFresh: true, IdentityValid: true,
			CapacityTokens: 10_000, UsedTokens: 5_000, Running: 4,
			AggregateTPS: 80, MeanActiveTPS: 20, TPSValid: true,
		},
	}
	adapter, err := newRequestAwarePredictiveAdapter(requestAwarePredictiveAdapterConfig{
		Manager: manager, Policy: policy,
		CapabilityProfile: requestAwareTestCapabilityProfile(10_000, 16, 8_992),
		CapabilityReason:  "test", Snapshot: provider,
		ManifestID: "request-aware-http-test", BlockSize: 16, Mode: "enforce",
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("newRequestAwarePredictiveAdapter: %v", err)
	}

	telemetry := adapter.PredictiveAdmissionTelemetry()
	if provider.requestReads != 0 || provider.observerReads != 1 {
		t.Fatalf("telemetry observer reads request=%d snapshot=%d, want 0/1", provider.requestReads, provider.observerReads)
	}
	if telemetry.RouterBackpressure.Active || !telemetry.Observer.MetricsFresh || telemetry.Observer.Running != 4 {
		t.Fatalf("mixed-generation telemetry Router=%+v observer=%+v", telemetry.RouterBackpressure, telemetry.Observer)
	}
}

func TestRequestAwareAdapterTelemetryProbeRecoversWithoutNewBusinessRequest(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 8_990, 0)
	decision := adapter.Decide(context.Background(), "telemetry-recovery", requestAwareAdapterInput(16, 0))
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("setup decision=%+v, want hard protection", decision)
	}
	beforeProbe := manager.Snapshot()
	protected := adapter.PredictiveAdmissionTelemetry()
	afterProbe := manager.Snapshot()
	if !protected.RouterBackpressure.Active || protected.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("protected Router telemetry=%+v, want hard clamp", protected.RouterBackpressure)
	}
	if beforeProbe.Reservations != afterProbe.Reservations || beforeProbe.EventSequence != afterProbe.EventSequence ||
		beforeProbe.Virtual != afterProbe.Virtual || protected.Attempts.Attempts != 1 {
		t.Fatalf("telemetry probe changed business state: before=%+v after=%+v attempts=%+v", beforeProbe, afterProbe, protected.Attempts)
	}

	if err := manager.ReconcileSample(runtimepredictive.SampleWindow{
		Observed: domainpredictive.VirtualState{
			PhysicalKVUpper:     5_000,
			ActiveKVUpper:       5_000,
			DecodeSequences:     4,
			ActiveContextTokens: 5_000,
		},
		StartedSequence:  beforeProbe.EventSequence,
		FinishedSequence: beforeProbe.EventSequence,
	}); err != nil {
		t.Fatalf("reconcile recovered state: %v", err)
	}
	held := adapter.PredictiveAdmissionTelemetry()
	if !held.RouterBackpressure.Active || held.RouterBackpressure.InspectCapacity != 1 {
		t.Fatalf("recovered current state lost bounded reject projection: %+v", held.RouterBackpressure)
	}
	adapter.now = func() time.Time { return time.Unix(1, 0).Add(requestAwareRouterRejectProjectionHold) }
	recovered := adapter.PredictiveAdmissionTelemetry()
	if recovered.RouterBackpressure.Active || recovered.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("recovered Router telemetry=%+v, want OPEN at bounded hold without another request", recovered.RouterBackpressure)
	}
	if recovered.Attempts.Attempts != 1 || manager.Snapshot().Reservations != 0 || manager.Snapshot().EventSequence != beforeProbe.EventSequence {
		t.Fatalf("recovery probe mutated business accounting: telemetry=%+v manager=%+v", recovered.Attempts, manager.Snapshot())
	}
}

func TestRequestAwareAdapterConcurrentTelemetryAndAdmissionRecoversFreshOpen(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	const decisionCount = 128
	const scraperCount = 8
	const scrapesPerWorker = 256

	start := make(chan struct{})
	errors := make(chan error, decisionCount)
	var wait sync.WaitGroup
	for worker := 0; worker < scraperCount; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for scrape := 0; scrape < scrapesPerWorker; scrape++ {
				_ = adapter.PredictiveAdmissionTelemetry()
			}
		}()
	}
	for index := 0; index < decisionCount; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			decision := adapter.Decide(
				context.Background(),
				fmt.Sprintf("telemetry-race-%d", index),
				requestAwareAdapterInput(64, 16),
			)
			if decision.Reservation == nil {
				return
			}
			if !decision.Reservation.MarkForwarded() {
				errors <- fmt.Errorf("reservation %d did not commit forward", index)
				return
			}
			if !decision.Reservation.Terminate(runtimepredictive.TerminalCompleted) {
				errors <- fmt.Errorf("reservation %d did not terminate", index)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	if t.Failed() {
		return
	}

	managerSnapshot := manager.Snapshot()
	if managerSnapshot.Reservations != 0 || managerSnapshot.Virtual.Upper.ActiveKVUpper != 5_000 ||
		managerSnapshot.Virtual.Upper.DecodeSequences != 4 {
		t.Fatalf("concurrent scrape/admission lifecycle leaked virtual capacity: %+v", managerSnapshot)
	}
	adapter.now = func() time.Time { return time.Unix(1, 0).Add(requestAwareRouterRejectProjectionHold) }
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.Attempts.Attempts != decisionCount {
		t.Fatalf("business attempts=%d, want %d without scrape accounting", telemetry.Attempts.Attempts, decisionCount)
	}
	if telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("fresh post-race scrape retained a sticky Router clamp: %+v", telemetry.RouterBackpressure)
	}
}

func TestRequestAwareAdapterShadowNeverPublishesRouterClamp(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixtureWithMode(t, 8_990, 0, "shadow")
	decision := adapter.Decide(context.Background(), "telemetry-shadow", requestAwareAdapterInput(16, 0))
	if decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("setup shadow decision=%+v, want would-protect", decision)
	}
	before := manager.Snapshot()
	telemetry := adapter.PredictiveAdmissionTelemetry()
	after := manager.Snapshot()
	if telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("shadow Router telemetry=%+v, want no clamp", telemetry.RouterBackpressure)
	}
	if before.Reservations != after.Reservations || before.EventSequence != after.EventSequence || before.Virtual != after.Virtual {
		t.Fatalf("shadow telemetry changed Manager: before=%+v after=%+v", before, after)
	}
}

func TestRequestAwareAdapterProtectionLogUsesExactVerdictAndBoundsDuplicates(t *testing.T) {
	adapter, _ := newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 1, "enforce")
	now := time.Unix(2_000, 0)
	adapter.now = func() time.Time { return now }
	adapter.decisionLogInterval = time.Second
	var events []requestAwareDecisionLogEvent
	adapter.onDecision = func(event requestAwareDecisionLogEvent) {
		events = append(events, event)
	}
	for _, requestID := range []string{"log-large-1", "log-large-2"} {
		decision := adapter.Decide(context.Background(), requestID, requestAwareAdapterInput(100*1024, 100))
		if decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
			t.Fatalf("%s decision=%+v, want protection", requestID, decision)
		}
	}
	if len(events) != 1 {
		t.Fatalf("immediate duplicate protection logs=%d, want one", len(events))
	}
	now = now.Add(time.Second)
	adapter.Decide(context.Background(), "log-large-3", requestAwareAdapterInput(100*1024, 100))
	if len(events) != 2 || events[1].Suppressed != 1 {
		t.Fatalf("bounded protection events=%+v, want second event with one suppression", events)
	}
	line := requestAwareDecisionLogLine(events[1])
	for _, want := range []string{
		"event=admission_decision",
		"mode=enforce",
		"enforced=true",
		"action=size_protect",
		"reason=prefill_busy",
		"http_reason=request_size_at_pressure",
		"scope=load",
		"pressure_source=prefill",
		"pressure=1.000000",
		"selection_input_tokens=102400",
		"reserved_tokens=102512",
		"allowance_tokens=0",
		"effective_kv=131072",
		"post_admit_kv=233584",
		"remaining_kv=3643792",
		"running=4",
		"waiting=1",
		"effective_sequences=4",
		"aggregate_tps_proxy=80.000000",
		"mean_active_tps_proxy=20.000000",
		"prefill_class=weighted",
		"estimated_prefill_tokens=102400",
		"suppressed=1",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("request-aware decision log missing %q: %s", want, line)
		}
	}
	for _, forbidden := range []string{"request_id=", "model=", "prompt=", "body=", "bearer="} {
		if strings.Contains(strings.ToLower(line), forbidden) {
			t.Fatalf("request-aware decision log contains forbidden field %q: %s", forbidden, line)
		}
	}
}

func newRequestAwareAdapterTestFixture(t testing.TB, usedTokens int64, waiting int) (*requestAwarePredictiveAdapter, *runtimepredictive.Manager) {
	return newRequestAwareAdapterTestFixtureWithMode(t, usedTokens, waiting, "enforce")
}

func newRequestAwareAdapterTestFixtureWithMode(t testing.TB, usedTokens int64, waiting int, mode string) (*requestAwarePredictiveAdapter, *runtimepredictive.Manager) {
	t.Helper()
	manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{
		PhysicalKVUpper:         usedTokens,
		ActiveKVUpper:           usedTokens,
		DecodeSequences:         4 + waiting,
		PendingPrefillSequences: waiting,
		ActiveContextTokens:     usedTokens,
	})
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		HardKVLimitTokens:            8_992,
		BlockSize:                    16,
		PrefillRegularTokens:         runtimepredictive.DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       runtimepredictive.DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       runtimepredictive.DefaultRequestAwarePrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: runtimepredictive.DefaultRequestAwarePrefillAggregateBudgetTokens,
	})
	if err != nil {
		t.Fatalf("NewRequestAwarePolicy: %v", err)
	}
	adapter, err := newRequestAwarePredictiveAdapter(requestAwarePredictiveAdapterConfig{
		Manager:           manager,
		Policy:            policy,
		CapabilityProfile: requestAwareTestCapabilityProfile(10_000, 16, 8_992),
		CapabilityReason:  "test",
		Snapshot: staticRequestAwareSnapshot{input: runtimepredictive.RequestAwareInput{
			MetricsFresh:       true,
			IdentityValid:      true,
			CapacityTokens:     10_000,
			Running:            4,
			Waiting:            waiting,
			AggregateTPSProxy:  80,
			MeanActiveTPSProxy: 20,
			TPSValid:           true,
		}},
		ManifestID: "request-aware-http-test",
		BlockSize:  16,
		Mode:       mode,
		Now:        func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatalf("newRequestAwarePredictiveAdapter: %v", err)
	}
	return adapter, manager
}

func newLargeRequestAwareAdapterTestFixtureWithMode(t testing.TB, usedTokens int64, waiting int, mode string) (*requestAwarePredictiveAdapter, *runtimepredictive.Manager) {
	t.Helper()
	const capacity = int64(4 * 1024 * 1024)
	const hard = int64(3_774_864)
	manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{
		PhysicalKVUpper: usedTokens, ActiveKVUpper: usedTokens,
		DecodeSequences: 4 + waiting, PendingPrefillSequences: waiting, ActiveContextTokens: usedTokens,
	})
	policy := newLargeRequestAwareServerTestPolicy(t)
	adapter, err := newRequestAwarePredictiveAdapter(requestAwarePredictiveAdapterConfig{
		Manager: manager, Policy: policy,
		CapabilityProfile: requestAwareTestCapabilityProfile(capacity, 16, hard),
		CapabilityReason:  "test",
		Snapshot: staticRequestAwareSnapshot{input: runtimepredictive.RequestAwareInput{
			MetricsFresh: true, IdentityValid: true, CapacityTokens: capacity,
			Running: 4, Waiting: waiting, AggregateTPSProxy: 80, MeanActiveTPSProxy: 20, TPSValid: true,
		}},
		ManifestID: "request-aware-http-test", BlockSize: 16, Mode: mode,
		Now: func() time.Time { return time.Unix(1, 0) },
	})
	if err != nil {
		t.Fatalf("new large request-aware adapter: %v", err)
	}
	return adapter, manager
}

func newLargeRequestAwareServerTestPolicy(t testing.TB) *runtimepredictive.RequestAwarePolicy {
	t.Helper()
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		HardKVLimitTokens:            3_774_864,
		BlockSize:                    16,
		PrefillRegularTokens:         runtimepredictive.DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       runtimepredictive.DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       runtimepredictive.DefaultRequestAwarePrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: runtimepredictive.DefaultRequestAwarePrefillAggregateBudgetTokens,
	})
	if err != nil {
		t.Fatalf("NewRequestAwarePolicy for large capability: %v", err)
	}
	return policy
}

func reconcileRequestAwareAdapterManager(
	t testing.TB,
	manager *runtimepredictive.Manager,
	usedTokens int64,
	running int,
	waiting int,
) {
	t.Helper()
	started := manager.StartSampleWindow()
	finished := manager.EventSequence()
	if err := manager.ReconcileSample(runtimepredictive.SampleWindow{
		Observed: domainpredictive.VirtualState{
			PhysicalKVUpper:         usedTokens,
			ActiveKVUpper:           usedTokens,
			DecodeSequences:         running + waiting,
			PendingPrefillSequences: waiting,
			ActiveContextTokens:     usedTokens,
		},
		StartedSequence:  started,
		FinishedSequence: finished,
	}); err != nil {
		t.Fatalf("reconcile adapter Manager: %v", err)
	}
}

func setRequestAwareAdapterObservation(
	t testing.TB,
	adapter *requestAwarePredictiveAdapter,
	manager *runtimepredictive.Manager,
	usedTokens int64,
	running int,
	waiting int,
) {
	t.Helper()
	reconcileRequestAwareAdapterManager(t, manager, usedTokens, running, waiting)
	input := adapter.snapshot.(staticRequestAwareSnapshot).input
	input.UsedTokens = usedTokens
	input.Running = running
	input.Waiting = waiting
	input.EffectiveSequences = running
	adapter.snapshot = staticRequestAwareSnapshot{input: input}
}

func requestAwareTestCapabilityProfile(capacity, blockSize, hard int64) runtimepredictive.BackendCapabilityProfile {
	return runtimepredictive.BackendCapabilityProfile{
		SchemaVersion:                runtimepredictive.CapabilityProfileSchema,
		ModelIdentitySHA256:          "request-aware-test-model",
		KVCapacityTokens:             capacity,
		KVBlockSize:                  blockSize,
		KVHardLimitTokens:            hard,
		PrefillRegularTokens:         runtimepredictive.DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       runtimepredictive.DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       runtimepredictive.DefaultRequestAwarePrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: runtimepredictive.DefaultRequestAwarePrefillAggregateBudgetTokens,
		Source:                       runtimepredictive.CapabilityProfileExplicit,
	}
}

func requestAwareAdapterInput(inputTokens, decodeTokens int64) predictiveShadowInput {
	return predictiveShadowInput{Cost: kvadmission.Cost{
		Supported:                   true,
		EstimatedInputLow:           inputTokens,
		EstimatedInputHigh:          inputTokens,
		ApproximateInputTokens:      inputTokens,
		ApproximateInputTokensKnown: true,
		BoundedDecodeTokens:         decodeTokens,
	}}
}
