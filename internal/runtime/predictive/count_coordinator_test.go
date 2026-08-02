package predictive

import (
	"math"
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
		ManifestID:                   "test-profile",
		BackendEpoch:                 "safe-test-backend-1",
		InputTokens:                  65,
		RequestComplexityTokensUpper: 65,
		PhysicalKVUpper:              128,
		ActiveKVUpper:                128,
		FuturePhysicalKVUpper:        0,
		FutureActiveKVUpper:          0,
		UncachedPrefillUpper:         65,
		DecodeHorizonUpper:           1,
		DecodeSequencesUpper:         1,
		ActiveContextTokensUpper:     66,
		FutureContextTokensUpper:     1,
		Confidence:                   0.99,
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
		{name: "negative accrued local latency", proposal: countTestProposal("local-latency", 5, 5), want: domain.ReasonPredictorProfileUnknown},
		{name: "invalid confidence", proposal: countTestProposal("confidence", 5, 5), want: domain.ReasonPredictorProfileUnknown},
	}
	tests[0].proposal.Analysis.ManifestID = "other"
	tests[1].proposal.Analysis.BackendEpoch = "other"
	tests[4].proposal.AccruedLocalAdmissionLatency = -time.Nanosecond
	tests[5].proposal.Confidence = 0
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := coordinator.DecideAndReserve(time.Unix(0, 0), test.proposal)
			if result.Decision.Reason != test.want || result.Reserved || result.AvailabilityUnavailable {
				t.Fatalf("result = %+v, want non-reserved %s", result, test.want)
			}
		})
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Reservations != 0 || snapshot.Manager.EventSequence != 0 {
		t.Fatalf("invalid proposals mutated state: %+v", snapshot)
	}
}

func TestCountCoordinatorPublishesNodeAvailabilitySeparatelyFromRequestErrors(t *testing.T) {
	coordinator := newCountTestCoordinator(t, domain.VirtualState{}, 1_000)
	if !coordinator.Available() {
		t.Fatal("new coordinator unexpectedly unavailable")
	}
	if !coordinator.InvalidateEpoch() || coordinator.Available() {
		t.Fatal("epoch invalidation did not close current coordinator availability")
	}
	result := coordinator.DecideAndReserve(time.Unix(0, 0), countTestProposal("unavailable", 5, 5))
	if result.Reserved || result.Decision.Reason != domain.ReasonPredictorProfileUnknown ||
		!result.AvailabilityUnavailable || result.Prediction.Source != PredictionSourceUnavailable {
		t.Fatalf("unavailable coordinator result = %+v", result)
	}
}

func TestCountCoordinatorReturnsUnknownForInvalidContextUpperWithoutMutation(t *testing.T) {
	coordinator := newCountTestCoordinatorWithModelMaximum(t, domain.VirtualState{}, math.MaxInt64, 1_024)
	tests := []struct {
		name     string
		proposal CountAdmissionProposal
	}{
		{name: "input plus output overflow", proposal: countTestProposal("overflow", math.MaxInt64, 1)},
		{name: "model maximum exceeded", proposal: countTestProposal("model-limit", 900, 125)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := coordinator.DecideAndReserve(time.Unix(0, 0), test.proposal)
			if result.Decision.Reason != domain.ReasonPredictorProfileUnknown || result.Reserved || result.Cost != (CountRequestCost{}) {
				t.Fatalf("invalid context result = %+v, want predictor-profile unknown without a projected cost", result)
			}
		})
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Reservations != 0 || snapshot.Manager.EventSequence != 0 {
		t.Fatalf("invalid contexts mutated state: %+v", snapshot)
	}
}

