package predictive

import (
	"fmt"
	"sync"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type safeScheduler struct{}

type exploratoryScheduler struct {
	safeScheduler
}

type mismatchedPredictionScheduler struct {
	safeScheduler
}

type panickingOutcomeScheduler struct {
	safeScheduler
}

type panickingPredictionScheduler struct {
	safeScheduler
}

func (panickingPredictionScheduler) Predict(time.Time, domain.VirtualState, domain.RequestCost) SchedulerPrediction {
	panic("injected scheduler prediction panic")
}

type recordingOutcomeScheduler struct {
	safeScheduler
	mu       sync.Mutex
	outcomes []SchedulerOutcome
}

func (panickingOutcomeScheduler) Observe(SchedulerPrediction, SchedulerOutcome) error {
	panic("injected scheduler outcome panic")
}

func (s *recordingOutcomeScheduler) Observe(_ SchedulerPrediction, outcome SchedulerOutcome) error {
	s.mu.Lock()
	s.outcomes = append(s.outcomes, outcome)
	s.mu.Unlock()
	return nil
}

func (s *recordingOutcomeScheduler) snapshot() []SchedulerOutcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]SchedulerOutcome(nil), s.outcomes...)
}

func (safeScheduler) Identity() ModelIdentity {
	return safeSchedulerIdentity()
}

func (safeScheduler) Predict(now time.Time, state domain.VirtualState, request domain.RequestCost) SchedulerPrediction {
	return SchedulerPrediction{
		Identity:    safeSchedulerIdentity(),
		PredictedAt: now,
		Features:    schedulerFeatures(state, request),
		Estimate: domain.SchedulerEstimate{
			ExistingUserTPSLower: 30,
			NewUserTPSLower:      30,
			TTFTUpper:            100 * time.Millisecond,
			TPOTUpper:            25 * time.Millisecond,
		},
		Source:     PredictionSourceStatic,
		Confidence: 0.99,
	}
}

func (exploratoryScheduler) Predict(now time.Time, state domain.VirtualState, request domain.RequestCost) SchedulerPrediction {
	prediction := safeScheduler{}.Predict(now, state, request)
	prediction.Exploratory = true
	return prediction
}

func safeSchedulerIdentity() ModelIdentity {
	return ModelIdentity{
		ProfileID:        "safe-test-profile",
		BackendEpoch:     "safe-test-backend-1",
		PredictorVersion: "safe-test-v1",
	}
}

func (mismatchedPredictionScheduler) Predict(now time.Time, state domain.VirtualState, request domain.RequestCost) SchedulerPrediction {
	prediction := safeScheduler{}.Predict(now, state, request)
	prediction.Identity.PredictorVersion = "wrong-version"
	return prediction
}

func testConstraints() domain.Constraints {
	return domain.Constraints{
		PhysicalKVHard:       85_000,
		ActiveKVHard:         85_000,
		UserTPSTarget:        25,
		TPOTSLO:              50 * time.Millisecond,
		WorkspaceRiskBudget:  0.02,
		PreemptionRiskBudget: 0.002,
		MinimumConfidence:    0.95,
	}
}

func testRequest() domain.RequestCost {
	return domain.RequestCost{
		ManifestID:  "test-profile",
		InputTokens: 10_000,
		KV: domain.KVIncrement{
			PhysicalKVUpper: 10_000,
			ActiveKVUpper:   10_000,
		},
		UncachedPrefillUpper:     10_000,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: 10_000,
		Confidence:               0.99,
	}
}

func TestManagerSnapshotExposesOnlyForwardedPendingPrefillDemand(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), exploratoryScheduler{})
	now := time.Unix(450, 0)
	if decision := manager.DecideAndReserve(now, "pending", testRequest()); decision.Reason != domain.ReasonFit {
		t.Fatalf("pending admission reason = %s", decision.Reason)
	}
	if snapshot := manager.Snapshot(); snapshot.ForwardedPendingPrefills != 0 || snapshot.ForwardedPendingPrefillTokens != 0 {
		t.Fatalf("unforwarded reservation appeared as a pending prefill: %+v", snapshot)
	}
	if !manager.MarkForwarded("pending") {
		t.Fatal("pending reservation was not forwarded")
	}
	firstPendingSequence := manager.Snapshot().ForwardedPendingPrefillSequence
	if snapshot := manager.Snapshot(); snapshot.ForwardedPendingPrefills != 1 || snapshot.ForwardedPendingPrefillTokens != testRequest().UncachedPrefillUpper ||
		!snapshot.ForwardedPendingPrefillFeaturesValid || snapshot.ForwardedPendingPrefillFeatures != schedulerFeatures(domain.VirtualState{}, testRequest()) ||
		!snapshot.ForwardedPendingPrefillExploratory || firstPendingSequence == 0 {
		t.Fatalf("forwarded pending prefill snapshot = %+v", snapshot)
	}
	if decision := manager.DecideAndReserve(now.Add(time.Millisecond), "second-pending", testRequest()); decision.Reason != domain.ReasonFit {
		t.Fatalf("second pending admission reason = %s", decision.Reason)
	}
	if !manager.MarkForwarded("second-pending") {
		t.Fatal("second pending reservation was not forwarded")
	}
	secondPendingSequence := manager.Snapshot().ForwardedPendingPrefillSequence
	if snapshot := manager.Snapshot(); snapshot.ForwardedPendingPrefills != 2 ||
		snapshot.ForwardedPendingPrefillTokens != 2*testRequest().UncachedPrefillUpper ||
		!snapshot.ForwardedPendingPrefillFeaturesValid ||
		snapshot.ForwardedPendingPrefillFeatures.PendingPrefillSequences != 2 ||
		snapshot.ForwardedPendingPrefillFeatures.UncachedPrefillTokens != 2*testRequest().UncachedPrefillUpper ||
		!snapshot.ForwardedPendingPrefillExploratory || secondPendingSequence <= firstPendingSequence {
		t.Fatalf("concurrent prefills did not expose the latest aggregate-pressure attribution: %+v", snapshot)
	}
	if !manager.Terminate("second-pending", TerminalExpired) {
		t.Fatal("second pending reservation was not terminated")
	}
	afterTerminationSequence := manager.Snapshot().ForwardedPendingPrefillSequence
	if snapshot := manager.Snapshot(); snapshot.ForwardedPendingPrefills != 1 || !snapshot.ForwardedPendingPrefillFeaturesValid || !snapshot.ForwardedPendingPrefillExploratory || afterTerminationSequence <= secondPendingSequence {
		t.Fatalf("single pending prefill did not recover immutable attribution: %+v", snapshot)
	}
	if !manager.MarkPrefillComplete("pending") {
		t.Fatal("pending reservation did not complete prefill")
	}
	if snapshot := manager.Snapshot(); snapshot.ForwardedPendingPrefills != 0 || snapshot.ForwardedPendingPrefillTokens != 0 || snapshot.ForwardedPendingPrefillSequence <= afterTerminationSequence {
		t.Fatalf("completed prefill remained in pending counters: %+v", snapshot)
	}
}

