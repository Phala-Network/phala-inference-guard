package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type recordingUpperBoundCoordinator struct {
	mu                  sync.Mutex
	proposals           []runtimepredictive.UpperBoundAdmissionProposal
	recordProposals     bool
	reservations        map[string]struct{}
	identity            runtimepredictive.ModelIdentity
	onTerminate         func()
	onObserve           func()
	reject              bool
	rejectReason        domainpredictive.Reason
	virtual             domainpredictive.VirtualState
	outcomes            []runtimepredictive.SchedulerOutcome
	interference        int
	correctAfterOutcome bool
	available           bool
	managerSequence     uint64
}

type blockingUpperBoundCoordinator struct {
	*recordingUpperBoundCoordinator
	entered chan struct{}
	release chan struct{}
}

func newRecordingUpperBoundCoordinator() *recordingUpperBoundCoordinator {
	return &recordingUpperBoundCoordinator{
		recordProposals: true,
		reservations:    make(map[string]struct{}),
		identity: runtimepredictive.ModelIdentity{
			ProfileID: "approximate-test", BackendEpoch: "capacity-1000-block-4", PredictorVersion: "approx-json-v1",
		},
		available: true,
	}
}

func (c *blockingUpperBoundCoordinator) DecideUpperBoundAndReserve(now time.Time, proposal runtimepredictive.UpperBoundAdmissionProposal) runtimepredictive.CountAdmissionResult {
	close(c.entered)
	<-c.release
	return c.recordingUpperBoundCoordinator.DecideUpperBoundAndReserve(now, proposal)
}

func (c *recordingUpperBoundCoordinator) Available() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.available
}

func (c *recordingUpperBoundCoordinator) DecideUpperBoundAndReserve(now time.Time, proposal runtimepredictive.UpperBoundAdmissionProposal) runtimepredictive.CountAdmissionResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.recordProposals {
		c.proposals = append(c.proposals, proposal)
	}
	activeContext := proposal.InputTokensUpper + proposal.DecodeHorizonUpper
	physicalKV := activeContext
	if remainder := physicalKV % 4; remainder != 0 {
		physicalKV += 4 - remainder
	}
	requestComplexity := proposal.RawInputTokensHigh
	if requestComplexity < proposal.InputTokensUpper {
		requestComplexity = proposal.InputTokensUpper
	}
	prediction := runtimepredictive.SchedulerPrediction{
		Identity: c.identity, PredictedAt: now, Source: runtimepredictive.PredictionSourceStatic, Confidence: 1,
		Features: runtimepredictive.SchedulerFeatures{
			ExistingDecodeSequences:         c.virtual.DecodeSequences,
			DecodeSequences:                 c.virtual.DecodeSequences + 1,
			ExistingPendingPrefillSequences: c.virtual.PendingPrefillSequences,
			PendingPrefillSequences:         c.virtual.PendingPrefillSequences + 1,
			ExistingActiveContextTokens:     c.virtual.ActiveContextTokens,
			ExistingUncachedPrefill:         c.virtual.UncachedPrefillTokens,
			ExistingPhysicalKVUpper:         c.virtual.PhysicalKVUpper,
			ExistingActiveKVUpper:           c.virtual.ActiveKVUpper,
			RequestComplexityTokensUpper:    requestComplexity,
			ActiveContextTokens:             c.virtual.ActiveContextTokens + activeContext,
			UncachedPrefillTokens:           c.virtual.UncachedPrefillTokens + proposal.InputTokensUpper,
			AccruedLocalAdmissionLatency:    proposal.AccruedLocalAdmissionLatency,
			PhysicalKVUpper:                 c.virtual.PhysicalKVUpper + physicalKV,
			ActiveKVUpper:                   c.virtual.ActiveKVUpper + physicalKV,
			DecodeHorizonUpper:              proposal.DecodeHorizonUpper,
		},
	}
	cost := runtimepredictive.CountRequestCost{
		ManifestID:                   "model-agnostic-test",
		BackendEpoch:                 c.identity.BackendEpoch,
		InputTokens:                  proposal.InputTokensUpper,
		RequestComplexityTokensUpper: requestComplexity,
		AccruedLocalAdmissionLatency: proposal.AccruedLocalAdmissionLatency,
		PhysicalKVUpper:              physicalKV,
		ActiveKVUpper:                physicalKV,
		FuturePhysicalKVUpper:        physicalKV - proposal.InputTokensUpper,
		FutureActiveKVUpper:          physicalKV - proposal.InputTokensUpper,
		UncachedPrefillUpper:         proposal.InputTokensUpper,
		DecodeHorizonUpper:           proposal.DecodeHorizonUpper,
		DecodeSequencesUpper:         1,
		ActiveContextTokensUpper:     activeContext,
		FutureContextTokensUpper:     proposal.DecodeHorizonUpper,
		Confidence:                   proposal.Confidence,
	}
	if c.reject {
		reason := c.rejectReason
		if reason == "" {
			reason = domainpredictive.ReasonNewTPSAtRisk
		}
		return runtimepredictive.CountAdmissionResult{
			Decision:                domainpredictive.Decision{Reason: reason},
			Prediction:              prediction,
			Cost:                    cost,
			DecisionManagerSequence: c.managerSequence,
		}
	}
	c.reservations[proposal.RequestID] = struct{}{}
	return runtimepredictive.CountAdmissionResult{
		Decision:                domainpredictive.Decision{Reason: domainpredictive.ReasonFit},
		Prediction:              prediction,
		Cost:                    cost,
		Reserved:                true,
		DecisionManagerSequence: c.managerSequence,
	}
}

func (c *recordingUpperBoundCoordinator) Snapshot() runtimepredictive.CountCoordinatorSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return runtimepredictive.CountCoordinatorSnapshot{Manager: runtimepredictive.Snapshot{
		IntakeOpen:    true,
		EventSequence: c.managerSequence,
		Virtual: domainpredictive.VirtualStateInterval{
			Lower: c.virtual,
			Upper: c.virtual,
		},
	}}
}

