package predictive

import (
	"fmt"
	"sync"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestRequestAwareManagerRequestSizeChangesRealReservation(t *testing.T) {
	const kib = int64(1024)
	policy := newPrefillRequestAwareTestPolicy(t)
	smallManager := NewManager("request-aware-test", domain.VirtualState{})
	largeManager := NewManager("request-aware-test", domain.VirtualState{})
	input := RequestAwareInput{
		MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib,
		Running: 4, EffectiveSequences: 4,
	}

	small := smallManager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "small", requestAwareManagerCost(8*kib, 100), 8*kib, policy, input,
	)
	large := largeManager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "large", requestAwareManagerCost(650*kib, 100), 650*kib, policy, input,
	)

	if !small.Reserved || small.Decision.Action != RequestAwareAdmit || large.Reserved || large.Decision.Action != RequestAwareSizeProtect {
		t.Fatalf("manager size decisions small=%+v large=%+v", small, large)
	}
	if got := smallManager.Snapshot().Reservations; got != 1 {
		t.Fatalf("small manager reservations=%d, want 1", got)
	}
	if got := largeManager.Snapshot().Reservations; got != 0 {
		t.Fatalf("large manager reservations=%d, want 0", got)
	}
}

func TestRequestAwareManagerFreshObservationsRecomputeEveryReservation(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	manager := newRequestAwareTestManager(7_000)
	input := requestAwareManagerInput()
	input.TPSValid = false
	cost := requestAwareManagerCost(500, 100)
	observation := domain.VirtualState{
		PhysicalKVUpper: 7_000, ActiveKVUpper: 7_000, DecodeSequences: 4, ActiveContextTokens: 7_000,
	}

	first := manager.DecideRequestAwareAndReserve(time.Unix(1, 0), "first", cost, 500, policy, input)
	reconcileRequestAwareManagerState(t, manager, observation)
	second := manager.DecideRequestAwareAndReserve(time.Unix(2, 0), "second", cost, 500, policy, input)
	reconcileRequestAwareManagerState(t, manager, observation)
	third := manager.DecideRequestAwareAndReserve(time.Unix(3, 0), "third", cost, 500, policy, input)
	reconcileRequestAwareManagerState(t, manager, observation)
	fourth := manager.DecideRequestAwareAndReserve(time.Unix(4, 0), "fourth", cost, 500, policy, input)
	if !first.Reserved || !second.Reserved || !third.Reserved || fourth.Reserved ||
		fourth.Decision.Action != RequestAwareHardProtect || fourth.Decision.Reason != RequestAwareReasonKV {
		t.Fatalf("burst decisions first=%+v second=%+v third=%+v fourth=%+v", first, second, third, fourth)
	}
	if got := manager.Snapshot().Reservations; got != 3 {
		t.Fatalf("burst reservations=%d, want 3", got)
	}
}

func TestRequestAwareManagerUsesExistingTerminalLifecycle(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	manager := newRequestAwareTestManager(5_000)
	result := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "lifecycle", requestAwareManagerCost(500, 100), 500, policy, requestAwareManagerInput(),
	)
	if !result.Reserved || !result.DecisionManagerSequenceValid ||
		result.DecisionManagerSequence != manager.EventSequence() {
		t.Fatalf("request-aware decision sequence=%+v manager=%d", result, manager.EventSequence())
	}
	if !manager.MarkForwarded("lifecycle") || !manager.Terminate("lifecycle", TerminalCompleted) {
		t.Fatalf("request-aware lifecycle result=%+v", result)
	}
	if got := manager.Snapshot().Reservations; got != 0 {
		t.Fatalf("terminal reservation count=%d, want 0", got)
	}
}

func TestNilRequestAwareManagerDoesNotClaimSequenceAuthority(t *testing.T) {
	var manager *Manager
	result := manager.DecideRequestAware(
		time.Unix(1, 0), "nil-manager", requestAwareManagerCost(500, 100), 500,
		newRequestAwareTestPolicy(t), requestAwareManagerInput(),
	)
	if result.Decision.Reason != RequestAwareReasonUnavailable || result.DecisionManagerSequenceValid {
		t.Fatalf("nil manager result=%+v, want unavailable without sequence authority", result)
	}
}

func TestV0127RequestAwareManagerIdleRegularBurstDoesNotCreateDecodeUsers(t *testing.T) {
	const kib = int64(1024)
	policy := newPrefillRequestAwareTestPolicy(t)
	manager := NewManager("request-aware-test", domain.VirtualState{})
	input := RequestAwareInput{
		MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib,
	}
	cost := requestAwareManagerCost(1_298, 512)

	for index := range 8 {
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), fmt.Sprintf("regular-%d", index), cost, 1_298, policy, input,
		)
		if !result.Reserved || result.Decision.Action != RequestAwareAdmit ||
			result.Decision.EffectiveSequences != 0 ||
			result.Decision.PendingPrefillSequences != index ||
			result.Decision.PostAdmitPendingPrefillTokens != int64(index+1)*1_298 {
			t.Fatalf("idle regular burst decision %d=%+v", index, result)
		}
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 8 {
		t.Fatalf("regular burst reservations=%+v want=8", snapshot)
	}
}

func TestRequestAwareManagerCountsCompletedDecodeAcrossObservationCoverage(t *testing.T) {
	const kib = int64(1024)
	policy := newPrefillRequestAwareTestPolicy(t)
	idle := RequestAwareInput{
		MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib,
	}
	setupCompleted := func(t *testing.T) *Manager {
		t.Helper()
		manager := NewManager("request-aware-test", domain.VirtualState{})
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), "decode", requestAwareManagerCost(4*kib, 512), 4*kib, policy, idle,
		)
		if !result.Reserved || !manager.MarkForwarded("decode") || !manager.MarkPrefillComplete("decode") {
			t.Fatalf("completed Decode setup=%+v", result)
		}
		return manager
	}

	t.Run("unobserved completed Decode is counted", func(t *testing.T) {
		manager := setupCompleted(t)
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "unobserved-probe", requestAwareManagerCost(80*kib, 0), 80*kib, policy, idle,
		)
		if result.Reserved || result.Decision.Reason != RequestAwareReasonDecodeInterference ||
			result.Decision.EffectiveSequences != 1 {
			t.Fatalf("unobserved completed Decode decision=%+v", result)
		}
	})

	t.Run("absorbed completed Decode is not double counted", func(t *testing.T) {
		manager := setupCompleted(t)
		reconcileRequestAwareManagerState(t, manager, domain.VirtualState{
			PhysicalKVUpper: 4*kib + 512, ActiveKVUpper: 4*kib + 512,
			DecodeSequences: 1, ActiveContextTokens: 4*kib + 512,
		})
		observed := idle
		observed.Running = 1
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "absorbed-probe", requestAwareManagerCost(40*kib, 0), 40*kib, policy, observed,
		)
		if !result.Reserved || result.Decision.EffectiveSequences != 1 {
			t.Fatalf("absorbed completed Decode decision=%+v", result)
		}
	})

	t.Run("ambiguous completed Decode keeps the safety upper", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), "decode", requestAwareManagerCost(4*kib, 512), 4*kib, policy, idle,
		)
		if !result.Reserved || !manager.MarkForwarded("decode") {
			t.Fatalf("ambiguous Decode setup=%+v", result)
		}
		started := manager.StartSampleWindow()
		if !manager.MarkPrefillComplete("decode") {
			t.Fatal("ambiguous Decode did not complete Prefill")
		}
		if err := manager.ReconcileSample(SampleWindow{
			Observed: domain.VirtualState{
				PhysicalKVUpper: 4*kib + 512, ActiveKVUpper: 4*kib + 512,
				DecodeSequences: 1, ActiveContextTokens: 4*kib + 512,
			},
			StartedSequence:  started,
			FinishedSequence: manager.EventSequence(),
		}); err != nil {
			t.Fatalf("reconcile ambiguous Decode: %v", err)
		}
		observed := idle
		observed.Running = 1
		probe := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "ambiguous-probe", requestAwareManagerCost(40*kib, 0), 40*kib, policy, observed,
		)
		if probe.Reserved || probe.Decision.Reason != RequestAwareReasonDecodeInterference ||
			probe.Decision.EffectiveSequences != 2 {
			t.Fatalf("ambiguous completed Decode decision=%+v", probe)
		}
	})
}