func TestManagerPendingPrefillEpisodeAdvancesExactlyOncePerLifecycleTransition(t *testing.T) {
	now := time.Unix(475, 0)
	causes := []TerminalCause{
		TerminalCompleted,
		TerminalLocalQoSReject,
		TerminalClientCancelled,
		TerminalClientDisconnected,
		TerminalUpstreamFailure,
		TerminalTimeout,
		TerminalExpired,
	}
	for _, cause := range causes {
		t.Run(string(cause), func(t *testing.T) {
			manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
			if decision := manager.DecideAndReserve(now, "pending", testRequest()); decision.Reason != domain.ReasonFit {
				t.Fatalf("pending admission reason = %s", decision.Reason)
			}
			if sequence := manager.Snapshot().ForwardedPendingPrefillSequence; sequence != 0 {
				t.Fatalf("unforwarded reservation advanced prefill episode to %d", sequence)
			}
			if !manager.MarkForwarded("pending") {
				t.Fatal("pending reservation was not forwarded")
			}
			forwardedSequence := manager.Snapshot().ForwardedPendingPrefillSequence
			if forwardedSequence == 0 {
				t.Fatal("forward did not create a prefill episode")
			}
			if !manager.Terminate("pending", cause) {
				t.Fatal("pending reservation was not terminated")
			}
			terminatedSequence := manager.Snapshot().ForwardedPendingPrefillSequence
			if terminatedSequence <= forwardedSequence {
				t.Fatalf("pending termination did not advance episode: %d -> %d", forwardedSequence, terminatedSequence)
			}
			if manager.Terminate("pending", cause) {
				t.Fatal("duplicate termination succeeded")
			}
			if sequence := manager.Snapshot().ForwardedPendingPrefillSequence; sequence != terminatedSequence {
				t.Fatalf("duplicate termination advanced episode: %d -> %d", terminatedSequence, sequence)
			}
		})
	}

	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	if decision := manager.DecideAndReserve(now, "completed-prefill", testRequest()); decision.Reason != domain.ReasonFit ||
		!manager.MarkForwarded("completed-prefill") || !manager.MarkPrefillComplete("completed-prefill") {
		t.Fatal("completed-prefill lifecycle setup failed")
	}
	completedSequence := manager.Snapshot().ForwardedPendingPrefillSequence
	if !manager.Complete("completed-prefill") {
		t.Fatal("completed-prefill reservation was not released")
	}
	if sequence := manager.Snapshot().ForwardedPendingPrefillSequence; sequence != completedSequence {
		t.Fatalf("terminal release advanced an already-complete prefill episode: %d -> %d", completedSequence, sequence)
	}

	manager = NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	if decision := manager.DecideAndReserve(now, "released", testRequest()); decision.Reason != domain.ReasonFit || !manager.MarkForwarded("released") {
		t.Fatal("resource-release lifecycle setup failed")
	}
	releaseSequence := manager.Snapshot().ForwardedPendingPrefillSequence
	if _, released := manager.ReleaseResources("released"); !released {
		t.Fatal("pending resources were not released")
	}
	if sequence := manager.Snapshot().ForwardedPendingPrefillSequence; sequence <= releaseSequence {
		t.Fatalf("pending resource release did not close the episode: %d -> %d", releaseSequence, sequence)
	}
}

func TestManagerTerminalObserverPanicStillReleasesReservation(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), panickingOutcomeScheduler{})
	now := time.Unix(500, 0)
	if decision := manager.DecideAndReserve(now, "panic-terminal", testRequest()); decision.Reason != domain.ReasonFit {
		t.Fatalf("panic-terminal admission reason = %s, want fit", decision.Reason)
	}
	if !manager.MarkForwarded("panic-terminal") {
		t.Fatal("panic-terminal reservation was not forwarded")
	}
	outcome := SchedulerOutcome{
		Identity:     safeSchedulerIdentity(),
		ObservedAt:   now.Add(time.Second),
		Attributed:   true,
		UserTPS:      30,
		UserTPSValid: true,
	}
	panicked := false
	terminated := false
	func() {
		defer func() { panicked = recover() != nil }()
		terminated = manager.TerminateWithOutcome("panic-terminal", TerminalCompleted, &outcome)
	}()
	if panicked || !terminated {
		t.Fatalf("terminal observer panic escaped/rejected = %t/%t", panicked, terminated)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 {
		t.Fatalf("terminal observer panic leaked reservation: %+v", snapshot)
	}
}

func TestManagerCensorsOutcomeWhenLaterAdmissionChangesItsProspectiveState(t *testing.T) {
	scheduler := &recordingOutcomeScheduler{}
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), scheduler)
	now := time.Unix(550, 0)
	for _, id := range []string{"first", "second"} {
		if decision := manager.DecideAndReserve(now, id, testRequest()); decision.Reason != domain.ReasonFit {
			t.Fatalf("%s admission reason = %s, want fit", id, decision.Reason)
		}
		if !manager.MarkForwarded(id) {
			t.Fatalf("%s reservation was not forwarded", id)
		}
	}
	outcome := SchedulerOutcome{
		Identity: safeSchedulerIdentity(), ObservedAt: now.Add(time.Second), Attributed: true,
		UserTPS: 30, UserTPSValid: true,
	}
	if !manager.TerminateWithOutcome("first", TerminalCompleted, &outcome) || !manager.TerminateWithOutcome("second", TerminalCompleted, &outcome) {
		t.Fatal("concurrent terminal outcome was rejected")
	}
	outcomes := scheduler.snapshot()
	if len(outcomes) != 2 || !outcomes[0].Censored || outcomes[1].Censored {
		t.Fatalf("causal outcome qualification = %+v, want first censored and final-state request qualified", outcomes)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 {
		t.Fatalf("causal outcome qualification leaked reservation: %+v", snapshot)
	}
}