func TestApproximatePredictiveEnforcePublishesBoundedRouterBackpressure(t *testing.T) {
	now := time.Unix(44_000, 0)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	coordinator.rejectReason = domainpredictive.ReasonExistingTPSAtRisk
	coordinator.virtual = domainpredictive.VirtualState{DecodeSequences: 1, ActiveKVUpper: 256}
	var transitions []predictiveRouterBackpressureEventKind
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "enforce",
		Now:                    func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
		OnRouterBackpressure: func(event predictiveRouterBackpressureEvent) {
			transitions = append(transitions, event.Kind)
		},
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	if got := adapter.DecideAndReserve(context.Background(), "risk-1", approximateAdapterTestInput()); got != nil {
		t.Fatal("enforce risk unexpectedly returned a reservation")
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if !telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.Reason != domainpredictive.ReasonExistingTPSAtRisk ||
		len(transitions) != 1 || transitions[0] != predictiveRouterBackpressureActivated {
		t.Fatalf("first backpressure telemetry=%+v transitions=%v", telemetry.RouterBackpressure, transitions)
	}
	now = now.Add(time.Second)
	_ = adapter.DecideAndReserve(context.Background(), "risk-2", approximateAdapterTestInput())
	telemetry = adapter.PredictiveAdmissionTelemetry()
	if telemetry.RouterBackpressure.Activations != 1 || telemetry.RouterBackpressure.Extensions != 1 ||
		len(transitions) != 2 || transitions[1] != predictiveRouterBackpressureRenewed {
		t.Fatalf("extension telemetry=%+v transitions=%v", telemetry.RouterBackpressure, transitions)
	}
	now = now.Add(3 * time.Second)
	if telemetry := adapter.PredictiveAdmissionTelemetry(); telemetry.RouterBackpressure.Active {
		t.Fatalf("backpressure remained active after expiry: %+v", telemetry.RouterBackpressure)
	}
}

func TestApproximatePredictiveInputEstimateChangesRealPreForwardDecisionWithBackendStateFixed(t *testing.T) {
	now := time.Unix(44_250, 0)
	identity := adapterTestIdentity()
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity: identity, BaseCompletionTPS: 100, PrefillTPSPenaltyPerKToken: 40,
		BaseTTFT: time.Millisecond, BaseTPOT: time.Millisecond, Confidence: 1,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity: identity, MinimumSamples: 3, MaximumSamplesPerCell: 8, MaximumCells: 32, MaxAge: time.Minute,
		LowerQuantile: 0.1, UpperQuantile: 0.9, MinimumTPSMultiplier: 0.2, MaximumTPSMultiplier: 1,
		MinimumLatencyMultiplier: 1, MaximumLatencyMultiplier: 2, CalibratedConfidence: 1,
		DecodeSequenceBucket: 1, ContextTokenBucket: 128, PrefillTokenBucket: 128, KVTokenBucket: 128,
	})
	if err != nil {
		t.Fatalf("new causality scheduler: %v", err)
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID: "adapter-test-manifest", BackendEpoch: identity.BackendEpoch, Scheduler: identity, BlockSize: 4,
		},
		ModelMaximumLength: 10_000,
		Initial: domainpredictive.VirtualState{
			PhysicalKVUpper: 1_000, ActiveKVUpper: 1_000, DecodeSequences: 1, ActiveContextTokens: 1_000,
		},
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard: 9_000, ActiveKVHard: 9_000, UserTPSTarget: 25, TPOTSLO: time.Second,
			WorkspaceRiskBudget: 1, PreemptionRiskBudget: 1, MinimumConfidence: 1,
		},
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatalf("new causality coordinator: %v", err)
	}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:  newApproximateAdapterTestCalibratorWithConfidence(t, 3, 1, 1),
		Coordinator: coordinator, Learner: scheduler, Mode: "enforce", Now: func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new causality adapter: %v", err)
	}
	small := approximateAdapterTestInput()
	small.Cost.EstimatedInputLow = 100
	small.Cost.EstimatedInputHigh = 100
	smallDecision := adapter.Decide(context.Background(), "small-fixed-state", small)
	if smallDecision.Outcome != predictiveAdmissionOutcomeForward || smallDecision.Reservation == nil {
		t.Fatalf("small fixed-state input decision = %+v, want forward", smallDecision)
	}
	if !smallDecision.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("small fixed-state reservation did not release")
	}

	large := small
	large.Cost.EstimatedInputLow = 2_000
	large.Cost.EstimatedInputHigh = 2_000
	largeDecision := adapter.Decide(context.Background(), "large-fixed-state", large)
	if largeDecision.Outcome != predictiveAdmissionOutcomeLoadProtection || largeDecision.Reservation != nil ||
		largeDecision.Reason != domainpredictive.ReasonExistingTPSAtRisk {
		t.Fatalf("larger approximate input did not change the real pre-forward decision: %+v", largeDecision)
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.Attempts.Fits != 1 || telemetry.Attempts.Risks != 1 || !telemetry.RouterBackpressure.Active ||
		telemetry.RouterBackpressure.Reason != domainpredictive.ReasonExistingTPSAtRisk || telemetry.Manager.Reservations != 0 {
		t.Fatalf("fixed-state tokenizer causality telemetry = %+v", telemetry)
	}
}

func TestApproximatePredictiveTwentySparseRequestsNeverSelfLock(t *testing.T) {
	now := time.Unix(44_500, 0)
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:  newApproximateAdapterTestCalibratorWithConfidence(t, 3, 1, 1),
		Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new sparse-flow adapter: %v", err)
	}
	for index := 0; index < 20; index++ {
		requestID := fmt.Sprintf("sparse-%02d", index)
		decision := adapter.Decide(context.Background(), requestID, approximateAdapterTestInput())
		if decision.Outcome != predictiveAdmissionOutcomeForward || decision.Reservation == nil || !decision.Reservation.MarkForwarded() {
			t.Fatalf("sparse request %d failed to make forward progress: %+v", index, decision)
		}
		if !decision.Reservation.Terminate(runtimepredictive.TerminalExpired) {
			t.Fatalf("sparse request %d did not release", index)
		}
		now = now.Add(10 * time.Second)
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.Attempts.Attempts != 20 || telemetry.Attempts.Fits != 20 || telemetry.Attempts.Risks != 0 || telemetry.Attempts.Unknown != 0 ||
		telemetry.Manager.Reservations != 0 || telemetry.DeferredOutcomes.Active != 0 || telemetry.RouterBackpressure.Active {
		t.Fatalf("twenty sparse requests left a false lock or lifecycle state: %+v", telemetry)
	}
}

func TestApproximatePredictiveBurstAcrossRouterEpisodesCannotBypassAtomicTPSForecast(t *testing.T) {
	now := time.Unix(44_625, 0)
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 60)
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:  newApproximateAdapterTestCalibratorWithConfidence(t, 3, 1, 1),
		Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new burst adapter: %v", err)
	}
	first := adapter.Decide(context.Background(), "burst-first", approximateAdapterTestInput())
	if first.Outcome != predictiveAdmissionOutcomeForward || first.Reservation == nil || !first.Reservation.MarkForwarded() {
		t.Fatalf("first burst request did not establish protected load: %+v", first)
	}
	for episode := 0; episode < 4; episode++ {
		decision := adapter.Decide(context.Background(), fmt.Sprintf("burst-retry-%d", episode), approximateAdapterTestInput())
		if decision.Outcome != predictiveAdmissionOutcomeLoadProtection || decision.Reservation != nil {
			t.Fatalf("Router episode %d bypassed the TPS forecast: %+v", episode, decision)
		}
		snapshot := coordinator.Snapshot().Manager
		if snapshot.Reservations != 1 {
			t.Fatalf("Router episode %d accumulated reservations: %+v", episode, snapshot)
		}
		now = now.Add(3 * time.Second)
	}
	if !first.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("first burst reservation did not release")
	}
	now = now.Add(3 * time.Second)
	afterDrain := adapter.Decide(context.Background(), "burst-after-drain", approximateAdapterTestInput())
	if afterDrain.Outcome != predictiveAdmissionOutcomeForward || afterDrain.Reservation == nil {
		t.Fatalf("drained burst remained locked: %+v", afterDrain)
	}
	if !afterDrain.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("post-drain burst reservation did not release")
	}
}