func TestRequestAwareManagerKeepsPublishedRunningUntilNextObservation(t *testing.T) {
	const (
		kib      = int64(1024)
		waveSize = 12
	)
	policy := newPrefillRequestAwareTestPolicy(t)
	manager := NewManager("request-aware-test", domain.VirtualState{})
	idle := RequestAwareInput{
		MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib,
	}
	cost := requestAwareManagerCost(4*kib, 512)

	firstWave := make([]string, 0, waveSize)
	for index := range waveSize {
		requestID := fmt.Sprintf("absorbed-first-wave-%d", index)
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, int64(index)), requestID, cost, 4*kib, policy, idle,
		)
		if !result.Reserved || !manager.MarkForwarded(requestID) || !manager.MarkPrefillComplete(requestID) {
			t.Fatalf("first wave %d lifecycle=%+v", index, result)
		}
		firstWave = append(firstWave, requestID)
	}
	observedKV := int64(waveSize) * (4*kib + 512)
	reconcileRequestAwareManagerState(t, manager, domain.VirtualState{
		PhysicalKVUpper: observedKV, ActiveKVUpper: observedKV,
		DecodeSequences: waveSize, ActiveContextTokens: observedKV,
	})
	for _, requestID := range firstWave {
		if !manager.Terminate(requestID, TerminalCompleted) {
			t.Fatalf("completed first wave terminal failed for %s", requestID)
		}
	}

	stale := idle
	stale.Running = waveSize
	secondWave := make([]string, 0, waveSize)
	protected := 0
	firstEffective := -1
	for index := range waveSize {
		requestID := fmt.Sprintf("stale-second-wave-%d", index)
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(2, int64(index)), requestID, cost, 4*kib, policy, stale,
		)
		if index == 0 {
			firstEffective = result.Decision.EffectiveSequences
		}
		if result.Reserved {
			secondWave = append(secondWave, requestID)
			continue
		}
		if result.Decision.Reason == RequestAwareReasonDecodeInterference {
			protected++
		}
	}
	for _, requestID := range secondWave {
		if !manager.Terminate(requestID, TerminalExpired) {
			t.Fatalf("second-wave cleanup failed for %s", requestID)
		}
	}
	if firstEffective != waveSize || len(secondWave)+protected != waveSize {
		t.Fatalf("stale published running was rewritten: effective=%d admitted=%d protected=%d, want effective %d",
			firstEffective, len(secondWave), protected, waveSize)
	}
}

