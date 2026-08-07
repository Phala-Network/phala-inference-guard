package predictive

import (
	"fmt"
	"sync"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestRequestAwareManagerRequestSizeChangesRealReservation(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	smallManager := newRequestAwareTestManager(2_000)
	largeManager := newRequestAwareTestManager(2_000)
	input := requestAwareManagerInput()
	input.Waiting = 1

	small := smallManager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "small", requestAwareManagerCost(800, 100), 400, policy, input,
	)
	large := largeManager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "large", requestAwareManagerCost(800, 100), 800, policy, input,
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

func TestRequestAwareManagerBurstRecomputesAfterEveryReservation(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	manager := newRequestAwareTestManager(7_000)
	input := requestAwareManagerInput()
	input.TPSValid = false
	cost := requestAwareManagerCost(500, 100)

	first := manager.DecideRequestAwareAndReserve(time.Unix(1, 0), "first", cost, 500, policy, input)
	second := manager.DecideRequestAwareAndReserve(time.Unix(2, 0), "second", cost, 500, policy, input)
	third := manager.DecideRequestAwareAndReserve(time.Unix(3, 0), "third", cost, 500, policy, input)
	fourth := manager.DecideRequestAwareAndReserve(time.Unix(4, 0), "fourth", cost, 500, policy, input)
	if !first.Reserved || !second.Reserved || !third.Reserved || fourth.Reserved ||
		fourth.Decision.Action != RequestAwareHardProtect || fourth.Decision.Reason != RequestAwareReasonKV {
		t.Fatalf("burst decisions first=%+v second=%+v third=%+v fourth=%+v", first, second, third, fourth)
	}
	if got := manager.Snapshot().Reservations; got != 3 {
		t.Fatalf("burst reservations=%d, want 3", got)
	}
}

func TestRequestAwareManagerForecastIncludesEarlierSameSnapshotReservation(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	manager := newRequestAwareTestManager(2_000)
	input := requestAwareManagerInput()
	input.MeanActiveTPSProxy = 19.8
	input.AggregateTPSProxy = 79.2
	cost := requestAwareManagerCost(300, 100)

	first := manager.DecideRequestAwareAndReserve(time.Unix(1, 0), "first-tps", cost, 300, policy, input)
	second := manager.DecideRequestAwareAndReserve(time.Unix(1, 0), "second-tps", cost, 300, policy, input)
	if !first.Reserved || first.Decision.Action != RequestAwareAdmit || second.Reserved || second.Decision.Action != RequestAwareSizeProtect {
		t.Fatalf("same-snapshot TPS forecast first=%+v second=%+v", first, second)
	}
	if first.Decision.EffectiveSequences != 4 || second.Decision.EffectiveSequences != 5 {
		t.Fatalf("same-snapshot effective sequences first=%+v second=%+v", first.Decision, second.Decision)
	}
}

func TestRequestAwareManagerHealthyBurstForecastIncludesEarlierReservation(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	manager := newRequestAwareTestManager(2_000)
	input := requestAwareManagerInput()
	cost := requestAwareManagerCost(300, 100)

	first := manager.DecideRequestAwareAndReserve(time.Unix(1, 0), "first-healthy", cost, 300, policy, input)
	second := manager.DecideRequestAwareAndReserve(time.Unix(1, 0), "second-healthy", cost, 300, policy, input)
	if !first.Reserved || first.Decision.Action != RequestAwareAdmit || second.Reserved || second.Decision.Action != RequestAwareSizeProtect {
		t.Fatalf("healthy same-snapshot manager forecast first=%+v second=%+v", first, second)
	}
	if first.Decision.EffectiveSequences != 4 || second.Decision.EffectiveSequences != 5 {
		t.Fatalf("healthy effective sequences first=%+v second=%+v", first.Decision, second.Decision)
	}
}

func TestRequestAwareManagerUsesExistingTerminalLifecycle(t *testing.T) {
	policy := newRequestAwareTestPolicy(t)
	manager := newRequestAwareTestManager(5_000)
	result := manager.DecideRequestAwareAndReserve(
		time.Unix(1, 0), "lifecycle", requestAwareManagerCost(500, 100), 500, policy, requestAwareManagerInput(),
	)
	if !result.Reserved || !manager.MarkForwarded("lifecycle") || !manager.Terminate("lifecycle", TerminalCompleted) {
		t.Fatalf("request-aware lifecycle result=%+v", result)
	}
	if got := manager.Snapshot().Reservations; got != 0 {
		t.Fatalf("terminal reservation count=%d, want 0", got)
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
		busy := idle
		busy.Running = 1
		busy.EffectiveSequences = 1
		result := manager.DecideRequestAwareAndReserve(
			time.Unix(1, 0), "divergent-class", requestAwareManagerCost(690*kib, 0), 285*kib, policy, busy,
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

	t.Run("aggregate budget sums interference estimates", func(t *testing.T) {
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
		if !first.Reserved || !second.Reserved || second.Decision.PostAdmitPendingPrefillTokens != 198*kib ||
			third.Reserved || third.Decision.Action != RequestAwareSizeProtect ||
			third.Decision.Reason != RequestAwareReasonPrefillBudget ||
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
		if !long.Reserved || long.Decision.PrefillClass != RequestAwarePrefillExclusive || !short.Reserved {
			t.Fatalf("divergent exclusive/short decisions long=%+v short=%+v", long, short)
		}
		if !manager.Terminate("short-during-divergent-exclusive", TerminalExpired) ||
			!manager.Terminate("divergent-exclusive", TerminalExpired) {
			t.Fatal("divergent exclusive lifecycle did not terminate")
		}
	})

	t.Run("hard KV still charges safety upper", func(t *testing.T) {
		hardKVPolicy := newRequestAwareTestPolicyWithLimits(t, 819_200, 983_040)
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
		if !afterPrefill.Reserved || afterPrefill.Decision.PendingPrefillTokens != 0 ||
			afterPrefill.Decision.PendingLongPrefillSequences != 0 {
			t.Fatalf("post-prefill divergent decision=%+v, want interference budget released", afterPrefill)
		}
		snapshot := manager.Snapshot()
		if snapshot.Reservations != 2 || snapshot.Virtual.Upper.ActiveKVUpper != 2*690*kib {
			t.Fatalf("post-prefill safety ownership=%+v, want both KV reservations retained", snapshot)
		}
		if !manager.Terminate("divergent-prefill-after", TerminalExpired) ||
			!manager.Terminate("divergent-prefill", TerminalCompleted) {
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
			ID:           "legacy-without-interference",
			Created:      time.Unix(9, 0),
			Cost:         requestAwareManagerCost(690*kib, 0),
			Assimilation: assimilationUnabsorbed,
		}
		short := manager.DecideRequestAwareAndReserve(
			time.Unix(9, 0), "short-behind-legacy", requestAwareManagerCost(32*kib, 0), 32*kib, policy, idle,
		)
		if short.Reserved || short.Decision.Action != RequestAwareSizeProtect ||
			short.Decision.Reason != RequestAwareReasonPrefillExclusive ||
			short.Decision.PendingPrefillTokens != 690*kib ||
			short.Decision.PendingQuiescentPrefillSequences != 1 {
			t.Fatalf("legacy fallback decision=%+v, want conservative safety-upper quiescent protection", short)
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
			result.Decision.Reason != RequestAwareReasonPrefillBudget ||
			result.Decision.PendingPrefillSequences != 1 ||
			result.Decision.PendingPrefillTokens != 240*kib ||
			result.Decision.PostAdmitPendingPrefillTokens != 339*kib {
			t.Fatalf("observed pending fallback decision=%+v, want conservative weighted budget protection", result)
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

func TestRequestAwareManagerConcurrentBurstNeverOversubscribesOrLeaks(t *testing.T) {
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

	t.Run("one long prefill while short remains work conserving", func(t *testing.T) {
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
		if !short.Reserved {
			t.Fatalf("32K request during 300K prefill=%+v, want ordinary admission", short)
		}
		if !manager.Terminate("short-during-long", TerminalExpired) || !manager.Terminate("long-first", TerminalExpired) {
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
		blocked := manager.DecideRequestAwareAndReserve(
			time.Unix(3, 0), "small-blocked", requestAwareManagerCost(8*kib, 0), 8*kib, policy, input,
		)
		if blocked.Decision.Action != RequestAwareSizeProtect || blocked.Reserved {
			t.Fatalf("small during 650K prefill=%+v, want exclusive prefill protection", blocked)
		}
		if !manager.MarkPrefillComplete("quiescent") {
			t.Fatal("quiescent prefill did not complete")
		}
		secondQuiescent := manager.DecideRequestAwareAndReserve(
			time.Unix(4, 0), "quiescent-during-local-decode", requestAwareManagerCost(650*kib, 0), 650*kib, policy, input,
		)
		if secondQuiescent.Reserved || secondQuiescent.Decision.Action != RequestAwareSizeProtect ||
			secondQuiescent.Decision.Reason != RequestAwareReasonPrefillBusy {
			t.Fatalf("second 650K during unobserved local decode=%+v, want busy protection", secondQuiescent)
		}
		afterPrefill := manager.DecideRequestAwareAndReserve(
			time.Unix(4, 0), "small-after-prefill", requestAwareManagerCost(8*kib, 0), 8*kib, policy, input,
		)
		if !afterPrefill.Reserved {
			t.Fatalf("small after 650K prefill completion=%+v, want immediate recovery", afterPrefill)
		}
		if !manager.Terminate("small-after-prefill", TerminalExpired) || !manager.Terminate("quiescent", TerminalExpired) {
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
			beforeIdlePoll.Decision.Reason != RequestAwareReasonPrefillBusy {
			t.Fatalf("quiescent request with stale busy snapshot=%+v, want prefill busy protection", beforeIdlePoll)
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
			policy := newRequestAwareTestPolicy(t)
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
					time.Unix(1, 0), requestIDs[index], cost, 3, policy, input,
				)
				if !result.Reserved {
					t.Fatalf("setup reservation %d=%+v", index, result)
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

func newPrefillRequestAwareTestPolicy(t *testing.T) *RequestAwarePolicy {
	t.Helper()
	return newRequestAwareTestPolicyWithLimits(t, 2_516_576, 3_774_864)
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