func TestCountCoordinatorConsumesLearnedResidualBeforeForwardWithStateHeldConstant(t *testing.T) {
	now := time.Unix(9_000, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	constraints := testLearnedConstraints()
	proposal := CountAdmissionProposal{
		RequestID: "cold",
		Analysis: TokenCountAnalysis{
			ManifestID:       "test-profile",
			BackendEpoch:     testPredictorIdentity().BackendEpoch,
			ExactInputTokens: 1_000,
		},
		DecodeHorizonUpper: 256,
		Confidence:         0.99,
	}

	cold := newCountLearnedCoordinator(t, scheduler, constraints).DecideAndReserve(now, proposal)
	if cold.Decision.Reason != domain.ReasonNewTPSAtRisk || cold.Reserved {
		t.Fatalf("cold decision = %+v, want prospective post-prefill new-user TPS risk", cold)
	}

	trainingConstraints := constraints
	trainingConstraints.UserTPSTarget = 0
	training := newCountLearnedCoordinator(t, scheduler, trainingConstraints)
	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		requestID := string(rune('a' + index))
		proposal.RequestID = requestID
		result := training.DecideAndReserve(now.Add(time.Duration(index)*time.Second), proposal)
		if !result.Reserved || result.Decision.Reason != domain.ReasonFit {
			t.Fatalf("training admission %d = %+v", index, result)
		}
		if !training.MarkForwarded(requestID) {
			t.Fatalf("mark training request %d forwarded", index)
		}
		outcome := healthyLearnedOutcome(result.Prediction, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
		if !training.ObserveOutcome(requestID, outcome) {
			t.Fatalf("training outcome %d was not attributed", index)
		}
		if !training.Complete(requestID) {
			t.Fatalf("complete training request %d", index)
		}
	}

	proposal.RequestID = "learned"
	learned := newCountLearnedCoordinator(t, scheduler, constraints).DecideAndReserve(now.Add(10*time.Second), proposal)
	if !learned.Reserved || learned.Decision.Reason != domain.ReasonFit || learned.Prediction.Source != PredictionSourceCalibrated {
		t.Fatalf("learned pre-forward decision = %+v, want calibrated fit", learned)
	}
	if learned.Decision.Projection != cold.Decision.Projection {
		t.Fatalf("current-state projection changed: cold=%+v learned=%+v", cold.Decision.Projection, learned.Decision.Projection)
	}
	if learned.Prediction.Estimate.NewUserTPSLower <= cold.Prediction.Estimate.NewUserTPSLower {
		t.Fatalf("learned new-user TPS %.3f did not exceed cold %.3f", learned.Prediction.Estimate.NewUserTPSLower, cold.Prediction.Estimate.NewUserTPSLower)
	}
}

func TestCountCoordinatorReservesApproximateUpperWithoutExactTokenizerIdentity(t *testing.T) {
	constraints := testConstraints()
	constraints.PhysicalKVHard = 1_000
	constraints.ActiveKVHard = 1_000
	schedulerIdentity := safeSchedulerIdentity()
	coordinator, err := NewCountCoordinator(CountCoordinatorConfig{
		Identity: CoordinatorIdentity{
			ManifestID: "approximate-test", BackendEpoch: schedulerIdentity.BackendEpoch,
			Scheduler: schedulerIdentity, BlockSize: 4,
		},
		ModelMaximumLength: 1_000,
		Constraints:        constraints,
		Scheduler:          safeScheduler{},
	})
	if err != nil {
		t.Fatalf("new approximate coordinator: %v", err)
	}
	result := coordinator.DecideUpperBoundAndReserve(time.Unix(0, 0), UpperBoundAdmissionProposal{
		RequestID: "approximate", InputTokensUpper: 75, RawInputTokensHigh: 100, DecodeHorizonUpper: 10,
		Confidence: 0.99,
	})
	if !result.Reserved || result.Cost.InputTokens != 75 || result.Cost.UncachedPrefillUpper != 75 || result.Cost.RequestComplexityTokensUpper != 100 {
		t.Fatalf("approximate upper was not reserved as full prefill: %+v", result)
	}
	if result.DecisionManagerSequence != 1 {
		t.Fatalf("approximate decision manager sequence = %d, want 1", result.DecisionManagerSequence)
	}
	pending, ok := PendingPrefillObservationForResult(result)
	if !ok || pending.DecisionManagerSequence != result.DecisionManagerSequence {
		t.Fatalf("pending-prefill decision sequence was not preserved: pending=%+v result=%+v", pending, result)
	}
	if result.Cost.PhysicalKVUpper != 88 || result.Cost.FuturePhysicalKVUpper != 12 {
		t.Fatalf("approximate block-rounded cost = %+v, want total/future 88/12", result.Cost)
	}
	if !coordinator.Terminate("approximate", TerminalExpired) {
		t.Fatal("approximate reservation did not release")
	}
}

func newCountTestCoordinator(t *testing.T, initial domain.VirtualState, kvHard int64) *CountCoordinator {
	return newCountTestCoordinatorWithModelMaximum(t, initial, kvHard, 262_144)
}

func newCountTestCoordinatorWithModelMaximum(t *testing.T, initial domain.VirtualState, kvHard, modelMaximumLength int64) *CountCoordinator {
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
		ModelMaximumLength: modelMaximumLength,
		Initial:            initial,
		Constraints:        constraints,
		Scheduler:          safeScheduler{},
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

func newCountLearnedCoordinator(t *testing.T, scheduler Scheduler, constraints domain.Constraints) *CountCoordinator {
	t.Helper()
	coordinator, err := NewCountCoordinator(CountCoordinatorConfig{
		Identity: CoordinatorIdentity{
			ManifestID:   "test-profile",
			BackendEpoch: testPredictorIdentity().BackendEpoch,
			Scheduler:    testPredictorIdentity(),
			BlockSize:    64,
		},
		ModelMaximumLength: 262_144,
		Initial:            learnedTestState(),
		Constraints:        constraints,
		Scheduler:          scheduler,
	})
	if err != nil {
		t.Fatalf("new learned count coordinator: %v", err)
	}
	return coordinator
}