func TestRequestAwareManagerDoesNotCreateCompletionCredits(t *testing.T) {
	const kib = int64(1024)
	policy := newPrefillRequestAwareTestPolicy(t)
	input := RequestAwareInput{
		MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib, Running: 1,
	}
	probeEffective := func(t *testing.T, manager *Manager, requestID string) int {
		t.Helper()
		result := manager.DecideRequestAware(
			time.Unix(10, 0), requestID, requestAwareManagerCost(1, 0), 1, policy, input,
		)
		if result.Decision.Action != RequestAwareAdmit {
			t.Fatalf("probe %s decision=%+v", requestID, result)
		}
		return result.Decision.EffectiveSequences
	}

	t.Run("completed terminal preserves the still-published sample", func(t *testing.T) {
		manager := setupV0128AbsorbedDecode(t, policy)
		if !manager.Terminate("absorbed-decode", TerminalCompleted) {
			t.Fatal("completed terminal failed")
		}
		if got := probeEffective(t, manager, "completed-terminal-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want stale observed 1 until the next sample", got)
		}
	})

	t.Run("completion credit belongs only to its published observation", func(t *testing.T) {
		manager := setupV0128UnabsorbedDecode(t, policy, "sequence-scoped-decode")
		started := manager.StartSampleWindow()
		if err := manager.ReconcileSample(SampleWindow{
			Observed: domain.VirtualState{
				PhysicalKVUpper: 4*kib + 512, ActiveKVUpper: 4*kib + 512,
				DecodeSequences: 1, ActiveContextTokens: 4*kib + 512,
			},
			StartedSequence: started, FinishedSequence: manager.EventSequence(), ObservationSequence: 2,
		}); err != nil {
			t.Fatalf("sequence-scoped reconciliation: %v", err)
		}
		if !manager.Terminate("sequence-scoped-decode", TerminalCompleted) {
			t.Fatal("sequence-scoped terminal failed")
		}

		decisionFor := func(sequence uint64, requestID string) RequestAwareManagerResult {
			t.Helper()
			observation := input
			observation.ObservationSequence = sequence
			return manager.DecideRequestAware(
				time.Unix(10, 0), requestID, requestAwareManagerCost(1, 0), 1, policy, observation,
			)
		}
		mismatched := decisionFor(1, "mismatched-observation-probe")
		if mismatched.Decision.Action != RequestAwareAdmit || mismatched.Decision.EffectiveSequences != 1 {
			t.Fatalf("mismatched observation decision=%+v, want raw running preserved", mismatched)
		}
		matched := decisionFor(2, "matched-observation-probe")
		if matched.Decision.Action != RequestAwareAdmit || matched.Decision.EffectiveSequences != 1 {
			t.Fatalf("matched observation decision=%+v, want raw running preserved", matched)
		}
	})

	t.Run("sample before terminal retains credit", func(t *testing.T) {
		manager := setupV0128AbsorbedDecode(t, policy)
		reconcileRequestAwareManagerState(t, manager, domain.VirtualState{
			PhysicalKVUpper: 4*kib + 512, ActiveKVUpper: 4*kib + 512,
			DecodeSequences: 1, ActiveContextTokens: 4*kib + 512,
		})
		if !manager.Terminate("absorbed-decode", TerminalCompleted) {
			t.Fatal("completed terminal after sample failed")
		}
		if got := probeEffective(t, manager, "before-terminal-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want sample value preserved after terminal", got)
		}
	})

	t.Run("absorbed lifecycle without observed active resources creates no credit", func(t *testing.T) {
		manager := setupV0128UnabsorbedDecode(t, policy, "buffered-complete-decode")
		reconcileRequestAwareManagerState(t, manager, domain.VirtualState{})
		if !manager.Terminate("buffered-complete-decode", TerminalCompleted) {
			t.Fatal("buffered completed terminal failed")
		}
		if got := probeEffective(t, manager, "buffered-complete-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want unrelated published running preserved", got)
		}
	})

	t.Run("preexisting backend Decode cannot be attributed to a local completion", func(t *testing.T) {
		const externalTokens = 4*kib + 512
		external := domain.VirtualState{
			PhysicalKVUpper: externalTokens, ActiveKVUpper: externalTokens,
			DecodeSequences: 1, ActiveContextTokens: externalTokens,
		}
		manager := NewManager("request-aware-test", external)
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), "local-behind-external", requestAwareManagerCost(4*kib, 512), 4*kib, policy,
			RequestAwareInput{
				MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib, Running: 1,
			},
		)
		if !result.Reserved || !manager.MarkForwarded("local-behind-external") ||
			!manager.MarkPrefillComplete("local-behind-external") {
			t.Fatalf("preexisting Decode setup=%+v", result)
		}
		reconcileRequestAwareManagerState(t, manager, external)
		if !manager.Terminate("local-behind-external", TerminalCompleted) {
			t.Fatal("preexisting Decode local terminal failed")
		}
		if got := probeEffective(t, manager, "preexisting-decode-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want preexisting backend Decode preserved", got)
		}
	})

	t.Run("observed local Prefill is not double-counted after Prefill completion", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), "observed-prefill-decode", requestAwareManagerCost(4*kib, 512), 4*kib, policy,
			RequestAwareInput{MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib},
		)
		if !result.Reserved || !manager.MarkForwarded("observed-prefill-decode") {
			t.Fatalf("observed Prefill setup=%+v", result)
		}
		observed := domain.VirtualState{
			PhysicalKVUpper: 4 * kib, ActiveKVUpper: 4 * kib,
			DecodeSequences: 1, ActiveContextTokens: 4 * kib,
		}
		reconcileRequestAwareManagerState(t, manager, observed)
		if !manager.MarkPrefillComplete("observed-prefill-decode") {
			t.Fatal("observed Prefill completion failed")
		}
		reconcileRequestAwareManagerState(t, manager, observed)
		if !manager.Terminate("observed-prefill-decode", TerminalCompleted) {
			t.Fatal("observed Prefill terminal failed")
		}
		if got := probeEffective(t, manager, "observed-prefill-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want stale observed Decode preserved", got)
		}
	})

	t.Run("unchanged external Decode cannot absorb a forwarded Prefill", func(t *testing.T) {
		const externalTokens = 4*kib + 512
		external := domain.VirtualState{
			PhysicalKVUpper: externalTokens, ActiveKVUpper: externalTokens,
			DecodeSequences: 1, ActiveContextTokens: externalTokens,
		}
		manager := NewManager("request-aware-test", external)
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), "queued-behind-external", requestAwareManagerCost(4*kib, 512), 4*kib, policy,
			RequestAwareInput{
				MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib, Running: 1,
			},
		)
		if !result.Reserved || !manager.MarkForwarded("queued-behind-external") {
			t.Fatalf("queued external setup=%+v", result)
		}
		reconcileRequestAwareManagerState(t, manager, external)
		if !manager.MarkPrefillComplete("queued-behind-external") {
			t.Fatal("queued external Prefill completion failed")
		}
		reconcileRequestAwareManagerState(t, manager, external)
		if !manager.Terminate("queued-behind-external", TerminalCompleted) {
			t.Fatal("queued external terminal failed")
		}
		if got := probeEffective(t, manager, "queued-external-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want external Decode preserved", got)
		}
	})

	t.Run("overlapping sample clears credit", func(t *testing.T) {
		manager := setupV0128AbsorbedDecode(t, policy)
		started := manager.StartSampleWindow()
		if !manager.Terminate("absorbed-decode", TerminalCompleted) {
			t.Fatal("overlapping terminal failed")
		}
		if err := manager.ReconcileSample(SampleWindow{
			Observed: domain.VirtualState{
				PhysicalKVUpper: 4*kib + 512, ActiveKVUpper: 4*kib + 512,
				DecodeSequences: 1, ActiveContextTokens: 4*kib + 512,
			},
			StartedSequence: started, FinishedSequence: manager.EventSequence(),
		}); err != nil {
			t.Fatalf("overlapping reconciliation: %v", err)
		}
		if got := probeEffective(t, manager, "overlap-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want conservative 1 for overlap", got)
		}
	})

	t.Run("sample after terminal clears credit", func(t *testing.T) {
		manager := setupV0128AbsorbedDecode(t, policy)
		if !manager.Terminate("absorbed-decode", TerminalCompleted) {
			t.Fatal("pre-sample terminal failed")
		}
		reconcileRequestAwareManagerObservation(t, manager, 1, 0)
		if got := probeEffective(t, manager, "after-terminal-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want new observed external Decode", got)
		}
	})

	t.Run("unabsorbed and ambiguous reservations create no credit", func(t *testing.T) {
		t.Run("unabsorbed", func(t *testing.T) {
			manager := setupV0128UnabsorbedDecode(t, policy, "unabsorbed-decode")
			if !manager.Terminate("unabsorbed-decode", TerminalCompleted) {
				t.Fatal("unabsorbed terminal failed")
			}
			if got := probeEffective(t, manager, "unabsorbed-probe"); got != 1 {
				t.Fatalf("effective Decode sequences=%d, want observed 1", got)
			}
		})
		t.Run("ambiguous", func(t *testing.T) {
			manager := NewManager("request-aware-test", domain.VirtualState{})
			result := manager.DecideRequestAwareAndReserve(
				time.Unix(1, 0), "ambiguous-decode", requestAwareManagerCost(4*kib, 512), 4*kib, policy,
				RequestAwareInput{MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib},
			)
			if !result.Reserved || !manager.MarkForwarded("ambiguous-decode") {
				t.Fatalf("ambiguous setup=%+v", result)
			}
			started := manager.StartSampleWindow()
			if !manager.MarkPrefillComplete("ambiguous-decode") {
				t.Fatal("ambiguous Prefill completion failed")
			}
			if err := manager.ReconcileSample(SampleWindow{
				Observed: domain.VirtualState{
					PhysicalKVUpper: 4*kib + 512, ActiveKVUpper: 4*kib + 512,
					DecodeSequences: 1, ActiveContextTokens: 4*kib + 512,
				},
				StartedSequence: started, FinishedSequence: manager.EventSequence(),
			}); err != nil {
				t.Fatalf("ambiguous reconciliation: %v", err)
			}
			if !manager.Terminate("ambiguous-decode", TerminalCompleted) {
				t.Fatal("ambiguous terminal failed")
			}
			if got := probeEffective(t, manager, "ambiguous-probe"); got != 1 {
				t.Fatalf("effective Decode sequences=%d, want conservative 1", got)
			}
		})
	})

	nonCompleted := []TerminalCause{
		TerminalLocalQoSReject, TerminalClientCancelled, TerminalClientDisconnected,
		TerminalUpstreamFailure, TerminalTimeout, TerminalExpired,
	}
	for _, cause := range nonCompleted {
		t.Run("no credit for "+string(cause), func(t *testing.T) {
			manager := setupV0128AbsorbedDecode(t, policy)
			if !manager.Terminate("absorbed-decode", cause) {
				t.Fatalf("terminal %s failed", cause)
			}
			if got := probeEffective(t, manager, "non-completed-probe-"+string(cause)); got != 1 {
				t.Fatalf("terminal %s effective Decode sequences=%d, want observed 1", cause, got)
			}
		})
	}

	t.Run("duplicate terminal cannot duplicate credit", func(t *testing.T) {
		manager := setupV0128AbsorbedDecode(t, policy)
		if !manager.Terminate("absorbed-decode", TerminalCompleted) ||
			manager.Terminate("absorbed-decode", TerminalCompleted) {
			t.Fatal("duplicate terminal idempotence failed")
		}
		if got := probeEffective(t, manager, "duplicate-terminal-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want raw running without completion credit", got)
		}
	})

	t.Run("epoch rebase clears credit", func(t *testing.T) {
		manager := setupV0128AbsorbedDecode(t, policy)
		if !manager.Terminate("absorbed-decode", TerminalCompleted) {
			t.Fatal("pre-rebase terminal failed")
		}
		manager.InvalidateEpoch()
		if err := manager.RebaseEpoch(domain.VirtualState{DecodeSequences: 1}); err != nil {
			t.Fatalf("rebase: %v", err)
		}
		if got := probeEffective(t, manager, "rebase-probe"); got != 1 {
			t.Fatalf("effective Decode sequences=%d, want rebased observed 1", got)
		}
	})
}