func TestManagerObservesQualifiedUnreservedOutcomeWithoutChangingAccounting(t *testing.T) {
	scheduler := &recordingOutcomeScheduler{}
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), scheduler)
	now := time.Unix(575, 0)
	prediction := scheduler.Predict(now, domain.VirtualState{}, testRequest())
	outcome := SchedulerOutcome{
		Identity: safeSchedulerIdentity(), ObservedAt: now.Add(time.Second), Attributed: true,
		UserTPS: 30, UserTPSValid: true,
	}
	if !manager.ObserveUnreservedOutcome(prediction, TerminalCompleted, true, outcome) {
		t.Fatal("qualified unreserved outcome was rejected")
	}
	if manager.ObserveUnreservedOutcome(prediction, TerminalLocalQoSReject, false, outcome) {
		t.Fatal("unforwarded local rejection trained an unreserved outcome")
	}
	observed := scheduler.snapshot()
	if len(observed) != 1 || observed[0].Censored {
		t.Fatalf("unreserved outcomes = %+v, want one qualified sample", observed)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.EventSequence != 0 || snapshot.ReservedPhysicalKV != 0 || snapshot.ReservedActiveKV != 0 {
		t.Fatalf("unreserved outcome changed accounting: %+v", snapshot)
	}
}

func TestManagerCensorsLiveReservationWhenUnreservedShadowWorkForwards(t *testing.T) {
	scheduler := &recordingOutcomeScheduler{}
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), scheduler)
	now := time.Unix(590, 0)
	if decision := manager.DecideAndReserve(now, "existing", testRequest()); decision.Reason != domain.ReasonFit {
		t.Fatalf("existing admission reason = %s, want fit", decision.Reason)
	}
	if !manager.MarkForwarded("existing") {
		t.Fatal("existing reservation was not forwarded")
	}
	if marked := manager.MarkLiveOutcomesInterfered(); marked != 1 {
		t.Fatalf("interfered reservations = %d, want 1", marked)
	}
	if marked := manager.MarkLiveOutcomesInterfered(); marked != 0 {
		t.Fatalf("duplicate interference marks = %d, want 0", marked)
	}
	outcome := SchedulerOutcome{
		Identity: safeSchedulerIdentity(), ObservedAt: now.Add(time.Second), Attributed: true,
		UserTPS: 30, UserTPSValid: true,
	}
	if !manager.TerminateWithOutcome("existing", TerminalCompleted, &outcome) {
		t.Fatal("interfered existing reservation did not terminate")
	}
	observed := scheduler.snapshot()
	if len(observed) != 1 || !observed[0].Censored {
		t.Fatalf("interfered reservation outcomes = %+v, want one censored sample", observed)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 {
		t.Fatalf("interference censoring leaked accounting: %+v", snapshot)
	}
}

func TestManagerRejectsNonColdPrefillCostWithoutReservation(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	cost := testRequest()
	cost.UncachedPrefillUpper--

	decision := manager.DecideAndReserve(time.Unix(0, 0), "discounted", cost)
	if decision.Reason != domain.ReasonPredictorProfileUnknown {
		t.Fatalf("reason = %s, want %s", decision.Reason, domain.ReasonPredictorProfileUnknown)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.EventSequence != 0 {
		t.Fatalf("non-cold request cost changed manager state: %+v", snapshot)
	}
}

func TestManagerRejectsStructurallyInconsistentRequestCost(t *testing.T) {
	base := domain.RequestCost{
		ManifestID:  "test-profile",
		InputTokens: 100,
		KV: domain.KVIncrement{
			PhysicalKVUpper: 512,
			ActiveKVUpper:   512,
		},
		FutureKV: domain.KVIncrement{
			PhysicalKVUpper: 384,
			ActiveKVUpper:   384,
		},
		UncachedPrefillUpper:     100,
		DecodeHorizonUpper:       400,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: 500,
		FutureContextTokensUpper: 400,
		Confidence:               0.99,
	}
	tests := []struct {
		name   string
		mutate func(*domain.RequestCost)
	}{
		{name: "zero sequences", mutate: func(cost *domain.RequestCost) { cost.DecodeSequencesUpper = 0 }},
		{name: "multiple sequences", mutate: func(cost *domain.RequestCost) { cost.DecodeSequencesUpper = 2 }},
		{name: "full context mismatch", mutate: func(cost *domain.RequestCost) { cost.ActiveContextTokensUpper-- }},
		{name: "future context mismatch", mutate: func(cost *domain.RequestCost) { cost.FutureContextTokensUpper-- }},
		{name: "full KV below context", mutate: func(cost *domain.RequestCost) {
			cost.KV = domain.KVIncrement{PhysicalKVUpper: 499, ActiveKVUpper: 499}
		}},
		{name: "input KV floor under-covered", mutate: func(cost *domain.RequestCost) {
			cost.FutureKV = domain.KVIncrement{PhysicalKVUpper: 450, ActiveKVUpper: 450}
		}},
		{name: "full physical and active KV diverge", mutate: func(cost *domain.RequestCost) { cost.KV.ActiveKVUpper-- }},
		{name: "future physical and active KV diverge", mutate: func(cost *domain.RequestCost) { cost.FutureKV.ActiveKVUpper-- }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
			cost := base
			test.mutate(&cost)
			decision := manager.DecideAndReserve(time.Unix(0, 0), "invalid", cost)
			if decision.Reason != domain.ReasonPredictorProfileUnknown {
				t.Fatalf("reason = %s, want %s", decision.Reason, domain.ReasonPredictorProfileUnknown)
			}
			if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.EventSequence != 0 {
				t.Fatalf("invalid cost changed manager state: %+v", snapshot)
			}
		})
	}
}

