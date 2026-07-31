package predictive

import (
	"sync"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestCountCoordinatorChargesEveryRepeatedPrefixAsFullColdCost(t *testing.T) {
	coordinator := newCountTestCoordinator(t, domain.VirtualState{}, 1_000)
	proposal := countTestProposal("first", 65, 1)

	first := coordinator.DecideAndReserve(time.Unix(0, 0), proposal)
	if !first.Reserved || first.Decision.Reason != domain.ReasonFit {
		t.Fatalf("first admission = %+v", first)
	}
	wantCost := CountRequestCost{
		ManifestID:               "test-profile",
		BackendEpoch:             "safe-test-backend-1",
		InputTokens:              65,
		PhysicalKVUpper:          128,
		ActiveKVUpper:            128,
		UncachedPrefillUpper:     65,
		DecodeHorizonUpper:       1,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: 66,
		Confidence:               0.99,
	}
	if first.Cost != wantCost {
		t.Fatalf("first full-cold cost = %+v, want %+v", first.Cost, wantCost)
	}
	if !coordinator.Complete("first") {
		t.Fatal("complete first reservation")
	}

	proposal.RequestID = "second"
	second := coordinator.DecideAndReserve(time.Unix(0, 1), proposal)
	if !second.Reserved || second.Decision.Reason != domain.ReasonFit || second.Cost != wantCost {
		t.Fatalf("repeated-prefix admission = %+v, want identical full-cold fit", second)
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Reservations != 1 || snapshot.Manager.ReservedPhysicalKV != 128 {
		t.Fatalf("repeated-prefix reservation snapshot = %+v", snapshot)
	}
}

func TestCountCoordinatorAtomicReservationAndCompletionReopenSafeCapacity(t *testing.T) {
	coordinator := newCountTestCoordinator(t, domain.VirtualState{
		PhysicalKVUpper: 70,
		ActiveKVUpper:   70,
	}, 134)

	start := make(chan struct{})
	results := make(chan struct {
		id     string
		result CountAdmissionResult
	}, 2)
	var workers sync.WaitGroup
	for _, id := range []string{"a", "b"} {
		workers.Add(1)
		go func(id string) {
			defer workers.Done()
			<-start
			results <- struct {
				id     string
				result CountAdmissionResult
			}{id: id, result: coordinator.DecideAndReserve(time.Unix(0, 0), countTestProposal(id, 5, 5))}
		}(id)
	}
	close(start)
	workers.Wait()
	close(results)

	reservedID := ""
	fit := 0
	over := 0
	for item := range results {
		switch item.result.Decision.Reason {
		case domain.ReasonFit:
			fit++
			reservedID = item.id
			if !item.result.Reserved {
				t.Fatalf("fit %q did not reserve", item.id)
			}
		case domain.ReasonKVOverBudget:
			over++
			if item.result.Reserved {
				t.Fatalf("over-budget %q reserved", item.id)
			}
		default:
			t.Fatalf("unexpected atomic result for %q: %+v", item.id, item.result)
		}
	}
	if fit != 1 || over != 1 {
		t.Fatalf("fit/over = %d/%d, want 1/1", fit, over)
	}
	if !coordinator.Complete(reservedID) {
		t.Fatalf("complete %q", reservedID)
	}
	reopened := coordinator.DecideAndReserve(time.Unix(0, 1), countTestProposal("reopened", 5, 5))
	if !reopened.Reserved || reopened.Decision.Reason != domain.ReasonFit {
		t.Fatalf("capacity did not reopen before a new sample: %+v", reopened)
	}
}

func TestCountCoordinatorRejectsIdentityAndProposalErrorsWithoutMutation(t *testing.T) {
	coordinator := newCountTestCoordinator(t, domain.VirtualState{}, 1_000)
	tests := []struct {
		name     string
		proposal CountAdmissionProposal
		want     domain.Reason
	}{
		{name: "wrong manifest", proposal: countTestProposal("manifest", 5, 5), want: domain.ReasonTokenizerProfileUnknown},
		{name: "wrong epoch", proposal: countTestProposal("epoch", 5, 5), want: domain.ReasonTokenizerProfileUnknown},
		{name: "empty request id", proposal: countTestProposal("", 5, 5), want: domain.ReasonPredictorProfileUnknown},
		{name: "negative horizon", proposal: countTestProposal("horizon", 5, -1), want: domain.ReasonPredictorProfileUnknown},
		{name: "invalid confidence", proposal: countTestProposal("confidence", 5, 5), want: domain.ReasonPredictorProfileUnknown},
	}
	tests[0].proposal.Analysis.ManifestID = "other"
	tests[1].proposal.Analysis.BackendEpoch = "other"
	tests[4].proposal.Confidence = 0
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := coordinator.DecideAndReserve(time.Unix(0, 0), test.proposal)
			if result.Decision.Reason != test.want || result.Reserved {
				t.Fatalf("result = %+v, want non-reserved %s", result, test.want)
			}
		})
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Reservations != 0 || snapshot.Manager.EventSequence != 0 {
		t.Fatalf("invalid proposals mutated state: %+v", snapshot)
	}
}

func newCountTestCoordinator(t *testing.T, initial domain.VirtualState, kvHard int64) *CountCoordinator {
	t.Helper()
	constraints := testConstraints()
	constraints.PhysicalKVHard = kvHard
	constraints.ActiveKVHard = kvHard
	coordinator, err := NewCountCoordinator(CountCoordinatorConfig{
		Identity: CoordinatorIdentity{
			ManifestID:   "test-profile",
			BackendEpoch: "safe-test-backend-1",
			Scheduler:    safeSchedulerIdentity(),
			BlockSize:    64,
		},
		Initial:     initial,
		Constraints: constraints,
		Scheduler:   safeScheduler{},
	})
	if err != nil {
		t.Fatalf("new count coordinator: %v", err)
	}
	return coordinator
}

func countTestProposal(requestID string, inputTokens, decodeHorizon int64) CountAdmissionProposal {
	return CountAdmissionProposal{
		RequestID: requestID,
		Analysis: TokenCountAnalysis{
			ManifestID:       "test-profile",
			BackendEpoch:     "safe-test-backend-1",
			ExactInputTokens: inputTokens,
		},
		DecodeHorizonUpper: decodeHorizon,
		Confidence:         0.99,
	}
}