func TestApproximatePredictiveCloseDuringDecisionPublishesAvailabilityAndReleasesReservation(t *testing.T) {
	now := time.Unix(44_750, 0)
	coordinator := &blockingUpperBoundCoordinator{
		recordingUpperBoundCoordinator: newRecordingUpperBoundCoordinator(),
		entered:                        make(chan struct{}),
		release:                        make(chan struct{}),
	}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:  newApproximateAdapterTestCalibratorWithConfidence(t, 3, 1, 1),
		Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new close-race adapter: %v", err)
	}

	decisionDone := make(chan predictiveAdmissionDecision, 1)
	go func() {
		decisionDone <- adapter.Decide(context.Background(), "close-race", approximateAdapterTestInput())
	}()
	select {
	case <-coordinator.entered:
	case <-time.After(time.Second):
		t.Fatal("decision did not enter the coordinator")
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("close adapter during decision: %v", err)
	}
	close(coordinator.release)

	var decision predictiveAdmissionDecision
	select {
	case decision = <-decisionDone:
	case <-time.After(time.Second):
		t.Fatal("decision did not finish after coordinator release")
	}
	if decision.Outcome != predictiveAdmissionOutcomeAvailabilityProtection || decision.Reservation != nil ||
		decision.Source != runtimepredictive.PredictionSourceUnavailable {
		t.Fatalf("close-race result = %+v, want availability protection", decision)
	}
	coordinator.mu.Lock()
	remaining := len(coordinator.reservations)
	coordinator.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("close-race leaked %d coordinator reservations", remaining)
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.Attempts.Attempts != 1 || telemetry.Attempts.Unknown != 1 ||
		telemetry.Attempts.LastRejectScope != predictiveProtectionScopeAvailability ||
		!telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.Scope != predictiveProtectionScopeAvailability {
		t.Fatalf("close-race availability publication = %+v", telemetry)
	}
}

func (c *recordingUpperBoundCoordinator) ObserveUnreservedOutcome(_ runtimepredictive.SchedulerPrediction, _ runtimepredictive.TerminalCause, _ bool, outcome runtimepredictive.SchedulerOutcome) bool {
	c.mu.Lock()
	c.outcomes = append(c.outcomes, outcome)
	if c.correctAfterOutcome {
		c.reject = false
	}
	onObserve := c.onObserve
	c.mu.Unlock()
	if onObserve != nil {
		onObserve()
	}
	return true
}

func (c *recordingUpperBoundCoordinator) MarkLiveOutcomesInterfered() int {
	c.mu.Lock()
	c.interference++
	c.mu.Unlock()
	return 1
}

func (c *recordingUpperBoundCoordinator) MarkForwarded(requestID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.reservations[requestID]
	return ok
}

func (c *recordingUpperBoundCoordinator) MarkPrefillComplete(requestID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.reservations[requestID]
	return ok
}

func (c *recordingUpperBoundCoordinator) Terminate(requestID string, _ runtimepredictive.TerminalCause) bool {
	return c.TerminateWithOutcome(requestID, runtimepredictive.TerminalCompleted, nil)
}

func (c *recordingUpperBoundCoordinator) ReleaseResources(requestID string) runtimepredictive.ResourceReleaseResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.reservations[requestID]; !ok {
		return runtimepredictive.ResourceReleaseResult{}
	}
	delete(c.reservations, requestID)
	if c.onTerminate != nil {
		c.onTerminate()
	}
	return runtimepredictive.ResourceReleaseResult{Released: true}
}

func (c *recordingUpperBoundCoordinator) TerminateWithOutcome(requestID string, _ runtimepredictive.TerminalCause, _ *runtimepredictive.SchedulerOutcome) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.reservations[requestID]; !ok {
		return false
	}
	delete(c.reservations, requestID)
	if c.onTerminate != nil {
		c.onTerminate()
	}
	return true
}

func (c *recordingUpperBoundCoordinator) Proposals() []runtimepredictive.UpperBoundAdmissionProposal {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]runtimepredictive.UpperBoundAdmissionProposal(nil), c.proposals...)
}

func TestApproximatePredictiveShadowRiskFeedbackCanCorrectNextPredictionWithoutReservation(t *testing.T) {
	now := time.Unix(45_000, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	coordinator.correctAfterOutcome = true
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "shadow", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new approximate predictive adapter: %v", err)
	}

	observation := adapter.DecideAndReserve(context.Background(), "shadow-risk", approximateAdapterTestInput())
	if observation == nil {
		t.Fatal("shadow risk decision did not return a non-accounting observation record")
	}
	coordinator.mu.Lock()
	accountingReservations := len(coordinator.reservations)
	coordinator.mu.Unlock()
	if accountingReservations != 0 {
		t.Fatalf("shadow risk decision created %d accounting reservations", accountingReservations)
	}
	if !observation.MarkForwarded() {
		t.Fatal("shadow-only observation did not follow the real forwarded lifecycle")
	}
	if !observePredictiveCompletion(observation, predictiveCompletionObservation{
		PromptTokens: 70, CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
		BackendMeanITL: 10 * time.Millisecond,
	}) {
		t.Fatal("shadow-only observation did not accept qualified completion usage")
	}
	if !observation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("shadow-only observation did not terminate")
	}
	coordinator.mu.Lock()
	outcomes := len(coordinator.outcomes)
	coordinator.mu.Unlock()
	if outcomes != 1 {
		t.Fatalf("qualified shadow outcomes = %d, want 1", outcomes)
	}
	if snapshot := calibrator.Snapshot(now); snapshot.SamplesAccepted != 1 || snapshot.SamplesStored != 1 {
		t.Fatalf("shadow-only prompt usage did not train the future size estimate: %+v", snapshot)
	}

	next := adapter.DecideAndReserve(context.Background(), "after-shadow-feedback", approximateAdapterTestInput())
	if next == nil {
		t.Fatal("qualified shadow feedback did not allow the corrected next prediction")
	}
	if !next.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("corrected next reservation did not release")
	}
}

func TestApproximatePredictiveShadowRiskPublishesAnonymousPendingPrefillWithoutResourceReservation(t *testing.T) {
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	coordinator.rejectReason = domainpredictive.ReasonExistingTPSAtRisk
	coordinator.managerSequence = 17
	coordinator.virtual = domainpredictive.VirtualState{
		PhysicalKVUpper:     1_000,
		ActiveKVUpper:       1_000,
		DecodeSequences:     1,
		ActiveContextTokens: 1_000,
	}
	shadowPrefills := newPredictiveShadowPendingPrefillStore(4)
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "shadow",
		ShadowObservationLimit: 4,
		ShadowPendingPrefills:  shadowPrefills,
	})
	if err != nil {
		t.Fatalf("new shadow-prefill adapter: %v", err)
	}

	decision := adapter.Decide(context.Background(), "private-request-id", approximateAdapterTestInput())
	if decision.Outcome != predictiveAdmissionOutcomeForward || decision.Reservation == nil {
		t.Fatalf("shadow risk decision = %+v", decision)
	}
	if snapshot := shadowPrefills.Snapshot(); snapshot.Count != 0 || snapshot.Tokens != 0 {
		t.Fatalf("unforwarded shadow risk appeared as pending prefill: %+v", snapshot)
	}
	if !decision.Reservation.MarkForwarded() {
		t.Fatal("shadow risk did not enter the forwarded lifecycle")
	}
	pending := shadowPrefills.Snapshot()
	if pending.Count != 1 || pending.Tokens <= 0 || !pending.FeaturesValid ||
		pending.Features.ExistingDecodeSequences != 1 || pending.Features.DecodeSequences != 2 ||
		pending.DecisionManagerSequence != 17 {
		t.Fatalf("forwarded shadow risk was not published as one anonymous pending prefill: %+v", pending)
	}
	coordinator.mu.Lock()
	accountingReservations := len(coordinator.reservations)
	coordinator.mu.Unlock()
	if accountingReservations != 0 || coordinator.Snapshot().Manager.Reservations != 0 {
		t.Fatalf("shadow pending-prefill observation entered resource accounting: local=%d manager=%+v", accountingReservations, coordinator.Snapshot().Manager)
	}
	if !decision.Reservation.MarkPrefillComplete() {
		t.Fatal("shadow pending prefill did not complete")
	}
	if snapshot := shadowPrefills.Snapshot(); snapshot.Count != 0 || snapshot.Tokens != 0 || snapshot.FeaturesValid {
		t.Fatalf("semantic output left a shadow pending-prefill observation: %+v", snapshot)
	}
	if !decision.Reservation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("shadow observation did not terminate after semantic output")
	}
}