func TestCompletionReopensPredictiveHeadroomBeforeNextSample(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 70_000,
		ActiveKVUpper:   70_000,
	}, testConstraints(), safeScheduler{})

	first := manager.DecideAndReserve(time.Unix(0, 0), "first", testRequest())
	if first.Reason != domain.ReasonFit {
		t.Fatalf("first reason = %s, want fit", first.Reason)
	}
	blocked := manager.DecideAndReserve(time.Unix(0, 1), "blocked", testRequest())
	if blocked.Reason != domain.ReasonKVOverBudget {
		t.Fatalf("blocked reason = %s, want KV over budget", blocked.Reason)
	}
	if !manager.Complete("first") {
		t.Fatal("first completion was not applied")
	}
	reopened := manager.DecideAndReserve(time.Unix(0, 2), "reopened", testRequest())
	if reopened.Reason != domain.ReasonFit {
		t.Fatalf("reopened reason = %s, want fit without a new sample", reopened.Reason)
	}
}

func TestConcurrentPredictAndReserveIsAtomic(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 70_000,
		ActiveKVUpper:   70_000,
	}, testConstraints(), safeScheduler{})

	start := make(chan struct{})
	reasons := make(chan domain.Reason, 2)
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			<-start
			reasons <- manager.DecideAndReserve(time.Unix(0, 0), id, testRequest()).Reason
		}(id)
	}
	close(start)
	wg.Wait()
	close(reasons)

	fit := 0
	over := 0
	for reason := range reasons {
		switch reason {
		case domain.ReasonFit:
			fit++
		case domain.ReasonKVOverBudget:
			over++
		default:
			t.Fatalf("unexpected reason %s", reason)
		}
	}
	if fit != 1 || over != 1 {
		t.Fatalf("fit/over = %d/%d, want 1/1", fit, over)
	}
	snapshot := manager.Snapshot()
	if snapshot.Reservations != 1 || snapshot.ReservedPhysicalKV != 10_000 {
		t.Fatalf("snapshot = %+v, want one 10k reservation", snapshot)
	}
}

func TestConcurrentDecideReconcileTerminateInvalidateAndSnapshotAreBounded(t *testing.T) {
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	constraints := testConstraints()
	constraints.PhysicalKVHard = 1_000_000
	constraints.ActiveKVHard = 1_000_000
	constraints.UserTPSTarget = 0
	manager := NewManager("test-profile", domain.VirtualState{}, constraints, scheduler)
	now := time.Unix(750, 0)
	seedIDs := make([]string, 8)
	for index := range seedIDs {
		id := fmt.Sprintf("seed-%d", index)
		seedIDs[index] = id
		if result := manager.decideAndReserve(now, id, learnedTestCost()); result.Decision.Reason != domain.ReasonFit {
			t.Fatalf("seed %d admission = %+v", index, result)
		}
		manager.MarkForwarded(id)
	}

	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := 0; index < 64; index++ {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			id := fmt.Sprintf("candidate-%d", index)
			result := manager.decideAndReserve(now.Add(time.Duration(index)*time.Millisecond), id, learnedTestCost())
			if result.Decision.Reason == domain.ReasonFit {
				manager.MarkForwarded(id)
				manager.MarkPrefillComplete(id)
				manager.Terminate(id, TerminalClientCancelled)
			}
		}(index)
	}
	for _, id := range seedIDs {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			<-start
			manager.Terminate(id, TerminalCompleted)
		}(id)
	}
	workers.Add(3)
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 128; index++ {
			started := manager.StartSampleWindow()
			finished := manager.EventSequence()
			_ = manager.ReconcileSample(SampleWindow{Observed: domain.VirtualState{}, StartedSequence: started, FinishedSequence: finished})
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		for index := 0; index < 128; index++ {
			manager.InvalidateLearning()
			_ = manager.Snapshot()
		}
	}()
	go func() {
		defer workers.Done()
		<-start
		manager.InvalidateEpoch()
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
		t.Fatal("concurrent manager lifecycle did not complete")
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.IntakeOpen {
		t.Fatalf("concurrent manager final state = %+v, want closed intake with zero reservations", snapshot)
	}
}

func TestDuplicateAndDoubleCompleteAreIdempotent(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 70_000,
		ActiveKVUpper:   70_000,
	}, testConstraints(), safeScheduler{})
	if got := manager.DecideAndReserve(time.Unix(0, 0), "same", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("first reason = %s, want fit", got.Reason)
	}
	if got := manager.DecideAndReserve(time.Unix(0, 1), "same", testRequest()); got.Reason != domain.ReasonDuplicateRequest {
		t.Fatalf("duplicate reason = %s, want duplicate", got.Reason)
	}
	if !manager.Complete("same") {
		t.Fatal("first completion must release")
	}
	if manager.Complete("same") {
		t.Fatal("double completion must be idempotent")
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.ReservedPhysicalKV != 0 {
		t.Fatalf("snapshot after completion = %+v", snapshot)
	}
}