func TestRequestAwareManagerDoesNotRetainCompletedReservationState(t *testing.T) {
	const requests = 4_097
	policy := newPrefillRequestAwareTestPolicy(t)
	manager := NewManager("request-aware-test", domain.VirtualState{})
	idle := RequestAwareInput{MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * 1024}
	requestIDs := make([]string, 0, requests)
	for index := range requests {
		requestID := fmt.Sprintf("bounded-credit-%d", index)
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, int64(index)), requestID, requestAwareManagerCost(1, 0), 1, policy, idle,
		)
		if !result.Reserved || !manager.MarkForwarded(requestID) || !manager.MarkPrefillComplete(requestID) {
			t.Fatalf("bounded setup %d=%+v", index, result)
		}
		requestIDs = append(requestIDs, requestID)
	}
	reconcileRequestAwareManagerState(t, manager, domain.VirtualState{
		PhysicalKVUpper: requests, ActiveKVUpper: requests,
		DecodeSequences: requests, ActiveContextTokens: requests,
	})
	for _, requestID := range requestIDs {
		if !manager.Terminate(requestID, TerminalCompleted) {
			t.Fatalf("bounded terminal failed for %s", requestID)
		}
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.RetiredReservations != 0 ||
		snapshot.RetiredEvictions != 0 || snapshot.CompletedDecodeCredits != 0 {
		t.Fatalf("completed reservations retained state=%+v", snapshot)
	}
	input := idle
	input.Running = requests
	result := manager.DecideRequestAware(
		time.Unix(2, 0), "bounded-credit-probe", requestAwareManagerCost(1, 0), 1, policy, input,
	)
	if result.Decision.EffectiveSequences != requests {
		t.Fatalf("effective Decode sequences=%d, want unchanged observed %d",
			result.Decision.EffectiveSequences, requests)
	}
}

func TestV0128ManagerObservationSequenceIsMonotonicAndRebased(t *testing.T) {
	manager := NewManager("request-aware-test", domain.VirtualState{})
	first := SampleWindow{ObservationSequence: 2}
	if err := manager.ReconcileSample(first); err != nil {
		t.Fatalf("first observation sequence: %v", err)
	}
	for _, stale := range []uint64{2, 1, 0} {
		if err := manager.ReconcileSample(SampleWindow{ObservationSequence: stale}); err == nil {
			t.Fatalf("stale observation sequence %d was accepted", stale)
		}
	}

	manager.InvalidateEpoch()
	if err := manager.RebaseEpoch(domain.VirtualState{}); err != nil {
		t.Fatalf("rebase observation sequence: %v", err)
	}
	watermark := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		StartedSequence: watermark, FinishedSequence: watermark, ObservationSequence: 1,
	}); err != nil {
		t.Fatalf("first post-rebase observation sequence: %v", err)
	}
}

func setupV0128AbsorbedDecode(t *testing.T, policy *RequestAwarePolicy) *Manager {
	t.Helper()
	manager := setupV0128UnabsorbedDecode(t, policy, "absorbed-decode")
	reconcileRequestAwareManagerState(t, manager, domain.VirtualState{
		PhysicalKVUpper: 4*1024 + 512, ActiveKVUpper: 4*1024 + 512,
		DecodeSequences: 1, ActiveContextTokens: 4*1024 + 512,
	})
	return manager
}

func setupV0128UnabsorbedDecode(t *testing.T, policy *RequestAwarePolicy, requestID string) *Manager {
	t.Helper()
	manager := NewManager("request-aware-test", domain.VirtualState{})
	result := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), requestID, requestAwareManagerCost(4*1024, 512), 4*1024, policy,
		RequestAwareInput{MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * 1024},
	)
	if !result.Reserved || !manager.MarkForwarded(requestID) || !manager.MarkPrefillComplete(requestID) {
		t.Fatalf("unabsorbed setup=%+v", result)
	}
	return manager
}

func TestRequestAwareManagerWaitingRemainsRequestSizeAware(t *testing.T) {
	const kib = int64(1024)
	policy := newPrefillRequestAwareTestPolicy(t)
	manager := NewManager("request-aware-test", domain.VirtualState{})
	reconcileRequestAwareManagerObservation(t, manager, 2, 1)
	input := RequestAwareInput{
		MetricsFresh: true, IdentityValid: true, CapacityTokens: 4 * 1024 * kib,
		Running: 2, Waiting: 1,
	}

	regular := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "regular", requestAwareManagerCost(8*kib, 64), 8*kib, policy, input,
	)
	weighted := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "weighted", requestAwareManagerCost(100*kib, 64), 100*kib, policy, input,
	)
	if !regular.Reserved || regular.Decision.Action != RequestAwareAdmit {
		t.Fatalf("waiting regular decision=%+v want admit", regular)
	}
	if weighted.Reserved || weighted.Decision.Action != RequestAwareSizeProtect ||
		weighted.Decision.Reason != RequestAwareReasonPrefillBusy {
		t.Fatalf("waiting weighted decision=%+v want Prefill busy protection", weighted)
	}
}

