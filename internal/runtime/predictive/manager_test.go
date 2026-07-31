package predictive

import (
	"sync"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type safeScheduler struct{}

type mismatchedPredictionScheduler struct {
	safeScheduler
}

func (safeScheduler) Identity() ModelIdentity {
	return safeSchedulerIdentity()
}

func (safeScheduler) Predict(now time.Time, state domain.VirtualState, request domain.RequestCost) SchedulerPrediction {
	return SchedulerPrediction{
		Identity:    safeSchedulerIdentity(),
		PredictedAt: now,
		Estimate: domain.SchedulerEstimate{
			ExistingUserTPSLower: 30,
			AllUserTPSLower:      30,
			TTFTUpper:            100 * time.Millisecond,
			TPOTUpper:            25 * time.Millisecond,
		},
		Confidence: 0.99,
	}
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
		TTFTSLO:              time.Second,
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
		Confidence: 0.99,
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

func TestManagerRejectsMismatchedSchedulerPredictionWithoutReservation(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), mismatchedPredictionScheduler{})

	decision := manager.DecideAndReserve(time.Unix(0, 0), "stale-predictor", testRequest())
	if decision.Reason != domain.ReasonPredictorProfileUnknown {
		t.Fatalf("reason = %s, want %s", decision.Reason, domain.ReasonPredictorProfileUnknown)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.EventSequence != 0 {
		t.Fatalf("mismatched scheduler prediction changed manager state: %+v", snapshot)
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
	watermark := manager.EventSequence()
	if watermark != 1 {
		t.Fatalf("event sequence = %d, want 1", watermark)
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

func TestAdmissionInsideSampleWindowWidensUpperBound(t *testing.T) {
	manager := NewManager("test-profile", domain.VirtualState{
		PhysicalKVUpper: 50_000,
		ActiveKVUpper:   50_000,
	}, testConstraints(), safeScheduler{})

	started := manager.EventSequence()
	if got := manager.DecideAndReserve(time.Unix(0, 0), "ambiguous", testRequest()); got.Reason != domain.ReasonFit {
		t.Fatalf("admission reason = %s, want fit", got.Reason)
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