func TestManagerResourceReleaseAtomicallyReturnsLearningInterference(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	now := time.Unix(0, 0)
	if got := manager.DecideAndReserve(now, "first", testRequest()); got.Reason != domain.ReasonFit || !manager.MarkForwarded("first") {
		t.Fatalf("first reservation setup = %s, want forwarded fit", got.Reason)
	}
	if got := manager.DecideAndReserve(now.Add(time.Second), "second", testRequest()); got.Reason != domain.ReasonFit || !manager.MarkForwarded("second") {
		t.Fatalf("second reservation setup = %s, want forwarded fit", got.Reason)
	}
	interfered, released := manager.ReleaseResources("first")
	if !released || !interfered {
		t.Fatalf("first resource release = released %t interfered %t, want true/true", released, interfered)
	}
	if _, released := manager.ReleaseResources("first"); released {
		t.Fatal("duplicate resource release was not idempotent")
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 1 || snapshot.ReservedPhysicalKV != testRequest().KV.PhysicalKVUpper {
		t.Fatalf("resource release changed unrelated reservation accounting: %+v", snapshot)
	}
	if !manager.Terminate("second", TerminalCompleted) {
		t.Fatal("second reservation did not terminate")
	}
}

func TestManagerConcurrentResourceReleaseTerminalAndAdmissionAreAtomic(t *testing.T) {
	const rounds = 128
	for round := 0; round < rounds; round++ {
		manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
		now := time.Unix(int64(round), 0)
		if got := manager.DecideAndReserve(now, "first", testRequest()); got.Reason != domain.ReasonFit || !manager.MarkForwarded("first") {
			t.Fatalf("round %d first reservation setup = %s", round, got.Reason)
		}
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(3)
		var released bool
		var terminated bool
		var admitted domain.Reason
		go func() {
			defer workers.Done()
			<-start
			_, released = manager.ReleaseResources("first")
		}()
		go func() {
			defer workers.Done()
			<-start
			terminated = manager.Terminate("first", TerminalCompleted)
		}()
		go func() {
			defer workers.Done()
			<-start
			admitted = manager.DecideAndReserve(now.Add(time.Second), "second", testRequest()).Reason
		}()
		close(start)
		workers.Wait()
		if released == terminated {
			t.Fatalf("round %d release/terminal winners = %t/%t, want exactly one", round, released, terminated)
		}
		if admitted != domain.ReasonFit {
			t.Fatalf("round %d concurrent admission = %s, want fit", round, admitted)
		}
		if !manager.Terminate("second", TerminalCompleted) {
			t.Fatalf("round %d second reservation did not terminate", round)
		}
		if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.ReservedPhysicalKV != 0 || snapshot.ReservedActiveKV != 0 {
			t.Fatalf("round %d final manager state = %+v", round, snapshot)
		}
	}
}

func TestManagerResourceReleaseReconcilesPrefillMaterialization(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 50_000,
		ActiveKVUpper:   50_000,
	}, testConstraints(), safeScheduler{})
	if got := manager.DecideAndReserve(time.Unix(0, 0), "absorbed", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("absorbed admission reason = %s, want fit", got.Reason)
	}
	if !manager.MarkForwarded("absorbed") || !manager.MarkPrefillComplete("absorbed") {
		t.Fatal("absorbed reservation did not complete prefill")
	}
	watermark := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed:         domain.VirtualState{PhysicalKVUpper: 60_000, ActiveKVUpper: 60_000},
		StartedSequence:  watermark,
		FinishedSequence: watermark,
	}); err != nil {
		t.Fatalf("initial absorbed reconcile: %v", err)
	}
	if _, released := manager.ReleaseResources("absorbed"); !released {
		t.Fatal("absorbed reservation resources were not released")
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.RetiredReservations != 1 || snapshot.Virtual.Lower.PhysicalKVUpper != 50_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 50_000 {
		t.Fatalf("absorbed release state = %+v", snapshot)
	}
	if err := manager.ReconcileSample(SampleWindow{
		Observed:         domain.VirtualState{PhysicalKVUpper: 60_000, ActiveKVUpper: 60_000},
		StartedSequence:  watermark,
		FinishedSequence: watermark,
	}); err != nil {
		t.Fatalf("late absorbed reconcile: %v", err)
	}
	if snapshot := manager.Snapshot(); snapshot.Virtual.Lower.PhysicalKVUpper != 50_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 50_000 {
		t.Fatalf("late absorbed sample reintroduced released work: %+v", snapshot)
	}
	clean := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed:         domain.VirtualState{PhysicalKVUpper: 50_000, ActiveKVUpper: 50_000},
		StartedSequence:  clean,
		FinishedSequence: clean,
	}); err != nil {
		t.Fatalf("clean absorbed reconcile: %v", err)
	}
	if snapshot := manager.Snapshot(); snapshot.RetiredReservations != 0 || snapshot.Virtual.Lower.PhysicalKVUpper != 50_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 50_000 {
		t.Fatalf("clean absorbed release state = %+v", snapshot)
	}

	unabsorbed := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 50_000,
		ActiveKVUpper:   50_000,
	}, testConstraints(), safeScheduler{})
	if got := unabsorbed.DecideAndReserve(time.Unix(0, 0), "unabsorbed", testRequest()); got.Reason != domain.ReasonFit || !unabsorbed.MarkForwarded("unabsorbed") {
		t.Fatalf("unabsorbed reservation setup = %s", got.Reason)
	}
	if _, released := unabsorbed.ReleaseResources("unabsorbed"); !released {
		t.Fatal("unabsorbed reservation resources were not released")
	}
	if snapshot := unabsorbed.Snapshot(); snapshot.Reservations != 0 || snapshot.RetiredReservations != 0 || snapshot.Virtual.Lower.PhysicalKVUpper != 50_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 50_000 {
		t.Fatalf("unabsorbed release state = %+v", snapshot)
	}
}