func TestRequestAwareManagerSeparatesPrefillInterferenceEstimateFromSafetyUpper(t *testing.T) {
	const kib = int64(1024)
	policy := newPrefillRequestAwareTestPolicy(t)
	idle := RequestAwareInput{
		MetricsFresh:   true,
		IdentityValid:  true,
		CapacityTokens: 4 * 1024 * 1024,
	}

	t.Run("classification follows interference estimate", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), "divergent-class", requestAwareManagerCost(690*kib, 0), 285*kib, policy, idle,
		)
		if !result.Reserved || result.Decision.Action != RequestAwareAdmit ||
			result.Decision.PrefillClass != RequestAwarePrefillExclusive ||
			result.Decision.EstimatedPrefillTokens != 285*kib {
			t.Fatalf("divergent class decision=%+v, want admitted 285K exclusive despite 690K safety upper", result)
		}
		if !manager.Terminate("divergent-class", TerminalExpired) {
			t.Fatal("divergent class reservation did not terminate")
		}
	})

	t.Run("idle same-snapshot work is bounded by Prefill aggregate only", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		cost := requestAwareManagerCost(240*kib, 0)
		first := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "divergent-weighted-first", cost, 99*kib, policy, idle,
		)
		second := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "divergent-weighted-second", cost, 99*kib, policy, idle,
		)
		third := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "divergent-weighted-third", cost, 99*kib, policy, idle,
		)
		if !first.Reserved || !second.Reserved || second.Decision.Action != RequestAwareAdmit ||
			second.Decision.EffectiveSequences != 0 ||
			second.Decision.PendingPrefillTokens != 99*kib ||
			second.Decision.PostAdmitPendingPrefillTokens != 198*kib ||
			third.Reserved || third.Decision.Reason != RequestAwareReasonPrefillBudget ||
			third.Decision.PressureSource != RequestAwarePressurePrefill ||
			third.Decision.PendingPrefillTokens != 198*kib ||
			third.Decision.PostAdmitPendingPrefillTokens != 297*kib {
			t.Fatalf("divergent weighted decisions first=%+v second=%+v third=%+v", first, second, third)
		}
		if !manager.Terminate("divergent-weighted-first", TerminalExpired) ||
			!manager.Terminate("divergent-weighted-second", TerminalExpired) {
			t.Fatal("divergent weighted reservations did not terminate")
		}
	})

	t.Run("exclusive estimate does not inherit quiescent safety upper", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		long := manager.DecideRequestAwareAndReserve(
			time.Unix(3, 0), "divergent-exclusive", requestAwareManagerCost(690*kib, 0), 285*kib, policy, idle,
		)
		short := manager.DecideRequestAwareAndReserve(
			time.Unix(3, 0), "short-during-divergent-exclusive", requestAwareManagerCost(32*kib, 0), 32*kib, policy, idle,
		)
		if !long.Reserved || long.Decision.PrefillClass != RequestAwarePrefillExclusive || short.Reserved ||
			short.Decision.Action != RequestAwareSizeProtect || short.Decision.Reason != RequestAwareReasonPrefillBusy {
			t.Fatalf("divergent exclusive/short decisions long=%+v short=%+v, want regular protection", long, short)
		}
		if !manager.Terminate("divergent-exclusive", TerminalExpired) {
			t.Fatal("divergent exclusive lifecycle did not terminate")
		}
	})

	t.Run("hard KV still charges safety upper", func(t *testing.T) {
		hardKVPolicy := newRequestAwareTestPolicyWithLimit(t, 983_040)
		manager := NewManager("request-aware-test", domain.VirtualState{
			PhysicalKVUpper:     300 * kib,
			ActiveKVUpper:       300 * kib,
			DecodeSequences:     1,
			ActiveContextTokens: 300 * kib,
		})
		input := idle
		input.CapacityTokens = 1024 * kib
		input.Running = 1
		input.EffectiveSequences = 1
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(4, 0), "divergent-hard-kv", requestAwareManagerCost(690*kib, 0), 99*kib, hardKVPolicy, input,
		)
		if result.Reserved || result.Decision.Action != RequestAwareHardProtect ||
			result.Decision.Reason != RequestAwareReasonKV {
			t.Fatalf("divergent hard KV decision=%+v, want safety-upper protection", result)
		}
	})

	t.Run("prefill completion releases only interference budget", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		cost := requestAwareManagerCost(690*kib, 0)
		first := manager.DecideRequestAwareAndReserve(
			time.Unix(5, 0), "divergent-prefill", cost, 285*kib, policy, idle,
		)
		if !first.Reserved || !manager.MarkForwarded("divergent-prefill") {
			t.Fatalf("divergent prefill setup=%+v", first)
		}
		blocked := manager.DecideRequestAwareAndReserve(
			time.Unix(5, 0), "divergent-prefill-blocked", cost, 285*kib, policy, idle,
		)
		if blocked.Reserved || blocked.Decision.Action != RequestAwareSizeProtect ||
			blocked.Decision.Reason != RequestAwareReasonPrefillConcurrency ||
			blocked.Decision.PendingPrefillTokens != 285*kib {
			t.Fatalf("divergent concurrent prefill=%+v, want 285K singleton protection", blocked)
		}
		if !manager.MarkPrefillComplete("divergent-prefill") || manager.MarkPrefillComplete("divergent-prefill") {
			t.Fatal("divergent prefill completion was not exact once")
		}
		afterPrefill := manager.DecideRequestAwareAndReserve(
			time.Unix(6, 0), "divergent-prefill-after", cost, 285*kib, policy, idle,
		)
		if afterPrefill.Reserved || afterPrefill.Decision.Reason != RequestAwareReasonDecodeInterference ||
			afterPrefill.Decision.PressureSource != RequestAwarePressureDecode ||
			afterPrefill.Decision.PendingPrefillTokens != 0 ||
			afterPrefill.Decision.PendingLongPrefillSequences != 0 {
			t.Fatalf("post-prefill divergent decision=%+v, want Prefill budget released but Decode protected", afterPrefill)
		}
		snapshot := manager.Snapshot()
		if snapshot.Reservations != 1 || snapshot.Virtual.Upper.ActiveKVUpper != 690*kib {
			t.Fatalf("post-prefill safety ownership=%+v, want original KV reservation retained", snapshot)
		}
		if !manager.Terminate("divergent-prefill", TerminalCompleted) {
			t.Fatal("divergent prefill reservations did not terminate")
		}
	})

	t.Run("terminal and cancellation release divergent reservation exact once", func(t *testing.T) {
		for _, cause := range []TerminalCause{TerminalClientCancelled, TerminalExpired, TerminalCompleted} {
			manager := NewManager("request-aware-test", domain.VirtualState{})
			requestID := string(cause)
			result := manager.DecideRequestAwareAndReserve(
				time.Unix(7, 0), requestID, requestAwareManagerCost(240*kib, 0), 99*kib, policy, idle,
			)
			if !result.Reserved || !manager.Terminate(requestID, cause) || manager.Terminate(requestID, cause) {
				t.Fatalf("divergent terminal cause=%s result=%+v snapshot=%+v", cause, result, manager.Snapshot())
			}
			probe := manager.DecideRequestAwareAndReserve(
				time.Unix(8, 0), requestID+"-probe", requestAwareManagerCost(240*kib, 0), 99*kib, policy, idle,
			)
			if !probe.Reserved || probe.Decision.PendingPrefillTokens != 0 {
				t.Fatalf("post-terminal cause=%s probe=%+v, want no leaked interference budget", cause, probe)
			}
			if !manager.Terminate(requestID+"-probe", TerminalExpired) {
				t.Fatalf("post-terminal cause=%s probe did not terminate", cause)
			}
		}
	})

	t.Run("missing interference metadata falls back to safety upper", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		manager.reservations["legacy-without-interference"] = reservation{
			ID:      "legacy-without-interference",
			Created: time.Unix(9, 0),
			Cost:    requestAwareManagerCost(690*kib, 0),
		}
		weighted := manager.DecideRequestAwareAndReserve(
			time.Unix(9, 0), "weighted-behind-legacy", requestAwareManagerCost(99*kib, 0), 99*kib, policy, idle,
		)
		if weighted.Reserved || weighted.Decision.Action != RequestAwareSizeProtect ||
			weighted.Decision.Reason != RequestAwareReasonPrefillExclusive ||
			weighted.Decision.PendingPrefillTokens != 690*kib ||
			weighted.Decision.PendingQuiescentPrefillSequences != 1 {
			t.Fatalf("legacy fallback decision=%+v, want conservative safety-upper quiescent protection", weighted)
		}
		if !manager.Terminate("legacy-without-interference", TerminalExpired) {
			t.Fatal("legacy fallback reservation did not terminate")
		}
	})

	t.Run("observed pending work falls back to safety upper", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{
			PhysicalKVUpper:         240 * kib,
			ActiveKVUpper:           240 * kib,
			DecodeSequences:         1,
			PendingPrefillSequences: 1,
			ActiveContextTokens:     240 * kib,
			UncachedPrefillTokens:   240 * kib,
		})
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(9, 0), "weighted-behind-observed", requestAwareManagerCost(99*kib, 0), 99*kib, policy, idle,
		)
		if result.Reserved || result.Decision.Action != RequestAwareSizeProtect ||
			result.Decision.Reason != RequestAwareReasonPrefillExclusive ||
			result.Decision.PendingPrefillSequences != 1 ||
			result.Decision.PendingUnknownPrefillSequences != 1 ||
			result.Decision.PendingPrefillTokens != 240*kib ||
			result.Decision.PostAdmitPendingPrefillTokens != 339*kib {
			t.Fatalf("observed pending fallback decision=%+v, want conservative unknown-Prefill protection", result)
		}
	})

	t.Run("512K boundary and 650K sample remain quiescent by interference estimate", func(t *testing.T) {
		for _, estimate := range []int64{512 * kib, 650 * kib} {
			manager := NewManager("request-aware-test", domain.VirtualState{})
			requestID := fmt.Sprintf("quiescent-%d", estimate)
			result := manager.DecideRequestAwareAndReserve(
				time.Unix(10, 0), requestID, requestAwareManagerCost(900*kib, 0), estimate, policy, idle,
			)
			if !result.Reserved || result.Decision.PrefillClass != RequestAwarePrefillQuiescent {
				t.Fatalf("quiescent estimate=%d decision=%+v", estimate, result)
			}
			if !manager.Terminate(requestID, TerminalExpired) {
				t.Fatalf("quiescent estimate=%d did not terminate", estimate)
			}
		}
	})
}