func TestApproximatePredictiveEnforceRiskCreatesNoShadowObservation(t *testing.T) {
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce",
	})
	if err != nil {
		t.Fatalf("new enforce predictive adapter: %v", err)
	}
	if reservation := adapter.DecideAndReserve(context.Background(), "enforce-risk", approximateAdapterTestInput()); reservation != nil {
		t.Fatal("enforce risk decision created a forwarded observation record")
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.ShadowObservations.Active != 0 || telemetry.ShadowObservations.Created != 0 {
		t.Fatalf("enforce mode shadow observations = %+v, want zero", telemetry.ShadowObservations)
	}
	if snapshot := calibrator.Snapshot(time.Now()); snapshot.SamplesAccepted != 0 || snapshot.SamplesStored != 0 {
		t.Fatalf("enforce rejection trained size calibration: %+v", snapshot)
	}
}

func TestApproximatePredictiveShadowObservationBoundAndCloseDoNotCreateResourcePressure(t *testing.T) {
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "shadow", ShadowObservationLimit: 1,
	})
	if err != nil {
		t.Fatalf("new bounded shadow adapter: %v", err)
	}
	first := adapter.DecideAndReserve(context.Background(), "held-open-first", approximateAdapterTestInput())
	if first == nil {
		t.Fatal("first shadow observation was not retained")
	}
	if second := adapter.DecideAndReserve(context.Background(), "held-open-second", approximateAdapterTestInput()); second != nil {
		t.Fatal("shadow observation bound retained a second held-open record")
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.ShadowObservations.Active != 1 || telemetry.ShadowObservations.Created != 1 || telemetry.ShadowObservations.Dropped != 1 {
		t.Fatalf("bounded shadow telemetry = %+v", telemetry.ShadowObservations)
	}
	coordinator.mu.Lock()
	accounting := len(coordinator.reservations)
	coordinator.mu.Unlock()
	if accounting != 0 {
		t.Fatalf("held-open shadow observations created %d accounting reservations", accounting)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("close bounded shadow adapter: %v", err)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().ShadowObservations; snapshot.Active != 0 {
		t.Fatalf("close left shadow observations: %+v", snapshot)
	}
	if first.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("closed shadow observation terminated twice")
	}
}

func TestApproximatePredictiveLaterShadowForwardCensorsEarlierObservation(t *testing.T) {
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "shadow", ShadowObservationLimit: 4,
	})
	if err != nil {
		t.Fatalf("new causal shadow adapter: %v", err)
	}
	first := adapter.DecideAndReserve(context.Background(), "shadow-first", approximateAdapterTestInput())
	second := adapter.DecideAndReserve(context.Background(), "shadow-second", approximateAdapterTestInput())
	if first == nil || second == nil || !first.MarkForwarded() || !second.MarkForwarded() {
		t.Fatal("causal shadow observations did not follow forwarding")
	}
	if !observePredictiveCompletion(first, predictiveCompletionObservation{
		PromptTokens: 70, CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
		BackendMeanITL: 10 * time.Millisecond,
	}) || !first.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("interfered first shadow observation did not complete")
	}
	coordinator.mu.Lock()
	outcomes := append([]runtimepredictive.SchedulerOutcome(nil), coordinator.outcomes...)
	coordinator.mu.Unlock()
	if len(outcomes) != 1 || !outcomes[0].Censored {
		t.Fatalf("causal shadow outcomes = %+v, want first censored", outcomes)
	}
	if !second.Terminate(runtimepredictive.TerminalClientCancelled) {
		t.Fatal("second shadow observation did not terminate")
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().ShadowObservations; snapshot.Active != 0 || snapshot.Censored != 1 {
		t.Fatalf("causal shadow telemetry = %+v", snapshot)
	}
}

func TestApproximatePredictiveUpstreamTerminalReleasesBeforeQualifiedOutcome(t *testing.T) {
	now := time.Unix(49_000, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new early-release adapter: %v", err)
	}
	reservation := adapter.DecideAndReserve(context.Background(), "early-release", approximateAdapterTestInput())
	if reservation == nil || !reservation.MarkForwarded() {
		t.Fatal("early-release reservation was not forwarded")
	}
	if !observePredictiveCompletion(reservation, predictiveCompletionObservation{
		PromptTokens: 70, CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
		BackendMeanITL: 10 * time.Millisecond,
	}) {
		t.Fatal("early-release reservation did not retain completion usage")
	}
	releaser, ok := reservation.(predictiveResourceReleaser)
	if !ok || !releaser.ReleaseResources() {
		t.Fatal("valid upstream terminal did not release resource accounting")
	}
	if releaser.ReleaseResources() {
		t.Fatal("resource release was not idempotent")
	}
	coordinator.mu.Lock()
	accounting := len(coordinator.reservations)
	outcomesBeforeTerminal := len(coordinator.outcomes)
	coordinator.mu.Unlock()
	if accounting != 0 || outcomesBeforeTerminal != 0 {
		t.Fatalf("early resource release accounting/outcomes = %d/%d, want 0/0", accounting, outcomesBeforeTerminal)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 1 || snapshot.Released != 1 || snapshot.Qualified != 0 {
		t.Fatalf("early resource release telemetry = %+v", snapshot)
	}
	if snapshot := calibrator.Snapshot(now); snapshot.SamplesAccepted != 0 {
		t.Fatalf("resource release trained input-size feedback before handler completion: %+v", snapshot)
	}

	if !reservation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("qualified deferred outcome did not terminate")
	}
	if reservation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("deferred terminal outcome was applied twice")
	}
	coordinator.mu.Lock()
	outcomesAfterTerminal := len(coordinator.outcomes)
	coordinator.mu.Unlock()
	if outcomesAfterTerminal != 1 {
		t.Fatalf("qualified deferred scheduler outcomes = %d, want exactly 1", outcomesAfterTerminal)
	}
	if snapshot := calibrator.Snapshot(now); snapshot.SamplesAccepted != 1 {
		t.Fatalf("qualified deferred input-size outcomes = %+v, want exactly one accepted sample", snapshot)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Terminated != 1 || snapshot.Qualified != 1 || snapshot.Censored != 0 {
		t.Fatalf("qualified deferred terminal telemetry = %+v", snapshot)
	}
}

