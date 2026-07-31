package predictive

import (
	"sync"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type safeScheduler struct{}

func (safeScheduler) Predict(state domain.VirtualState, request domain.RequestCost) domain.SchedulerEstimate {
	return domain.SchedulerEstimate{
		ExistingUserTPSLower: 30,
		AllUserTPSLower:      30,
		TTFTUpper:            100 * time.Millisecond,
		TPOTUpper:            25 * time.Millisecond,
	}
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
		ManifestID: "test-profile",
		InputTokens: 10_000,
		KV: domain.KVIncrement{
			PhysicalKVUpper: 10_000,
			ActiveKVUpper:   10_000,
		},
		Confidence: 0.99,
	}
}

func TestCompletionReopensPredictiveHeadroomBeforeNextSample(t *testing.T) {
	manager := NewManager(domain.VirtualState{
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
	manager := NewManager(domain.VirtualState{
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
	manager := NewManager(domain.VirtualState{
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