func TestRequestAwareManagerConcurrentBurstStopsAtHardKVWithoutPacer(t *testing.T) {
	for _, concurrency := range []int{1, 16, 64, 256} {
		t.Run(fmt.Sprintf("concurrency_%d", concurrency), func(t *testing.T) {
			policy := newRequestAwareTestPolicy(t)
			manager := newRequestAwareTestManager(7_000)
			input := requestAwareManagerInput()
			input.TPSValid = false
			cost := requestAwareManagerCost(500, 100)
			start := make(chan struct{})
			results := make([]RequestAwareManagerResult, concurrency)
			var wait sync.WaitGroup
			wait.Add(concurrency)
			for index := range concurrency {
				go func() {
					defer wait.Done()
					<-start
					results[index] = manager.DecideRequestAwareAndReserve(
						time.Unix(10, 0),
						fmt.Sprintf("concurrent-%d", index),
						cost,
						500,
						policy,
						input,
					)
				}()
			}
			close(start)
			wait.Wait()

			wantReserved := concurrency
			if wantReserved > 3 {
				wantReserved = 3
			}
			reservedIDs := make([]string, 0, wantReserved)
			for index, result := range results {
				if result.Reserved {
					reservedIDs = append(reservedIDs, fmt.Sprintf("concurrent-%d", index))
				}
			}
			snapshot := manager.Snapshot()
			if len(reservedIDs) != wantReserved || snapshot.Reservations != wantReserved ||
				snapshot.Virtual.Upper.ActiveKVUpper != 7_000+int64(wantReserved)*600 ||
				snapshot.Virtual.Upper.ActiveKVUpper > 9_000 {
				t.Fatalf("concurrent burst reserved=%d IDs=%v snapshot=%+v, want %d without oversubscription", len(reservedIDs), reservedIDs, snapshot, wantReserved)
			}
			for _, requestID := range reservedIDs {
				if !manager.Terminate(requestID, TerminalClientCancelled) {
					t.Fatalf("terminate admitted %s failed", requestID)
				}
			}
			if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.Virtual.Upper.ActiveKVUpper != 7_000 {
				t.Fatalf("concurrent burst leaked after terminal: %+v", snapshot)
			}
		})
	}
}

func TestRequestAwareManagerRebaseEpochClearsOldOwnershipAndReopens(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	manager := newRequestAwareTestManager(2_000)
	result := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "old-epoch", requestAwareManagerCost(500, 100), 500, policy, requestAwareManagerInput(),
	)
	if !result.Reserved {
		t.Fatalf("old-epoch reservation=%+v", result)
	}
	manager.InvalidateEpoch()
	if err := manager.RebaseEpoch(domain.VirtualState{
		PhysicalKVUpper:     1_200,
		ActiveKVUpper:       1_200,
		DecodeSequences:     2,
		ActiveContextTokens: 1_200,
	}); err != nil {
		t.Fatalf("RebaseEpoch: %v", err)
	}
	snapshot := manager.Snapshot()
	if !snapshot.IntakeOpen || snapshot.Reservations != 0 || snapshot.RetiredReservations != 0 ||
		snapshot.Virtual.Lower.PhysicalKVUpper != 1_200 || snapshot.Virtual.Upper.PhysicalKVUpper != 1_200 ||
		snapshot.Virtual.Upper.DecodeSequences != 2 {
		t.Fatalf("rebased manager=%+v", snapshot)
	}
	if manager.MarkForwarded("old-epoch") || manager.Terminate("old-epoch", TerminalExpired) {
		t.Fatal("old-epoch handle mutated rebased manager")
	}
	newResult := manager.DecideRequestAwareAndReserve(
		time.Unix(2, 0), "new-epoch", requestAwareManagerCost(100, 20), 100, policy, RequestAwareInput{
			MetricsFresh:       true,
			IdentityValid:      true,
			CapacityTokens:     10_000,
			Running:            2,
			EffectiveSequences: 2,
		},
	)
	if !newResult.Reserved {
		t.Fatalf("new-epoch reservation=%+v", newResult)
	}
}