func TestApproximatePredictiveDeferredOutcomeBoundDropsLearningNotResourceRelease(t *testing.T) {
	now := time.Unix(49_500, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", DeferredOutcomeLimit: 1,
		Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new bounded deferred adapter: %v", err)
	}
	reservations := make([]predictiveShadowReservation, 0, 2)
	for index := 0; index < 2; index++ {
		requestID := "bounded-deferred-" + string(rune('a'+index))
		reservation := adapter.DecideAndReserve(context.Background(), requestID, approximateAdapterTestInput())
		if reservation == nil || !reservation.MarkForwarded() {
			t.Fatalf("bounded deferred reservation %d was not forwarded", index)
		}
		if !observePredictiveCompletion(reservation, predictiveCompletionObservation{
			PromptTokens: int64(70 + index), CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
			BackendMeanITL: 10 * time.Millisecond,
		}) {
			t.Fatalf("bounded deferred reservation %d did not retain completion usage", index)
		}
		if !reservation.(predictiveResourceReleaser).ReleaseResources() {
			t.Fatalf("bounded deferred reservation %d did not release resources", index)
		}
		reservations = append(reservations, reservation)
	}
	coordinator.mu.Lock()
	accounting := len(coordinator.reservations)
	coordinator.mu.Unlock()
	if accounting != 0 {
		t.Fatalf("bounded deferred outcomes retained %d accounting reservations", accounting)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 1 || snapshot.Released != 2 || snapshot.Dropped != 1 {
		t.Fatalf("bounded deferred release telemetry = %+v", snapshot)
	}
	if !reservations[0].Terminate(runtimepredictive.TerminalCompleted) || !reservations[1].Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("bounded deferred outcomes did not finish idempotently")
	}
	coordinator.mu.Lock()
	outcomes := len(coordinator.outcomes)
	coordinator.mu.Unlock()
	if outcomes != 1 {
		t.Fatalf("bounded deferred outcomes trained %d scheduler samples, want one retained sample", outcomes)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Terminated != 1 || snapshot.Qualified != 1 || snapshot.Dropped != 1 {
		t.Fatalf("bounded deferred terminal telemetry = %+v", snapshot)
	}
}

func TestApproximatePredictiveDeferredDisconnectIsCensoredWithoutLearning(t *testing.T) {
	now := time.Unix(49_750, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new disconnected deferred adapter: %v", err)
	}
	reservation := adapter.DecideAndReserve(context.Background(), "deferred-disconnect", approximateAdapterTestInput())
	if reservation == nil || !reservation.MarkForwarded() || !reservation.(predictiveResourceReleaser).ReleaseResources() {
		t.Fatal("disconnected deferred reservation did not release resource accounting")
	}
	if !reservation.Terminate(runtimepredictive.TerminalClientDisconnected) {
		t.Fatal("disconnected deferred outcome did not terminate")
	}
	coordinator.mu.Lock()
	outcomes := len(coordinator.outcomes)
	coordinator.mu.Unlock()
	if outcomes != 0 || calibrator.Snapshot(now).SamplesAccepted != 0 {
		t.Fatalf("disconnected deferred outcome trained scheduler/input size: %d/%+v", outcomes, calibrator.Snapshot(now))
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Terminated != 1 || snapshot.Censored != 1 || snapshot.Qualified != 0 {
		t.Fatalf("disconnected deferred telemetry = %+v", snapshot)
	}
}

func TestApproximatePredictiveCloseCensorsDeferredOutcomeWithoutLearning(t *testing.T) {
	now := time.Unix(49_875, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new close-deferred adapter: %v", err)
	}
	reservation := adapter.DecideAndReserve(context.Background(), "deferred-close", approximateAdapterTestInput())
	if reservation == nil || !reservation.MarkForwarded() {
		t.Fatal("close-deferred reservation was not forwarded")
	}
	if !observePredictiveCompletion(reservation, predictiveCompletionObservation{
		PromptTokens: 70, CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
		BackendMeanITL: 10 * time.Millisecond,
	}) || !reservation.(predictiveResourceReleaser).ReleaseResources() {
		t.Fatal("close-deferred reservation did not reach bounded deferred state")
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("close adapter with deferred outcome: %v", err)
	}
	if reservation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("handler completion trained a deferred outcome after adapter close")
	}
	coordinator.mu.Lock()
	outcomes := len(coordinator.outcomes)
	accounting := len(coordinator.reservations)
	coordinator.mu.Unlock()
	if outcomes != 0 || accounting != 0 || calibrator.Snapshot(now).SamplesAccepted != 0 {
		t.Fatalf("close-deferred learned/leaked scheduler, accounting, or size state: %d/%d/%+v", outcomes, accounting, calibrator.Snapshot(now))
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Released != 1 || snapshot.Terminated != 1 || snapshot.Censored != 1 || snapshot.Qualified != 0 {
		t.Fatalf("close-deferred telemetry = %+v", snapshot)
	}
}

func TestApproximatePredictiveCloseWaitsForRegisteredDeferredLearning(t *testing.T) {
	now := time.Unix(49_880, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	observeStarted := make(chan struct{})
	releaseObserve := make(chan struct{})
	coordinator.onObserve = func() {
		close(observeStarted)
		<-releaseObserve
	}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new close-learning adapter: %v", err)
	}
	reservation := adapter.DecideAndReserve(context.Background(), "deferred-close-learning", approximateAdapterTestInput())
	if reservation == nil || !reservation.MarkForwarded() || !observePredictiveCompletion(reservation, predictiveCompletionObservation{
		PromptTokens: 70, CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
		BackendMeanITL: 10 * time.Millisecond,
	}) || !reservation.(predictiveResourceReleaser).ReleaseResources() {
		t.Fatal("close-learning reservation did not reach deferred state")
	}
	terminalDone := make(chan bool, 1)
	go func() {
		terminalDone <- reservation.Terminate(runtimepredictive.TerminalCompleted)
	}()
	select {
	case <-observeStarted:
	case <-time.After(time.Second):
		t.Fatal("deferred learning did not start")
	}
	closeDone := make(chan error, 2)
	go func() {
		closeDone <- adapter.Close()
	}()
	go func() {
		closeDone <- adapter.Close()
	}()
	select {
	case err := <-closeDone:
		t.Fatalf("concurrent adapter close returned before registered learning completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseObserve)
	select {
	case terminated := <-terminalDone:
		if !terminated {
			t.Fatal("registered deferred terminal outcome was rejected")
		}
	case <-time.After(time.Second):
		t.Fatal("deferred terminal did not finish after observer release")
	}
	for index := 0; index < 2; index++ {
		select {
		case err := <-closeDone:
			if err != nil {
				t.Fatalf("close adapter %d after registered learning: %v", index, err)
			}
		case <-time.After(time.Second):
			t.Fatalf("adapter close %d did not finish after registered learning", index)
		}
	}
	coordinator.mu.Lock()
	outcomes := len(coordinator.outcomes)
	accounting := len(coordinator.reservations)
	coordinator.mu.Unlock()
	if outcomes != 1 || accounting != 0 || calibrator.Snapshot(now).SamplesAccepted != 1 {
		t.Fatalf("registered close-learning final state = %d/%d/%+v", outcomes, accounting, calibrator.Snapshot(now))
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Terminated != 1 || snapshot.Qualified != 1 {
		t.Fatalf("registered close-learning telemetry = %+v", snapshot)
	}
}

func TestApproximatePredictiveConcurrentResourceReleaseAndTerminalConverge(t *testing.T) {
	now := time.Unix(49_900, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new release-terminal race adapter: %v", err)
	}
	const rounds = 64
	for index := 0; index < rounds; index++ {
		requestID := fmt.Sprintf("release-terminal-race-%d", index)
		reservation := adapter.DecideAndReserve(context.Background(), requestID, approximateAdapterTestInput())
		if reservation == nil || !reservation.MarkForwarded() || !observePredictiveCompletion(reservation, predictiveCompletionObservation{
			PromptTokens: 70, CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
			BackendMeanITL: 10 * time.Millisecond,
		}) {
			t.Fatalf("race reservation %d setup failed", index)
		}
		start := make(chan struct{})
		results := make(chan bool, 2)
		go func() {
			<-start
			results <- reservation.(predictiveResourceReleaser).ReleaseResources()
		}()
		go func() {
			<-start
			results <- reservation.Terminate(runtimepredictive.TerminalCompleted)
		}()
		close(start)
		first, second := <-results, <-results
		if !first && !second {
			t.Fatalf("race reservation %d neither released nor terminated", index)
		}
		if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 {
			t.Fatalf("race reservation %d left deferred state: %+v", index, snapshot)
		}
		coordinator.mu.Lock()
		accounting := len(coordinator.reservations)
		coordinator.mu.Unlock()
		if accounting != 0 {
			t.Fatalf("race reservation %d left manager accounting: %d", index, accounting)
		}
		now = now.Add(time.Second)
	}
	if snapshot := calibrator.Snapshot(now); snapshot.SamplesAccepted != rounds {
		t.Fatalf("release-terminal race trained %d size samples, want exactly %d", snapshot.SamplesAccepted, rounds)
	}
}

func TestApproximatePredictiveAdapterLearnsOnlyAfterTerminalReleaseForNextRequest(t *testing.T) {
	now := time.Unix(50_000, 0)
	calibrator := newApproximateAdapterTestCalibrator(t, 2)
	coordinator := newRecordingUpperBoundCoordinator()
	releaseObservedBeforeLearning := true
	coordinator.onTerminate = func() {
		if calibrator.Snapshot(now).SamplesAccepted != 0 && len(coordinator.proposals) == 1 {
			releaseObservedBeforeLearning = false
		}
	}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new approximate predictive adapter: %v", err)
	}

	for index, promptTokens := range []int64{60, 65} {
		requestID := "learn-" + string(rune('a'+index))
		reservation := adapter.DecideAndReserve(context.Background(), requestID, approximateAdapterTestInput())
		if reservation == nil || !reservation.MarkForwarded() {
			t.Fatalf("training reservation %d was not forwarded", index)
		}
		if !observePredictiveCompletion(reservation, predictiveCompletionObservation{
			PromptTokens: promptTokens, CompletionTokens: 5,
			ElapsedSinceRequest: 100 * time.Millisecond, BackendMeanITL: 10 * time.Millisecond,
		}) {
			t.Fatalf("training completion %d was not accepted", index)
		}
		if !reservation.Terminate(runtimepredictive.TerminalCompleted) {
			t.Fatalf("training reservation %d did not terminate", index)
		}
		now = now.Add(time.Second)
	}
	if !releaseObservedBeforeLearning {
		t.Fatal("input-size learning ran before terminal reservation release")
	}

	third := adapter.DecideAndReserve(context.Background(), "learn-c", approximateAdapterTestInput())
	if third == nil {
		t.Fatal("learned request was not reserved")
	}
	proposals := coordinator.Proposals()
	if len(proposals) != 3 {
		t.Fatalf("proposal count = %d, want 3", len(proposals))
	}
	if proposals[0].InputTokensUpper != 100 || proposals[1].InputTokensUpper != 100 {
		t.Fatalf("feedback changed its own or an immature estimate: %+v", proposals[:2])
	}
	if proposals[2].InputTokensUpper >= 100 || proposals[2].InputTokensUpper < 65 {
		t.Fatalf("mature learned upper = %d, want [65,100)", proposals[2].InputTokensUpper)
	}
	for index, proposal := range proposals {
		if proposal.RawInputTokensHigh != 100 {
			t.Fatalf("proposal %d raw QoS complexity = %d, want stable 100", index, proposal.RawInputTokensHigh)
		}
	}
	if !third.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("learned request did not release")
	}
}

func TestApproximatePredictiveAdapterUsesHintAsNextRequestSizeReference(t *testing.T) {
	now := time.Unix(55_000, 0)
	calibrator := newApproximateAdapterTestCalibrator(t, 2)
	coordinator := newRecordingUpperBoundCoordinator()
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new hinted approximate adapter: %v", err)
	}

	trainingInput := approximateAdapterTestInput()
	trainingInput.Cost.EstimatedInputLow = 50
	trainingInput.Cost.EstimatedInputHigh = 400
	trainingInput.Cost.ApproximateInputTokens = 100
	trainingInput.Cost.ApproximateInputTokensKnown = true
	for index := 0; index < 2; index++ {
		requestID := fmt.Sprintf("hint-train-%d", index)
		reservation := adapter.DecideAndReserve(context.Background(), requestID, trainingInput)
		if reservation == nil || !reservation.MarkForwarded() || !observePredictiveCompletion(reservation, predictiveCompletionObservation{
			PromptTokens: 100, CompletionTokens: 5, ElapsedSinceRequest: 100 * time.Millisecond, BackendMeanITL: 10 * time.Millisecond,
		}) || !reservation.Terminate(runtimepredictive.TerminalCompleted) {
			t.Fatalf("hint training request %d did not complete", index)
		}
		now = now.Add(time.Second)
	}

	small := trainingInput
	small.Cost.ApproximateInputTokens = 50
	smallReservation := adapter.DecideAndReserve(context.Background(), "hint-small", small)
	if smallReservation == nil {
		t.Fatal("hinted small request was not reserved")
	}
	large := trainingInput
	large.Cost.ApproximateInputTokens = 200
	largeReservation := adapter.DecideAndReserve(context.Background(), "hint-large", large)
	if largeReservation == nil {
		t.Fatal("hinted large request was not reserved")
	}

	proposals := coordinator.Proposals()
	if len(proposals) != 4 || proposals[0].InputTokensUpper != 400 || proposals[1].InputTokensUpper != 400 {
		t.Fatalf("hint feedback changed its producing requests: %+v", proposals)
	}
	if proposals[2].InputTokensUpper < 50 || proposals[2].InputTokensUpper >= 100 || proposals[3].InputTokensUpper <= 200 || proposals[3].InputTokensUpper >= 400 {
		t.Fatalf("same raw interval did not use hint to separate later request sizes: small=%+v large=%+v", proposals[2], proposals[3])
	}
	if snapshot := calibrator.Snapshot(now); snapshot.HintSamplesStored != 2 || !snapshot.LastHintUsed || snapshot.LastHint != 200 {
		t.Fatalf("hint path telemetry = %+v", snapshot)
	}
	if !smallReservation.Terminate(runtimepredictive.TerminalExpired) || !largeReservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("hinted reservations did not release")
	}
}

func TestApproximatePredictiveAdapterMissingPromptUsageDoesNotPoisonColdProgress(t *testing.T) {
	now := time.Unix(60_000, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new approximate predictive adapter: %v", err)
	}
	reservation := adapter.DecideAndReserve(context.Background(), "missing-usage", approximateAdapterTestInput())
	if reservation == nil || !reservation.MarkForwarded() {
		t.Fatal("missing-usage request was not forwarded")
	}
	if !observePredictiveCompletion(reservation, predictiveCompletionObservation{
		CompletionTokens: 5, ElapsedSinceRequest: 100 * time.Millisecond,
		BackendMeanITL: 10 * time.Millisecond,
	}) {
		t.Fatal("completion-only usage was not accepted for TPS observation")
	}
	if !reservation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("missing-usage request did not terminate")
	}
	if snapshot := calibrator.Snapshot(now); snapshot.SamplesAccepted != 0 || snapshot.SamplesRejected != 0 || snapshot.SamplesStored != 0 {
		t.Fatalf("missing prompt usage poisoned input learner: %+v", snapshot)
	}
	next := adapter.DecideAndReserve(context.Background(), "after-missing", approximateAdapterTestInput())
	if next == nil {
		t.Fatal("cold-safe request was locked out after missing usage")
	}
	proposals := coordinator.Proposals()
	if got := proposals[len(proposals)-1].InputTokensUpper; got != 100 {
		t.Fatalf("post-missing estimate = %d, want cold upper 100", got)
	}
	if !next.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("post-missing request did not release")
	}
}

