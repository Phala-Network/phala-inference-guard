package predictive

import (
	"fmt"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestPrefillQoSGateIgnoresInstantaneousTPSValues(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	base := requestAwareTestInput()
	base.CapacityTokens = 4 * 1024 * 1024
	base.Running = 4
	base.EffectiveSequences = 4
	base.SelectionInputTokens = 49 * 1024
	base.SafetyInputTokens = 49 * 1024
	base.EstimatedPrefillTokens = 49 * 1024
	base.RequestReservedTokens = 52 * 1024

	low := base
	low.AggregateTPSProxy = 0.01
	low.MeanActiveTPSProxy = 0.001
	low.TPSValid = true
	high := base
	high.AggregateTPSProxy = 1_000_000
	high.MeanActiveTPSProxy = 1_000_000
	high.TPSValid = true

	lowDecision := policy.Evaluate(low)
	highDecision := policy.Evaluate(high)
	if lowDecision != highDecision || lowDecision.Action != RequestAwareAdmit {
		t.Fatalf("TPS changed admission: low=%+v high=%+v", lowDecision, highDecision)
	}
}

func TestPrefillQoSGateDoesNotTreatCompletedGenerationWindowTPSAsContention(t *testing.T) {
	policy := newLargeRequestAwareTestPolicy(t)
	input := requestAwareTestInput()
	input.CapacityTokens = 4 * 1024 * 1024
	input.Running = 0
	input.Waiting = 0
	input.LocalActiveDecodeSequences = 0
	input.AggregateTPSProxy = 80
	input.MeanActiveTPSProxy = 20
	input.TPSValid = true
	input.SelectionInputTokens = 96 * 1024
	input.SafetyInputTokens = 96 * 1024
	input.EstimatedPrefillTokens = 96 * 1024
	input.RequestReservedTokens = 100 * 1024

	decision := policy.Evaluate(input)
	if decision.Action != RequestAwareAdmit || decision.Contended ||
		decision.PrefillClass != RequestAwarePrefillWeighted {
		t.Fatalf("completed generation window decision=%+v, want open weighted admission", decision)
	}
}

func TestPrefillQoSGateBoundsManySmallRequestsUnderContention(t *testing.T) {
	const kib = int64(1024)
	policy := newLargeRequestAwareTestPolicy(t)
	manager := NewManager("request-aware-test", domain.VirtualState{DecodeSequences: 4})
	input := RequestAwareInput{
		MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * 1024, Running: 4,
	}

	for index := range 16 {
		requestID := fmt.Sprintf("contended-small-%d", index)
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, int64(index)), requestID, requestAwareManagerCost(4*kib, 0), 4*kib, policy, input,
		)
		if !result.Reserved || result.Decision.Action != RequestAwareAdmit {
			t.Fatalf("contended request %d=%+v, want admitted through 64K boundary", index, result)
		}
	}
	blocked := manager.DecideRequestAwareAndReserve(
		time.Unix(2, 0), "contended-small-16", requestAwareManagerCost(4*kib, 0), 4*kib, policy, input,
	)
	if blocked.Reserved || blocked.Decision.Action != RequestAwareSizeProtect ||
		blocked.Decision.Reason != RequestAwareReasonPrefillBudget ||
		blocked.ProtectionScope != RequestAwareProtectionLoad ||
		!blocked.CanonicalDecisionValid || blocked.CanonicalDecision.Action == RequestAwareAdmit {
		t.Fatalf("post-budget decision=%+v, want load-scoped bounded protection", blocked)
	}

	// First byte moves exactly one request out of pending Prefill. The backend
	// is still contended by the resulting local Decode, but the released 4K
	// budget must be reusable immediately without a poll, cooldown, or a new
	// business-request success signal.
	if !manager.MarkForwarded("contended-small-0") ||
		!manager.MarkPrefillComplete("contended-small-0") {
		t.Fatal("first contended request did not reach first-byte lifecycle")
	}
	recovered := manager.DecideRequestAwareAndReserve(
		time.Unix(2, 1), "contended-small-recovered", requestAwareManagerCost(4*kib, 0), 4*kib, policy, input,
	)
	if !recovered.Reserved || recovered.Decision.Action != RequestAwareAdmit ||
		!recovered.Decision.Contended || recovered.Decision.PostAdmitPendingPrefillTokens != 64*kib {
		t.Fatalf("first-byte recovery=%+v, want immediate reuse at the 64K boundary", recovered)
	}

	for index := range 16 {
		if !manager.Terminate(fmt.Sprintf("contended-small-%d", index), TerminalExpired) {
			t.Fatalf("terminal cleanup failed for contended request %d", index)
		}
	}
	if !manager.Terminate("contended-small-recovered", TerminalExpired) {
		t.Fatal("terminal cleanup failed for recovered request")
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 ||
		snapshot.Virtual.Upper.PendingPrefillSequences != 0 ||
		snapshot.Virtual.Upper.UncachedPrefillTokens != 0 {
		t.Fatalf("small-request lifecycle did not drain exactly: %+v", snapshot)
	}

	lowFlow := manager.DecideRequestAwareAndReserve(
		time.Unix(3, 0), "contended-low-flow", requestAwareManagerCost(49*kib, 0), 49*kib, policy, input,
	)
	if !lowFlow.Reserved || lowFlow.Decision.Action != RequestAwareAdmit ||
		!manager.Terminate("contended-low-flow", TerminalExpired) {
		t.Fatalf("post-drain low-flow request=%+v, want immediate admission and exact drain", lowFlow)
	}
}

func TestPrefillQoSGateLocalDecodeSelectsContentionAfterObservationCoverage(t *testing.T) {
	const kib = int64(1024)
	policy := newLargeRequestAwareTestPolicy(t)
	manager := NewManager("request-aware-test", domain.VirtualState{})
	idle := RequestAwareInput{MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * 1024}
	active := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "active-decode", requestAwareManagerCost(4*kib, 256), 4*kib, policy, idle,
	)
	if !active.Reserved || !manager.MarkForwarded("active-decode") || !manager.MarkPrefillComplete("active-decode") {
		t.Fatalf("active Decode setup=%+v", active)
	}
	weighted := manager.DecideRequestAware(
		time.Unix(2, 0), "weighted", requestAwareManagerCost(96*kib, 0), 96*kib, policy, idle,
	)
	if weighted.Decision.Action != RequestAwareSizeProtect || !weighted.Decision.Contended ||
		weighted.ProtectionScope != RequestAwareProtectionRequest ||
		weighted.CanonicalDecision.Action != RequestAwareAdmit {
		t.Fatalf("local Decode contention=%+v, want request-scoped weighted protection", weighted)
	}
}