func TestManagerRejectsMismatchedTokenizerManifestWithoutReservation(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	cost := testRequest()
	cost.ManifestID = "stale-profile"

	decision := manager.DecideAndReserve(time.Unix(0, 0), "stale", cost)
	if decision.Reason != domain.ReasonTokenizerProfileUnknown {
		t.Fatalf("reason = %s, want %s", decision.Reason, domain.ReasonTokenizerProfileUnknown)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.EventSequence != 0 {
		t.Fatalf("stale manifest changed manager state: %+v", snapshot)
	}
}

func TestManagerReturnsDecisionSequenceFromTheAtomicAdmissionSnapshot(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	admitted := manager.decideAndReserve(time.Unix(0, 0), "sequence", testRequest())
	if admitted.Decision.Reason != domain.ReasonFit || admitted.DecisionManagerSequence != 1 {
		t.Fatalf("admitted decision sequence = %+v, want fit at sequence 1", admitted)
	}
	rejected := manager.decideAndReserve(time.Unix(0, 1), "sequence", testRequest())
	if rejected.Decision.Reason != domain.ReasonDuplicateRequest || rejected.DecisionManagerSequence != 1 {
		t.Fatalf("rejected decision sequence = %+v, want duplicate at unchanged sequence 1", rejected)
	}
	if snapshot := manager.Snapshot(); snapshot.EventSequence != rejected.DecisionManagerSequence {
		t.Fatalf("decision/snapshot sequence mismatch: result=%+v snapshot=%+v", rejected, snapshot)
	}
}

func TestManagerTracksPendingPrefillSeparatelyFromReadyDecodeSequences(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	if result := manager.DecideAndReserve(time.Unix(0, 0), "pending", testRequest()); result.Reason != domain.ReasonFit {
		t.Fatalf("pending admission = %+v", result)
	}
	if snapshot := manager.Snapshot(); snapshot.Virtual.Upper.DecodeSequences != 1 || snapshot.Virtual.Upper.PendingPrefillSequences != 1 {
		t.Fatalf("reserved prefill state = %+v, want total/pending 1/1", snapshot.Virtual.Upper)
	}
	if !manager.MarkForwarded("pending") {
		t.Fatal("pending reservation was not forwarded")
	}
	if snapshot := manager.Snapshot(); snapshot.Virtual.Upper.DecodeSequences != 1 || snapshot.Virtual.Upper.PendingPrefillSequences != 1 {
		t.Fatalf("forwarded prefill state = %+v, want total/pending 1/1", snapshot.Virtual.Upper)
	}
	if !manager.MarkPrefillComplete("pending") {
		t.Fatal("pending reservation did not become decode-ready")
	}
	if snapshot := manager.Snapshot(); snapshot.Virtual.Upper.DecodeSequences != 1 || snapshot.Virtual.Upper.PendingPrefillSequences != 0 {
		t.Fatalf("decode-ready state = %+v, want total/pending 1/0", snapshot.Virtual.Upper)
	}
}

func TestManagerRejectsMismatchedSchedulerPredictionWithoutReservation(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), mismatchedPredictionScheduler{})

	decision := manager.DecideAndReserve(time.Unix(0, 0), "stale-predictor", testRequest())
	if decision.Reason != domain.ReasonPredictorProfileUnknown {
		t.Fatalf("reason = %s, want %s", decision.Reason, domain.ReasonPredictorProfileUnknown)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.EventSequence != 0 || snapshot.IntakeOpen {
		t.Fatalf("mismatched scheduler prediction changed manager state: %+v", snapshot)
	}
	if manager.Available() {
		t.Fatal("mismatched scheduler prediction did not quarantine availability")
	}
}

func TestManagerConvertsSchedulerPredictionPanicIntoAvailabilityQuarantine(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), panickingPredictionScheduler{})
	result := manager.decideAndReserve(time.Unix(0, 0), "panic-predictor", testRequest())
	if result.Decision.Reason != domain.ReasonPredictorProfileUnknown || !result.AvailabilityUnavailable ||
		result.Prediction.Source != PredictionSourceUnavailable {
		t.Fatalf("panicking scheduler result = %+v", result)
	}
	if manager.Available() {
		t.Fatal("panicking scheduler did not quarantine current availability")
	}
	if snapshot := manager.Snapshot(); snapshot.IntakeOpen || snapshot.Reservations != 0 || snapshot.EventSequence != 0 {
		t.Fatalf("panicking scheduler quarantine state = %+v", snapshot)
	}
}

func TestSampleAssimilatesReservationPresentAcrossWholePollWindow(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 50_000,
		ActiveKVUpper:   50_000,
	}, testConstraints(), safeScheduler{})

	if got := manager.DecideAndReserve(time.Unix(0, 0), "active", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("admission reason = %s, want fit", got.Reason)
	}
	if !manager.MarkForwarded("active") || !manager.MarkPrefillComplete("active") {
		t.Fatal("active reservation did not reach completed prefill")
	}
	watermark := manager.EventSequence()
	if watermark != 3 {
		t.Fatalf("event sequence = %d, want admission, forward, and prefill events", watermark)
	}
	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper: 60_000,
			ActiveKVUpper:   60_000,
		},
		StartedSequence:  watermark,
		FinishedSequence: watermark,
	}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	snapshot := manager.Snapshot()
	if snapshot.Virtual.Lower.PhysicalKVUpper != 60_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 60_000 {
		t.Fatalf("assimilated virtual interval = %+v, want exact 60k", snapshot.Virtual)
	}
	if !manager.Complete("active") {
		t.Fatal("completion was not applied")
	}
	snapshot = manager.Snapshot()
	if snapshot.Virtual.Lower.PhysicalKVUpper != 50_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 50_000 {
		t.Fatalf("post-completion interval = %+v, want exact 50k", snapshot.Virtual)
	}
}

func TestSampleRetainsUnmaterializedDecodeHorizonAfterCurrentKVIsObserved(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	cost := domain.RequestCost{
		ManifestID:  "test-profile",
		InputTokens: 100,
		KV: domain.KVIncrement{
			PhysicalKVUpper: 500,
			ActiveKVUpper:   500,
		},
		FutureKV: domain.KVIncrement{
			PhysicalKVUpper: 400,
			ActiveKVUpper:   400,
		},
		UncachedPrefillUpper:     100,
		DecodeHorizonUpper:       400,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: 500,
		FutureContextTokensUpper: 400,
		Confidence:               0.99,
	}
	if got := manager.DecideAndReserve(time.Unix(0, 0), "long-decode", cost); got.Reason != domain.ReasonFit {
		t.Fatalf("admission reason = %s, want fit", got.Reason)
	}
	if !manager.MarkForwarded("long-decode") || !manager.MarkPrefillComplete("long-decode") {
		t.Fatal("long decode reservation did not reach completed prefill")
	}
	watermark := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper:     100,
			ActiveKVUpper:       100,
			DecodeSequences:     1,
			ActiveContextTokens: 100,
		},
		StartedSequence:  watermark,
		FinishedSequence: watermark,
	}); err != nil {
		t.Fatalf("reconcile partial materialization: %v", err)
	}
	snapshot := manager.Snapshot()
	if snapshot.Virtual.Upper.PhysicalKVUpper != 500 || snapshot.Virtual.Upper.ActiveKVUpper != 500 || snapshot.Virtual.Upper.ActiveContextTokens != 500 {
		t.Fatalf("future decode reservation was lost after scrape: %+v", snapshot.Virtual)
	}
}