func TestApproximatePredictiveConcurrentLifecycleAndCloseCompleteWithoutLeaks(t *testing.T) {
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 3, 1, 1)
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce",
	})
	if err != nil {
		t.Fatalf("new concurrent lifecycle adapter: %v", err)
	}
	for index := 0; index < 4; index++ {
		if reservation := adapter.DecideAndReserve(context.Background(), fmt.Sprintf("seed-%d", index), approximateAdapterTestInput()); reservation == nil {
			t.Fatalf("seed reservation %d was rejected", index)
		}
	}

	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < 64; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			reservation := adapter.DecideAndReserve(context.Background(), fmt.Sprintf("concurrent-%d", index), approximateAdapterTestInput())
			if reservation == nil {
				return
			}
			reservation.MarkForwarded()
			reservation.MarkPrefillComplete()
			observePredictiveSemanticTTFT(reservation, 10*time.Millisecond)
			observePredictiveCompletion(reservation, predictiveCompletionObservation{
				PromptTokens: 75, CompletionTokens: 5, ElapsedSinceRequest: 50 * time.Millisecond,
				BackendMeanITL: 10 * time.Millisecond,
			})
			if index%2 == 0 {
				if releaser, ok := reservation.(predictiveResourceReleaser); ok {
					releaser.ReleaseResources()
				}
			}
			cause := runtimepredictive.TerminalCompleted
			if index%3 == 0 {
				cause = runtimepredictive.TerminalClientCancelled
			}
			reservation.Terminate(cause)
		}(index)
	}
	workers.Add(3)
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 128; index++ {
			coordinator.InvalidateLearning()
			calibrator.InvalidateLearning()
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 128; index++ {
			_ = adapter.PredictiveAdmissionTelemetry()
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		_ = adapter.Close()
	}()
	close(start)
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("concurrent decide/observe/terminate/invalidate/telemetry/close did not complete")
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("idempotent close after concurrent lifecycle: %v", err)
	}
	adapter.mu.Lock()
	remaining := len(adapter.reservations)
	deferred := len(adapter.deferredOutcomes)
	adapter.mu.Unlock()
	if remaining != 0 || deferred != 0 || coordinator.Snapshot().Manager.Reservations != 0 {
		t.Fatalf("concurrent lifecycle left adapter/deferred/coordinator reservations = %d/%d/%d", remaining, deferred, coordinator.Snapshot().Manager.Reservations)
	}
	if decision := adapter.Decide(context.Background(), "after-close", approximateAdapterTestInput()); decision.Outcome != predictiveAdmissionOutcomeAvailabilityProtection || decision.Reservation != nil || decision.Source != runtimepredictive.PredictionSourceUnavailable {
		t.Fatalf("closed adapter decision = %+v, want availability protection", decision)
	}
}