func TestRequestAwareManagerAppliesAtomicLongPrefillBudgetsAndLifecycle(t *testing.T) {
	const kib = int64(1024)
	policy := newPrefillRequestAwareTestPolicy(t)
	input := RequestAwareInput{
		MetricsFresh:   true,
		IdentityValid:  true,
		CapacityTokens: 4 * 1024 * 1024,
	}

	t.Run("weighted aggregate budget", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		first := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), "weighted-first", requestAwareManagerCost(200*kib, 0), 200*kib, policy, input,
		)
		if !first.Reserved {
			t.Fatalf("first weighted request=%+v, want reservation", first)
		}
		second := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), "weighted-second", requestAwareManagerCost(100*kib, 0), 100*kib, policy, input,
		)
		if second.Decision.Action != RequestAwareSizeProtect || second.Reserved {
			t.Fatalf("post-admit 300K weighted request=%+v, want atomic 256K budget protection", second)
		}
		if !manager.Terminate("weighted-first", TerminalExpired) {
			t.Fatal("weighted reservation did not terminate")
		}
	})

	t.Run("weighted prefill blocks regular until prefill complete", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		weighted := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "weighted-qos", requestAwareManagerCost(195*kib, 0), 195*kib, policy, input,
		)
		if !weighted.Reserved || !manager.MarkForwarded("weighted-qos") {
			t.Fatalf("weighted setup=%+v, want forwarded reservation", weighted)
		}
		pending := manager.CurrentRequestAwarePending(policy)
		if pending.LongPrefillSequences != 1 || pending.PrefillSequences != 1 {
			t.Fatalf("weighted pending=%+v, want one known long Prefill", pending)
		}
		blocked := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "regular-during-weighted", requestAwareManagerCost(8*kib, 0), 8*kib, policy, input,
		)
		if blocked.Reserved || blocked.Decision.Action != RequestAwareSizeProtect ||
			blocked.Decision.Reason != RequestAwareReasonPrefillBusy {
			t.Fatalf("regular during weighted=%+v, want pre-forward Prefill protection", blocked)
		}
		if !manager.MarkPrefillComplete("weighted-qos") {
			t.Fatal("weighted Prefill did not complete")
		}
		recovered := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "regular-after-weighted", requestAwareManagerCost(8*kib, 0), 8*kib, policy, input,
		)
		if !recovered.Reserved || recovered.Decision.Action != RequestAwareAdmit {
			t.Fatalf("regular after weighted=%+v, want immediate recovery", recovered)
		}
		if !manager.Terminate("regular-after-weighted", TerminalExpired) ||
			!manager.Terminate("weighted-qos", TerminalCompleted) {
			t.Fatal("weighted QoS lifecycle did not terminate")
		}
	})

	t.Run("weighted prefill gate releases across terminal causes", func(t *testing.T) {
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
				manager := NewManager("request-aware-test", domain.VirtualState{})
				requestID := "weighted-" + string(cause)
				weighted := manager.DecideRequestAwareAndReserve(
					time.Unix(3, 0), requestID, requestAwareManagerCost(195*kib, 0), 195*kib, policy, input,
				)
				if !weighted.Reserved || !manager.MarkForwarded(requestID) {
					t.Fatalf("weighted setup cause=%s result=%+v", cause, weighted)
				}
				if pending := manager.CurrentRequestAwarePending(policy); pending.LongPrefillSequences != 1 {
					t.Fatalf("weighted pending cause=%s snapshot=%+v, want one known long Prefill", cause, pending)
				}
				if !manager.Terminate(requestID, cause) || manager.Terminate(requestID, cause) {
					t.Fatalf("weighted terminal cause=%s did not release exact once", cause)
				}
				if pending := manager.CurrentRequestAwarePending(policy); pending.LongPrefillSequences != 0 {
					t.Fatalf("weighted terminal cause=%s leaked gate: %+v", cause, pending)
				}
				probeID := requestID + "-regular"
				probe := manager.DecideRequestAwareAndReserve(
					time.Unix(4, 0), probeID, requestAwareManagerCost(8*kib, 0), 8*kib, policy, input,
				)
				if !probe.Reserved || probe.Decision.Action != RequestAwareAdmit ||
					!manager.Terminate(probeID, TerminalExpired) {
					t.Fatalf("post-terminal regular cause=%s result=%+v", cause, probe)
				}
			})
		}
	})

	t.Run("unforwarded weighted rollback releases gate", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		weighted := manager.DecideRequestAwareAndReserve(
			time.Unix(5, 0), "weighted-rollback", requestAwareManagerCost(195*kib, 0), 195*kib, policy, input,
		)
		if !weighted.Reserved {
			t.Fatalf("weighted rollback setup=%+v", weighted)
		}
		if pending := manager.CurrentRequestAwarePending(policy); pending.LongPrefillSequences != 1 {
			t.Fatalf("unforwarded weighted pending=%+v, want one atomic reservation", pending)
		}
		if !manager.Terminate("weighted-rollback", TerminalExpired) {
			t.Fatal("unforwarded weighted rollback did not terminate")
		}
		regular := manager.DecideRequestAwareAndReserve(
			time.Unix(6, 0), "regular-after-rollback", requestAwareManagerCost(8*kib, 0), 8*kib, policy, input,
		)
		if !regular.Reserved || regular.Decision.Action != RequestAwareAdmit ||
			!manager.Terminate("regular-after-rollback", TerminalExpired) {
			t.Fatalf("regular after rollback=%+v, want immediate recovery", regular)
		}
	})

	t.Run("weighted epoch rebase clears old gate", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		weighted := manager.DecideRequestAwareAndReserve(
			time.Unix(7, 0), "weighted-old-epoch", requestAwareManagerCost(195*kib, 0), 195*kib, policy, input,
		)
		if !weighted.Reserved || !manager.MarkForwarded("weighted-old-epoch") {
			t.Fatalf("weighted epoch setup=%+v", weighted)
		}
		if pending := manager.CurrentRequestAwarePending(policy); pending.LongPrefillSequences != 1 {
			t.Fatalf("weighted old epoch pending=%+v, want one known long Prefill", pending)
		}
		if !manager.InvalidateEpoch() {
			t.Fatal("weighted epoch invalidation did not close intake")
		}
		if err := manager.RebaseEpoch(domain.VirtualState{}); err != nil {
			t.Fatalf("weighted epoch rebase: %v", err)
		}
		if pending := manager.CurrentRequestAwarePending(policy); pending.LongPrefillSequences != 0 {
			t.Fatalf("weighted rebased epoch leaked gate: %+v", pending)
		}
		regular := manager.DecideRequestAwareAndReserve(
			time.Unix(8, 0), "regular-after-rebase", requestAwareManagerCost(8*kib, 0), 8*kib, policy, input,
		)
		if !regular.Reserved || regular.Decision.Action != RequestAwareAdmit ||
			!manager.Terminate("regular-after-rebase", TerminalExpired) {
			t.Fatalf("regular after weighted epoch rebase=%+v", regular)
		}
	})

	t.Run("one long prefill blocks regular until release", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		first := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "long-first", requestAwareManagerCost(300*kib, 0), 300*kib, policy, input,
		)
		if !first.Reserved {
			t.Fatalf("first 300K request=%+v, want reservation", first)
		}
		second := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "long-second", requestAwareManagerCost(300*kib, 0), 300*kib, policy, input,
		)
		if second.Decision.Action != RequestAwareSizeProtect || second.Reserved {
			t.Fatalf("second concurrent 300K request=%+v, want one-long-prefill protection", second)
		}
		short := manager.DecideRequestAwareAndReserve(
			time.Unix(2, 0), "short-during-long", requestAwareManagerCost(32*kib, 0), 32*kib, policy, input,
		)
		if short.Reserved || short.Decision.Action != RequestAwareSizeProtect ||
			short.Decision.Reason != RequestAwareReasonPrefillBusy {
			t.Fatalf("32K request during 300K prefill=%+v, want known-Prefill protection", short)
		}
		if !manager.Terminate("long-first", TerminalExpired) {
			t.Fatal("long/short reservations did not terminate")
		}
	})

	t.Run("quiescent prefill is exclusive until prefill complete", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		first := manager.DecideRequestAwareAndReserve(
			time.Unix(3, 0), "quiescent", requestAwareManagerCost(650*kib, 0), 650*kib, policy, input,
		)
		if !first.Reserved || !manager.MarkForwarded("quiescent") {
			t.Fatalf("idle 650K request=%+v, want forwarded reservation", first)
		}
		regular := manager.DecideRequestAwareAndReserve(
			time.Unix(3, 0), "small-during-quiescent", requestAwareManagerCost(8*kib, 0), 8*kib, policy, input,
		)
		if regular.Reserved || regular.Decision.Action != RequestAwareSizeProtect ||
			regular.Decision.Reason != RequestAwareReasonPrefillBusy {
			t.Fatalf("small during 650K prefill=%+v, want known-Prefill protection", regular)
		}
		if !manager.MarkPrefillComplete("quiescent") {
			t.Fatal("quiescent prefill did not complete")
		}
		secondQuiescent := manager.DecideRequestAwareAndReserve(
			time.Unix(4, 0), "quiescent-during-local-decode", requestAwareManagerCost(650*kib, 0), 650*kib, policy, input,
		)
		if secondQuiescent.Reserved || secondQuiescent.Decision.Action != RequestAwareSizeProtect ||
			secondQuiescent.Decision.Reason != RequestAwareReasonDecodeInterference ||
			secondQuiescent.Decision.PressureSource != RequestAwarePressureDecode {
			t.Fatalf("second 650K during unobserved local decode=%+v, want Decode protection", secondQuiescent)
		}
		reconcileRequestAwareManagerState(t, manager, domain.VirtualState{
			PhysicalKVUpper: 650 * kib, ActiveKVUpper: 650 * kib,
			DecodeSequences: 1, ActiveContextTokens: 650 * kib,
		})
		afterPrefill := manager.DecideRequestAwareAndReserve(
			time.Unix(4, 0), "small-after-prefill", requestAwareManagerCost(8*kib, 0), 8*kib, policy, input,
		)
		if !afterPrefill.Reserved {
			t.Fatalf("small after 650K prefill completion=%+v, want immediate recovery", afterPrefill)
		}
		if !manager.Terminate("small-after-prefill", TerminalExpired) ||
			!manager.Terminate("quiescent", TerminalExpired) {
			t.Fatal("quiescent lifecycle reservations did not terminate")
		}
	})

	t.Run("quiescent terminal releases local gate without overriding observed busy", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		first := manager.DecideRequestAwareAndReserve(
			time.Unix(5, 0), "quiescent-cancelled", requestAwareManagerCost(650*kib, 0), 650*kib, policy, input,
		)
		if !first.Reserved || !manager.MarkForwarded("quiescent-cancelled") ||
			!manager.Terminate("quiescent-cancelled", TerminalClientCancelled) {
			t.Fatalf("quiescent cancellation lifecycle=%+v", first)
		}
		observedBusy := input
		observedBusy.Running = 1
		observedBusy.EffectiveSequences = 1
		beforeIdlePoll := manager.DecideRequestAwareAndReserve(
			time.Unix(5, 0), "quiescent-before-idle-poll", requestAwareManagerCost(650*kib, 0), 650*kib, policy, observedBusy,
		)
		if beforeIdlePoll.Reserved || beforeIdlePoll.Decision.Action != RequestAwareSizeProtect ||
			beforeIdlePoll.Decision.Reason != RequestAwareReasonDecodeInterference ||
			beforeIdlePoll.Decision.PressureSource != RequestAwarePressureDecode {
			t.Fatalf("quiescent request with stale busy snapshot=%+v, want Decode protection", beforeIdlePoll)
		}
		afterIdlePoll := manager.DecideRequestAwareAndReserve(
			time.Unix(5, 0), "quiescent-after-cancel", requestAwareManagerCost(650*kib, 0), 650*kib, policy, input,
		)
		if !afterIdlePoll.Reserved {
			t.Fatalf("quiescent request after fresh idle snapshot=%+v, want recovery", afterIdlePoll)
		}
		if !manager.Terminate("quiescent-after-cancel", TerminalExpired) {
			t.Fatal("post-cancel quiescent reservation did not terminate")
		}
	})

	t.Run("concurrent long burst admits exactly one", func(t *testing.T) {
		manager := NewManager("request-aware-test", domain.VirtualState{})
		const concurrency = 64
		start := make(chan struct{})
		results := make(chan RequestAwareManagerResult, concurrency)
		var wait sync.WaitGroup
		for index := range concurrency {
			wait.Add(1)
			go func() {
				defer wait.Done()
				<-start
				results <- manager.DecideRequestAwareAndReserve(
					time.Unix(6, 0), fmt.Sprintf("long-burst-%d", index),
					requestAwareManagerCost(300*kib, 0), 300*kib, policy, input,
				)
			}()
		}
		close(start)
		wait.Wait()
		close(results)
		reserved := 0
		for result := range results {
			if result.Reserved {
				reserved++
				continue
			}
			if result.Decision.Action != RequestAwareSizeProtect {
				t.Fatalf("long burst non-reserved decision=%+v, want size protection", result)
			}
		}
		if reserved != 1 {
			t.Fatalf("long burst reservations=%d, want exactly one", reserved)
		}
		snapshot := manager.Snapshot()
		if snapshot.Reservations != 1 || snapshot.Virtual.Upper.PendingPrefillSequences != 1 ||
			snapshot.Virtual.Upper.UncachedPrefillTokens != 300*kib {
			t.Fatalf("long burst manager snapshot=%+v", snapshot)
		}
	})
}

