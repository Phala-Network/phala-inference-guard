package predictive

import (
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestTerminalKeepsLastObservedKVWithoutNegativeRetiredCredit(t *testing.T) {
	manager := NewManager("request-aware-test", domain.VirtualState{})
	policy := newPrefillRequestAwareTestPolicy(t)
	cost := requestAwareManagerCost(96, 16)
	input := requestAwareManagerInput()
	input.Running = 0
	input.CapacityTokens = 4 * 1024 * 1024

	reserved := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "covered", cost, 96, policy, input,
	)
	if !reserved.Reserved || !manager.MarkForwarded("covered") || !manager.MarkPrefillComplete("covered") {
		t.Fatalf("reservation setup=%+v snapshot=%+v", reserved, manager.Snapshot())
	}
	started := manager.StartSampleWindow()
	finished := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper:     96,
			ActiveKVUpper:       96,
			DecodeSequences:     1,
			ActiveContextTokens: 96,
		},
		StartedSequence:     started,
		FinishedSequence:    finished,
		ObservationSequence: 1,
	}); err != nil {
		t.Fatalf("cover reservation with observation: %v", err)
	}
	if before := manager.Snapshot(); before.Virtual.Upper.PhysicalKVUpper != 112 {
		t.Fatalf("covered overlay=%+v, want observed 96 + future 16", before)
	}

	if !manager.Terminate("covered", TerminalCompleted) {
		t.Fatal("covered reservation did not terminate")
	}
	after := manager.Snapshot()
	if after.Virtual.Upper.PhysicalKVUpper != 96 || after.Virtual.Lower.PhysicalKVUpper != 96 ||
		after.RetiredReservations != 0 || after.CompletedDecodeCredits != 0 {
		t.Fatalf("terminal snapshot=%+v, want unchanged stale observation and no negative retired credit", after)
	}
}

func TestObservedRunningIsNotReducedByInferredPendingPrefillOwnership(t *testing.T) {
	manager := NewManager("request-aware-test", domain.VirtualState{})
	policy := newPrefillRequestAwareTestPolicy(t)
	cost := requestAwareManagerCost(96, 16)
	input := requestAwareManagerInput()
	input.Running = 0
	input.CapacityTokens = 4 * 1024 * 1024

	reserved := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "pending", cost, 96, policy, input,
	)
	if !reserved.Reserved || !manager.MarkForwarded("pending") {
		t.Fatalf("pending reservation setup=%+v snapshot=%+v", reserved, manager.Snapshot())
	}
	started := manager.StartSampleWindow()
	finished := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed: domain.VirtualState{
			PhysicalKVUpper:     96,
			ActiveKVUpper:       96,
			DecodeSequences:     1,
			ActiveContextTokens: 96,
		},
		StartedSequence:     started,
		FinishedSequence:    finished,
		ObservationSequence: 1,
	}); err != nil {
		t.Fatalf("observe pending reservation: %v", err)
	}

	current := requestAwareManagerInput()
	current.Running = 1
	current.CapacityTokens = 4 * 1024 * 1024
	current.ObservationSequence = 1
	decision := manager.DecideRequestAware(
		time.Unix(2, 0), "probe", requestAwareManagerCost(16, 0), 16, policy, current,
	)
	if decision.Decision.EffectiveSequences != 1 {
		t.Fatalf("decision=%+v, want conservative running upper 1 without inferred Prefill subtraction", decision.Decision)
	}
}