func TestApproximatePredictiveLowFlowRecoversAfterTPSDrainWaitingStaleAndPreemption(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(70_000, 0)}
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 100)
	calibrator, err := runtimepredictive.NewInputSizeCalibrator(runtimepredictive.InputSizeCalibratorConfig{
		EstimatorVersion: "approx-json-v1", MinimumSamples: 3,
		MaximumSamplesPerClass: 8, MaxAge: time.Minute,
		UpperQuantile: 0.9, SafetyMargin: 1.10,
		MinimumMultiplier: 0.25, MaximumMultiplier: 4,
		ColdConfidence: 1, LearnedConfidence: 1,
	})
	if err != nil {
		t.Fatalf("new recovery calibrator: %v", err)
	}
	fixture := &observerMetricsFixture{body: observerMetrics(1_000, 0, 0, 0, 0, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) {
		t.Fatal("initial coherent idle metrics did not open low-flow intake")
	}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Upstream: observer, Mode: "enforce", Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("new recovery adapter: %v", err)
	}

	first := adapter.DecideAndReserve(context.Background(), "low-flow-first", approximateAdapterTestInput())
	if first == nil {
		t.Fatal("first cold-safe low-flow request was rejected")
	}
	if concurrent := adapter.DecideAndReserve(context.Background(), "tps-risk", approximateAdapterTestInput()); concurrent != nil {
		t.Fatal("second cold request bypassed the TPS protection")
	}
	if !first.Terminate(runtimepredictive.TerminalClientCancelled) {
		t.Fatal("first low-flow request did not release")
	}
	postDrain := adapter.DecideAndReserve(context.Background(), "post-tps-drain", approximateAdapterTestInput())
	if postDrain == nil || !postDrain.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("TPS-risk rejection remained sticky after reservations drained")
	}

	clock.Advance(time.Second)
	fixture.Set(observerMetrics(1_000, 0, 0, 1, 0, true))
	observer.poll(context.Background())
	if blocked := adapter.DecideAndReserve(context.Background(), "while-waiting", approximateAdapterTestInput()); blocked != nil {
		t.Fatal("request was admitted while vLLM waiting was non-zero")
	}
	clock.Advance(time.Second)
	fixture.Set(observerMetrics(1_000, 0, 0, 0, 0, true))
	observer.poll(context.Background())
	postWaiting := adapter.DecideAndReserve(context.Background(), "post-waiting", approximateAdapterTestInput())
	if postWaiting == nil || !postWaiting.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("waiting recovery did not admit the next cold-safe request")
	}

	clock.Advance(observer.maximumAge + time.Nanosecond)
	if stale := adapter.DecideAndReserve(context.Background(), "while-stale", approximateAdapterTestInput()); stale != nil {
		t.Fatal("request was admitted from stale metrics")
	}
	observer.poll(context.Background())
	postFreshness := adapter.DecideAndReserve(context.Background(), "post-freshness", approximateAdapterTestInput())
	if postFreshness == nil || !postFreshness.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("first coherent post-stale snapshot did not restore cold-safe progress")
	}

	clock.Advance(time.Second)
	fixture.Set(observerMetrics(1_000, 0, 0, 0, 1, true))
	observer.poll(context.Background())
	if cooling := adapter.DecideAndReserve(context.Background(), "during-cooldown", approximateAdapterTestInput()); cooling != nil {
		t.Fatal("request was admitted during preemption cooldown")
	}
	clock.Advance(observer.preemptionCooldown)
	postCooldown := adapter.DecideAndReserve(context.Background(), "post-cooldown", approximateAdapterTestInput())
	if postCooldown == nil || !postCooldown.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("preemption cooldown remained sticky after its exact boundary")
	}

	clock.Advance(time.Second)
	fixture.Set(observerMetrics(2_000, 0, 0, 0, 1, true))
	observer.poll(context.Background())
	if drifted := adapter.DecideAndReserve(context.Background(), "after-capacity-drift", approximateAdapterTestInput()); drifted != nil {
		t.Fatal("capacity drift reused an old coordinator instead of permanent quarantine")
	}
	if snapshot := coordinator.Snapshot().Manager; snapshot.IntakeOpen || snapshot.Reservations != 0 {
		t.Fatalf("final low-flow recovery state = %+v, want quarantined intake with zero reservations", snapshot)
	}
}