func TestRequestAwareManagerConcurrentRebaseInvalidatesEveryOldHandle(t *testing.T) {
	for _, concurrency := range []int{1, 16, 64, 256} {
		t.Run(fmt.Sprintf("concurrency_%d", concurrency), func(t *testing.T) {
			policy := newRebaseStressRequestAwareTestPolicy(t)
			manager := NewManager("request-aware-test", domain.VirtualState{
				DecodeSequences: 4,
			})
			input := requestAwareManagerInput()
			input.TPSValid = false
			input.AggregateTPSProxy = 0
			input.MeanActiveTPSProxy = 0
			cost := requestAwareManagerCost(9, 0)
			requestIDs := make([]string, concurrency)
			for index := range concurrency {
				requestIDs[index] = fmt.Sprintf("old-epoch-%d", index)
				result := manager.DecideRequestAwareAndReserve(
					time.Unix(1, 0), requestIDs[index], cost, 1, policy, input,
				)
				if !result.Reserved {
					t.Fatalf("setup reservation %d=%+v", index, result)
				}
				if index+1 < concurrency {
					reconcileRequestAwareManagerObservation(t, manager, 4, 0)
				}
			}

			start := make(chan struct{})
			errors := make(chan error, 1)
			var wait sync.WaitGroup
			wait.Add(concurrency + 1)
			for _, requestID := range requestIDs {
				go func() {
					defer wait.Done()
					<-start
					manager.MarkForwarded(requestID)
					manager.Terminate(requestID, TerminalExpired)
				}()
			}
			go func() {
				defer wait.Done()
				<-start
				if err := manager.RebaseEpoch(domain.VirtualState{
					PhysicalKVUpper:     123,
					ActiveKVUpper:       123,
					DecodeSequences:     2,
					ActiveContextTokens: 123,
				}); err != nil {
					errors <- err
				}
			}()
			close(start)
			wait.Wait()
			close(errors)
			for err := range errors {
				t.Fatalf("concurrent RebaseEpoch: %v", err)
			}

			snapshot := manager.Snapshot()
			if !snapshot.IntakeOpen || snapshot.Reservations != 0 || snapshot.RetiredReservations != 0 ||
				snapshot.Virtual.Lower.PhysicalKVUpper != 123 || snapshot.Virtual.Upper.PhysicalKVUpper != 123 ||
				snapshot.Virtual.Upper.DecodeSequences != 2 {
				t.Fatalf("post-rebase manager=%+v", snapshot)
			}
			for _, requestID := range requestIDs {
				if manager.MarkForwarded(requestID) || manager.Terminate(requestID, TerminalExpired) {
					t.Fatalf("old handle %q mutated post-rebase state", requestID)
				}
			}
		})
	}
}

func newRequestAwareTestManager(usedTokens int64) *Manager {
	return NewManager("request-aware-test", domain.VirtualState{
		PhysicalKVUpper:     usedTokens,
		ActiveKVUpper:       usedTokens,
		DecodeSequences:     4,
		ActiveContextTokens: usedTokens,
	})
}

func newRebaseStressRequestAwareTestPolicy(t *testing.T) *RequestAwarePolicy {
	t.Helper()
	policy, err := NewRequestAwarePolicy(RequestAwareConfig{
		HardKVLimitTokens:            8_992,
		BlockSize:                    16,
		PrefillRegularTokens:         1024 * 1024,
		PrefillExclusiveTokens:       2 * 1024 * 1024,
		PrefillQuiescentTokens:       4 * 1024 * 1024,
		PrefillAggregateBudgetTokens: 2 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRequestAwarePolicy for rebase stress: %v", err)
	}
	return policy
}

func newPrefillRequestAwareTestPolicy(t *testing.T) *RequestAwarePolicy {
	t.Helper()
	return newRequestAwareTestPolicyWithLimit(t, 3_774_864)
}

func requestAwareManagerInput() RequestAwareInput {
	return RequestAwareInput{
		MetricsFresh:       true,
		IdentityValid:      true,
		CapacityTokens:     10_000,
		Running:            4,
		EffectiveSequences: 4,
		AggregateTPSProxy:  80,
		MeanActiveTPSProxy: 20,
		TPSValid:           true,
	}
}

func reconcileRequestAwareManagerObservation(t *testing.T, manager *Manager, running, waiting int) {
	t.Helper()
	reconcileRequestAwareManagerState(t, manager, domain.VirtualState{
		DecodeSequences:         running + waiting,
		PendingPrefillSequences: waiting,
	})
}

func reconcileRequestAwareManagerState(t *testing.T, manager *Manager, observed domain.VirtualState) {
	t.Helper()
	started := manager.StartSampleWindow()
	finished := manager.EventSequence()
	if err := manager.ReconcileSample(SampleWindow{
		Observed:         observed,
		StartedSequence:  started,
		FinishedSequence: finished,
	}); err != nil {
		t.Fatalf("reconcile request-aware observation=%+v: %v", observed, err)
	}
}

func requestAwareManagerCost(inputTokens, decodeTokens int64) domain.RequestCost {
	return domain.RequestCost{
		ManifestID:  "request-aware-test",
		InputTokens: inputTokens,
		KV: domain.KVIncrement{
			PhysicalKVUpper: inputTokens + decodeTokens,
			ActiveKVUpper:   inputTokens + decodeTokens,
		},
		FutureKV: domain.KVIncrement{
			PhysicalKVUpper: decodeTokens,
			ActiveKVUpper:   decodeTokens,
		},
		UncachedPrefillUpper:     inputTokens,
		DecodeHorizonUpper:       decodeTokens,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: inputTokens + decodeTokens,
		FutureContextTokensUpper: decodeTokens,
		Confidence:               1,
	}
}