func TestAdmittedButNotForwardedReservationIsNotAssimilated(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 50_000,
		ActiveKVUpper:   50_000,
	}, testConstraints(), safeScheduler{})
	if got := manager.DecideAndReserve(time.Unix(0, 0), "local-queue", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("admission reason = %s, want fit", got.Reason)
	}
	watermark := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper: 50_000,
			ActiveKVUpper:   50_000,
		},
		StartedSequence:  watermark,
		FinishedSequence: watermark,
	}); err != nil {
		t.Fatalf("reconcile local queue: %v", err)
	}
	snapshot := manager.Snapshot()
	if snapshot.Virtual.Upper.PhysicalKVUpper != 60_000 || snapshot.Virtual.Upper.ActiveKVUpper != 60_000 {
		t.Fatalf("local reservation was absorbed before forwarding: %+v", snapshot.Virtual)
	}
}

func TestAdmissionInsideSampleWindowWidensUpperBound(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 50_000,
		ActiveKVUpper:   50_000,
	}, testConstraints(), safeScheduler{})

	started := manager.EventSequence()
	if got := manager.DecideAndReserve(time.Unix(0, 0), "ambiguous", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("admission reason = %s, want fit", got.Reason)
	}
	if !manager.MarkForwarded("ambiguous") || !manager.MarkPrefillComplete("ambiguous") {
		t.Fatal("ambiguous reservation did not reach completed prefill")
	}
	finished := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper: 50_000,
			ActiveKVUpper:   50_000,
		},
		StartedSequence:  started,
		FinishedSequence: finished,
	}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	snapshot := manager.Snapshot()
	if snapshot.Virtual.Lower.PhysicalKVUpper != 50_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 60_000 {
		t.Fatalf("ambiguous virtual interval = %+v, want [50k, 60k]", snapshot.Virtual)
	}
}

func TestAdmissionAfterSampleWindowRemainsDefinitelyUnabsorbed(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 50_000,
		ActiveKVUpper:   50_000,
	}, testConstraints(), safeScheduler{})

	started := manager.EventSequence()
	finished := started
	if got := manager.DecideAndReserve(time.Unix(0, 0), "newer", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("admission reason = %s, want fit", got.Reason)
	}
	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper: 50_000,
			ActiveKVUpper:   50_000,
		},
		StartedSequence:  started,
		FinishedSequence: finished,
	}); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	snapshot := manager.Snapshot()
	if snapshot.Virtual.Lower.PhysicalKVUpper != 60_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 60_000 {
		t.Fatalf("unabsorbed virtual interval = %+v, want exact 60k", snapshot.Virtual)
	}
}

func TestLateSampleDoesNotReintroduceCompletedOwnedWork(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 50_000,
		ActiveKVUpper:   50_000,
	}, testConstraints(), safeScheduler{})

	if got := manager.DecideAndReserve(time.Unix(0, 0), "owned", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("admission reason = %s, want fit", got.Reason)
	}
	if !manager.MarkForwarded("owned") || !manager.MarkPrefillComplete("owned") {
		t.Fatal("owned reservation did not reach completed prefill")
	}
	watermark := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper: 60_000,
			ActiveKVUpper:   60_000,
		},
		StartedSequence:  watermark,
		FinishedSequence: watermark,
	}); err != nil {
		t.Fatalf("initial reconcile failed: %v", err)
	}
	if !manager.Complete("owned") {
		t.Fatal("completion was not applied")
	}

	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper: 60_000,
			ActiveKVUpper:   60_000,
		},
		StartedSequence:  watermark,
		FinishedSequence: watermark,
	}); err != nil {
		t.Fatalf("late reconcile failed: %v", err)
	}
	snapshot := manager.Snapshot()
	if snapshot.Virtual.Lower.PhysicalKVUpper != 50_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 50_000 {
		t.Fatalf("late-sample interval = %+v, want exact 50k", snapshot.Virtual)
	}
}

func TestCompletionInsideSampleWindowRemainsConservativeUntilCleanSample(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 50_000,
		ActiveKVUpper:   50_000,
	}, testConstraints(), safeScheduler{})

	if got := manager.DecideAndReserve(time.Unix(0, 0), "windowed", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("admission reason = %s, want fit", got.Reason)
	}
	if !manager.MarkForwarded("windowed") || !manager.MarkPrefillComplete("windowed") {
		t.Fatal("windowed reservation did not reach completed prefill")
	}
	first := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed:         domain.VirtualState{PhysicalKVUpper: 60_000, ActiveKVUpper: 60_000},
		StartedSequence:  first,
		FinishedSequence: first,
	}); err != nil {
		t.Fatalf("initial reconcile failed: %v", err)
	}

	started := manager.EventSequence()
	if !manager.Complete("windowed") {
		t.Fatal("completion was not applied")
	}
	finished := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed:         domain.VirtualState{PhysicalKVUpper: 60_000, ActiveKVUpper: 60_000},
		StartedSequence:  started,
		FinishedSequence: finished,
	}); err != nil {
		t.Fatalf("ambiguous reconcile failed: %v", err)
	}
	snapshot := manager.Snapshot()
	if snapshot.Virtual.Lower.PhysicalKVUpper != 50_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 60_000 {
		t.Fatalf("completion-window interval = %+v, want [50k, 60k]", snapshot.Virtual)
	}

	clean := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed:         domain.VirtualState{PhysicalKVUpper: 50_000, ActiveKVUpper: 50_000},
		StartedSequence:  clean,
		FinishedSequence: clean,
	}); err != nil {
		t.Fatalf("clean reconcile failed: %v", err)
	}
	snapshot = manager.Snapshot()
	if snapshot.Virtual.Lower.PhysicalKVUpper != 50_000 || snapshot.Virtual.Upper.PhysicalKVUpper != 50_000 {
		t.Fatalf("clean interval = %+v, want exact 50k", snapshot.Virtual)
	}
}