func TestApproximatePredictiveAllTerminalCausesReleaseExactlyOnce(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(80_000, 0)}
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1)
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("new terminal adapter: %v", err)
	}
	causes := []runtimepredictive.TerminalCause{
		runtimepredictive.TerminalCompleted,
		runtimepredictive.TerminalLocalQoSReject,
		runtimepredictive.TerminalClientCancelled,
		runtimepredictive.TerminalClientDisconnected,
		runtimepredictive.TerminalUpstreamFailure,
		runtimepredictive.TerminalTimeout,
		runtimepredictive.TerminalExpired,
	}
	for index, cause := range causes {
		requestID := "terminal-" + string(rune('a'+index))
		reservation := adapter.DecideAndReserve(context.Background(), requestID, approximateAdapterTestInput())
		if reservation == nil || !reservation.MarkForwarded() {
			t.Fatalf("%s reservation did not forward", cause)
		}
		if cause == runtimepredictive.TerminalCompleted {
			if !reservation.MarkPrefillComplete() || !observePredictiveSemanticTTFT(reservation, 20*time.Millisecond) {
				t.Fatal("completed reservation did not record prefill lifecycle")
			}
			if !observePredictiveCompletion(reservation, predictiveCompletionObservation{
				PromptTokens: 80, CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
				BackendMeanITL: 10 * time.Millisecond,
			}) {
				t.Fatal("completed reservation did not record qualified usage")
			}
		}
		if !reservation.Terminate(cause) {
			t.Fatalf("%s did not terminate", cause)
		}
		if reservation.Terminate(cause) {
			t.Fatalf("%s terminated twice", cause)
		}
		if snapshot := coordinator.Snapshot().Manager; snapshot.Reservations != 0 {
			t.Fatalf("%s leaked reservation: %+v", cause, snapshot)
		}
		clock.Advance(time.Second)
	}
	if snapshot := calibrator.Snapshot(clock.Now()); snapshot.SamplesAccepted != 1 || snapshot.SamplesRejected != 0 || snapshot.SamplesStored != 1 {
		t.Fatalf("terminal outcomes poisoned input-size learning: %+v", snapshot)
	}
}

func newApproximateAdapterTestCalibrator(t testing.TB, minimumSamples int) *runtimepredictive.InputSizeCalibrator {
	t.Helper()
	return newApproximateAdapterTestCalibratorWithConfidence(t, minimumSamples, 0.90, 0.98)
}

func newApproximateAdapterTestCalibratorWithConfidence(t testing.TB, minimumSamples int, coldConfidence, learnedConfidence float64) *runtimepredictive.InputSizeCalibrator {
	t.Helper()
	calibrator, err := runtimepredictive.NewInputSizeCalibrator(runtimepredictive.InputSizeCalibratorConfig{
		EstimatorVersion: "approx-json-v1", MinimumSamples: minimumSamples,
		MaximumSamplesPerClass: 8, MaxAge: time.Minute,
		UpperQuantile: 0.9, SafetyMargin: 1.10,
		MinimumMultiplier: 0.25, MaximumMultiplier: 4,
		ColdConfidence: coldConfidence, LearnedConfidence: learnedConfidence,
	})
	if err != nil {
		t.Fatalf("new input-size calibrator: %v", err)
	}
	return calibrator
}

func approximateAdapterTestInput() predictiveShadowInput {
	return predictiveShadowInput{
		Path: "/v1/chat/completions",
		Cost: kvadmission.Cost{
			Supported: true, EstimatedInputLow: 50, EstimatedInputHigh: 100,
			MaxOutputTokens: 8, HasMaxOutputTokens: true, BoundedDecodeTokens: 8,
		},
	}
}

var benchmarkPredictiveReservation predictiveShadowReservation

func BenchmarkApproximatePredictiveAdmissionLifecycle(b *testing.B) {
	now := time.Unix(90_000, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(b, 3, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.recordProposals = false
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		b.Fatalf("new approximate predictive adapter: %v", err)
	}
	input := approximateAdapterTestInput()
	completion := predictiveCompletionObservation{
		PromptTokens: 80, CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
		BackendMeanITL: 10 * time.Millisecond,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchmarkPredictiveReservation = adapter.DecideAndReserve(context.Background(), "benchmark-request", input)
		if benchmarkPredictiveReservation == nil || !benchmarkPredictiveReservation.MarkForwarded() ||
			!benchmarkPredictiveReservation.MarkPrefillComplete() ||
			!observePredictiveSemanticTTFT(benchmarkPredictiveReservation, 20*time.Millisecond) ||
			!observePredictiveCompletion(benchmarkPredictiveReservation, completion) ||
			!benchmarkPredictiveReservation.Terminate(runtimepredictive.TerminalCompleted) {
			b.Fatal("predictive lifecycle did not complete")
		}
	}
	b.StopTimer()
	if err := adapter.Close(); err != nil {
		b.Fatalf("close approximate predictive adapter: %v", err)
	}
}

func BenchmarkApproximatePredictiveDeferredOutcomeLifecycle(b *testing.B) {
	now := time.Unix(91_000, 0)
	calibrator := newApproximateAdapterTestCalibratorWithConfidence(b, 3, 1, 1)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.recordProposals = false
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Mode: "enforce", Now: func() time.Time { return now },
	})
	if err != nil {
		b.Fatalf("new deferred benchmark adapter: %v", err)
	}
	input := approximateAdapterTestInput()
	completion := predictiveCompletionObservation{
		PromptTokens: 80, CompletionTokens: 5, ElapsedSinceRequest: 60 * time.Millisecond,
		BackendMeanITL: 10 * time.Millisecond,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		benchmarkPredictiveReservation = adapter.DecideAndReserve(context.Background(), "deferred-benchmark", input)
		if benchmarkPredictiveReservation == nil || !benchmarkPredictiveReservation.MarkForwarded() ||
			!observePredictiveCompletion(benchmarkPredictiveReservation, completion) ||
			!benchmarkPredictiveReservation.(predictiveResourceReleaser).ReleaseResources() ||
			!benchmarkPredictiveReservation.Terminate(runtimepredictive.TerminalCompleted) {
			b.Fatal("predictive deferred lifecycle did not complete")
		}
	}
	b.StopTimer()
	if err := adapter.Close(); err != nil {
		b.Fatalf("close deferred benchmark adapter: %v", err)
	}
}