func TestReconcileRejectsInvalidOrStaleWatermarks(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	if err := manager.ReconcileSample(SampleWindow{StartedSequence: 1, FinishedSequence: 0}); err == nil {
		t.Fatal("finish before start must fail")
	}
	if err := manager.ReconcileSample(SampleWindow{StartedSequence: 0, FinishedSequence: 1}); err == nil {
		t.Fatal("future finish watermark must fail")
	}
	if got := manager.DecideAndReserve(time.Unix(0, 0), "one", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("admission reason = %s, want fit", got.Reason)
	}
	if err := manager.ReconcileSample(SampleWindow{StartedSequence: 1, FinishedSequence: 1}); err != nil {
		t.Fatalf("valid sample failed: %v", err)
	}
	if err := manager.ReconcileSample(SampleWindow{StartedSequence: 0, FinishedSequence: 0}); err == nil {
		t.Fatal("stale sample must fail")
	}
}

func TestRetiredReservationQueueIsBoundedFIFOAcrossWrap(t *testing.T) {
	var queue retiredReservationQueue
	for sequence := 0; sequence < maximumRetiredReservations+3; sequence++ {
		evicted := queue.Push(retiredReservation{CompletedSequence: uint64(sequence)})
		if want := sequence >= maximumRetiredReservations; evicted != want {
			t.Fatalf("push %d eviction = %t, want %t", sequence, evicted, want)
		}
	}
	if got := queue.Len(); got != maximumRetiredReservations {
		t.Fatalf("retired queue length = %d, want %d", got, maximumRetiredReservations)
	}
	for want := 3; want < maximumRetiredReservations+3; want++ {
		item, ok := queue.Pop()
		if !ok || item.CompletedSequence != uint64(want) {
			t.Fatalf("retired queue pop = %+v/%t, want sequence %d", item, ok, want)
		}
	}
	if _, ok := queue.Pop(); ok || queue.Len() != 0 {
		t.Fatalf("empty retired queue pop/length = %t/%d", ok, queue.Len())
	}
}

func TestReconcileRetiredQueuePreservesSequenceSemantics(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	manager.eventSequence = 20
	for _, sequence := range []uint64{5, 10, 15} {
		manager.retired.Push(retiredReservation{
			CompletedSequence: sequence,
			MaterializedFloor: domain.RequestCost{KV: domain.KVIncrement{
				PhysicalKVUpper: 100,
				ActiveKVUpper:   100,
			}},
		})
	}
	if err := manager.ReconcileSample(SampleWindow{
		Observed:         domain.VirtualState{PhysicalKVUpper: 1_000, ActiveKVUpper: 1_000},
		StartedSequence:  7,
		FinishedSequence: 12,
	}); err != nil {
		t.Fatalf("mixed retirement reconcile failed: %v", err)
	}
	snapshot := manager.Snapshot()
	if snapshot.Virtual.Lower.PhysicalKVUpper != 800 || snapshot.Virtual.Upper.PhysicalKVUpper != 900 {
		t.Fatalf("mixed retirement interval = %+v, want physical KV [800,900]", snapshot.Virtual)
	}
	if snapshot.RetiredReservations != 2 || snapshot.RetiredEvictions != 0 {
		t.Fatalf("mixed retirement queue snapshot = %+v", snapshot)
	}
	if err := manager.ReconcileSample(SampleWindow{
		Observed:         domain.VirtualState{PhysicalKVUpper: 800, ActiveKVUpper: 800},
		StartedSequence:  20,
		FinishedSequence: 20,
	}); err != nil {
		t.Fatalf("clean retirement reconcile failed: %v", err)
	}
	snapshot = manager.Snapshot()
	if snapshot.Virtual.Lower.PhysicalKVUpper != 800 || snapshot.Virtual.Upper.PhysicalKVUpper != 800 || snapshot.RetiredReservations != 0 {
		t.Fatalf("clean retirement snapshot = %+v, want exact physical KV 800 and empty queue", snapshot)
	}
}

func TestManagerEpochInvalidationClosesIntakeButPreservesTerminalRelease(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	if !manager.Available() {
		t.Fatal("new manager unexpectedly unavailable")
	}
	first := manager.DecideAndReserve(time.Unix(20_000, 0), "in-flight", testRequest())
	if first.Reason != domain.ReasonFit {
		t.Fatalf("initial reservation = %+v", first)
	}
	if !manager.InvalidateEpoch() {
		t.Fatal("first epoch invalidation did not close intake")
	}
	if manager.InvalidateEpoch() {
		t.Fatal("duplicate epoch invalidation reported a state change")
	}
	if manager.Available() {
		t.Fatal("epoch-invalidated manager still reported available")
	}
	blocked := manager.DecideAndReserve(time.Unix(20_001, 0), "after-drift", testRequest())
	if blocked.Reason != domain.ReasonPredictorProfileUnknown {
		t.Fatalf("post-drift admission = %+v, want closed intake", blocked)
	}
	if snapshot := manager.Snapshot(); snapshot.IntakeOpen || snapshot.Reservations != 1 {
		t.Fatalf("quarantined snapshot = %+v, want closed intake with owned reservation", snapshot)
	}
	if !manager.Terminate("in-flight", TerminalExpired) {
		t.Fatal("quarantined in-flight reservation could not terminate")
	}
	if snapshot := manager.Snapshot(); snapshot.IntakeOpen || snapshot.Reservations != 0 || snapshot.ReservedPhysicalKV != 0 {
		t.Fatalf("post-terminal quarantined snapshot = %+v", snapshot)
	}
}

func BenchmarkRetiredReservationQueuePushAtCapacity(b *testing.B) {
	var queue retiredReservationQueue
	for sequence := 0; sequence < maximumRetiredReservations; sequence++ {
		queue.Push(retiredReservation{CompletedSequence: uint64(sequence)})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for sequence := 0; sequence < b.N; sequence++ {
		queue.Push(retiredReservation{CompletedSequence: uint64(sequence)})
	}
}

func BenchmarkManagerReleaseResources(b *testing.B) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), safeScheduler{})
	now := time.Unix(100_000, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if decision := manager.DecideAndReserve(now, "release-benchmark", testRequest()); decision.Reason != domain.ReasonFit || !manager.MarkForwarded("release-benchmark") {
			b.Fatalf("release benchmark reservation setup = %s", decision.Reason)
		}
		if _, released := manager.ReleaseResources("release-benchmark"); !released {
			b.Fatal("release benchmark did not release resources")
		}
	}
}
