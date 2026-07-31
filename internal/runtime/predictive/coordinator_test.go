package predictive

import (
	"sync"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestCoordinatorCommitsCumulativePhaseStateIntoNextPrediction(t *testing.T) {
	now := time.Unix(8_000, 0)
	coordinator := mustCoordinator(t, 60, domain.VirtualState{})

	first := coordinator.DecideAndReserve(now, coordinatorProposal("first", 1, 2))
	second := coordinator.DecideAndReserve(now.Add(time.Millisecond), coordinatorProposal("second", 3, 4))
	third := coordinator.DecideAndReserve(now.Add(2*time.Millisecond), coordinatorProposal("third", 5, 6))

	if first.Decision.Reason != domain.ReasonFit || second.Decision.Reason != domain.ReasonFit {
		t.Fatalf("first/second reasons = %s/%s, want fit/fit", first.Decision.Reason, second.Decision.Reason)
	}
	if third.Decision.Reason != domain.ReasonNewTPSAtRisk {
		t.Fatalf("third reason = %s, want %s (scheduler=%+v)", third.Decision.Reason, domain.ReasonNewTPSAtRisk, third.Decision.Scheduler)
	}
	if first.Cost.KV != second.Cost.KV {
		t.Fatalf("identical cold costs changed: first=%+v second=%+v", first.Cost.KV, second.Cost.KV)
	}
	if second.Decision.Scheduler.AllUserTPSLower >= first.Decision.Scheduler.AllUserTPSLower {
		t.Fatalf("second prospective TPS %.3f must be below first %.3f", second.Decision.Scheduler.AllUserTPSLower, first.Decision.Scheduler.AllUserTPSLower)
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Manager.Reservations != 2 || snapshot.Cache.Requests != 2 {
		t.Fatalf("snapshot after TPS reject = %+v, want exactly two committed requests", snapshot)
	}
	if snapshot.Manager.Virtual.Upper.DecodeSequences != 2 || snapshot.Manager.Virtual.Upper.ActiveContextTokens != 24 {
		t.Fatalf("committed phase state = %+v, want two decoders and 24 active context tokens", snapshot.Manager.Virtual.Upper)
	}
}

func TestCoordinatorRollsBackCacheReferencesWhenSchedulerRejects(t *testing.T) {
	now := time.Unix(9_000, 0)
	coordinator := mustCoordinator(t, 20, domain.VirtualState{})
	before := coordinator.Snapshot()

	result := coordinator.DecideAndReserve(now, coordinatorProposal("rejected", 1, 2))
	if result.Decision.Reason != domain.ReasonNewTPSAtRisk || result.Reserved {
		t.Fatalf("decision/reserved = %s/%t, want new TPS risk/false", result.Decision.Reason, result.Reserved)
	}
	after := coordinator.Snapshot()
	if after != before {
		t.Fatalf("rejected transaction changed state: before=%+v after=%+v", before, after)
	}
}

func TestCoordinatorCacheHitAndPhaseLifecycleShareOneReservation(t *testing.T) {
	now := time.Unix(10_000, 0)
	coordinator := mustCoordinator(t, 120, domain.VirtualState{})
	analysis := coordinatorAnalysis(1, 2)

	first := coordinator.DecideAndReserve(now, AdmissionProposal{
		RequestID:          "first",
		Analysis:           analysis,
		DecodeHorizonUpper: 4,
		Confidence:         0.99,
	})
	if first.Decision.Reason != domain.ReasonFit || first.CacheHits.Certain != 0 || first.Cost.UncachedPrefillUpper != 8 {
		t.Fatalf("cold admission = %+v", first)
	}
	beforePrefill := coordinator.Snapshot().Manager.Virtual.Upper
	if beforePrefill.UncachedPrefillTokens != 8 || beforePrefill.DecodeSequences != 1 {
		t.Fatalf("prefill state = %+v, want uncached=8 decode=1", beforePrefill)
	}
	if !coordinator.MarkPrefillComplete("first") {
		t.Fatal("semantic first output must transition cache and manager state")
	}
	afterPrefill := coordinator.Snapshot()
	if afterPrefill.Manager.Virtual.Upper.UncachedPrefillTokens != 0 || afterPrefill.Manager.Virtual.Upper.DecodeSequences != 1 || afterPrefill.Cache.ActiveBlocks != 2 {
		t.Fatalf("post-prefill snapshot = %+v", afterPrefill)
	}

	second := coordinator.DecideAndReserve(now.Add(time.Second), AdmissionProposal{
		RequestID:          "second",
		Analysis:           analysis,
		DecodeHorizonUpper: 4,
		Confidence:         0.99,
	})
	if second.Decision.Reason != domain.ReasonFit || second.CacheHits.Certain != 8 || second.Cost.UncachedPrefillUpper != 0 {
		t.Fatalf("hot admission = %+v", second)
	}
	if second.Cost.KV.PhysicalKVUpper >= first.Cost.KV.PhysicalKVUpper {
		t.Fatalf("hot physical KV = %d, want below cold %d", second.Cost.KV.PhysicalKVUpper, first.Cost.KV.PhysicalKVUpper)
	}

	if !coordinator.Complete("second") || !coordinator.Complete("first") {
		t.Fatal("both coordinator completions must release manager and cache ownership")
	}
	final := coordinator.Snapshot()
	if final.Manager.Reservations != 0 || final.Manager.Virtual.Upper.DecodeSequences != 0 || final.Manager.Virtual.Upper.ActiveContextTokens != 0 || final.Cache.Requests != 0 {
		t.Fatalf("final snapshot leaked state: %+v", final)
	}
}

func TestCoordinatorRejectsProfileOrAnalysisMismatchWithoutMutation(t *testing.T) {
	identity := coordinatorModelIdentity()
	scheduler := mustCoordinatorScheduler(t, identity, 60)
	config := coordinatorConfig(identity, scheduler, domain.VirtualState{})
	config.Identity.Scheduler.PredictorVersion = "wrong-version"
	if _, err := NewCoordinator(config); err == nil {
		t.Fatal("scheduler identity mismatch must fail coordinator construction")
	}

	coordinator := mustCoordinator(t, 60, domain.VirtualState{})
	before := coordinator.Snapshot()
	proposal := coordinatorProposal("stale", 1, 2)
	proposal.Analysis.BackendEpoch = "wrong-backend-epoch"
	result := coordinator.DecideAndReserve(time.Unix(11_000, 0), proposal)
	if result.Decision.Reason != domain.ReasonTokenizerProfileUnknown || result.Reserved {
		t.Fatalf("stale analysis decision = %+v", result)
	}
	if after := coordinator.Snapshot(); after != before {
		t.Fatalf("stale analysis changed state: before=%+v after=%+v", before, after)
	}
}

func TestCoordinatorConcurrentNearTPSCapacityCommitsOnlyOne(t *testing.T) {
	coordinator := mustCoordinator(t, 60, domain.VirtualState{DecodeSequences: 1, ActiveContextTokens: 12})
	now := time.Unix(12_000, 0)
	start := make(chan struct{})
	results := make(chan AdmissionResult, 2)
	var group sync.WaitGroup
	for index, values := range [][2]byte{{1, 2}, {3, 4}} {
		group.Add(1)
		go func(index int, values [2]byte) {
			defer group.Done()
			<-start
			results <- coordinator.DecideAndReserve(now, coordinatorProposal(string(rune('a'+index)), values[0], values[1]))
		}(index, values)
	}
	close(start)
	group.Wait()
	close(results)

	fit := 0
	risk := 0
	for result := range results {
		switch result.Decision.Reason {
		case domain.ReasonFit:
			fit++
		case domain.ReasonNewTPSAtRisk:
			risk++
		default:
			t.Fatalf("unexpected concurrent decision: %+v", result)
		}
	}
	if fit != 1 || risk != 1 {
		t.Fatalf("fit/risk = %d/%d, want 1/1", fit, risk)
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Manager.Reservations != 1 || snapshot.Cache.Requests != 1 || snapshot.Manager.Virtual.Upper.DecodeSequences != 2 {
		t.Fatalf("concurrent snapshot = %+v, want one new committed request", snapshot)
	}
}

func mustCoordinator(t *testing.T, baseCompletionTPS float64, initial domain.VirtualState) *Coordinator {
	t.Helper()
	identity := coordinatorModelIdentity()
	scheduler := mustCoordinatorScheduler(t, identity, baseCompletionTPS)
	coordinator, err := NewCoordinator(coordinatorConfig(identity, scheduler, initial))
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	return coordinator
}

func coordinatorConfig(identity ModelIdentity, scheduler *LearnedScheduler, initial domain.VirtualState) CoordinatorConfig {
	return CoordinatorConfig{
		Identity: CoordinatorIdentity{
			ManifestID:   "test-profile",
			BackendEpoch: identity.BackendEpoch,
			Scheduler:    identity,
			BlockSize:    4,
		},
		Initial:             initial,
		Constraints:         testLearnedConstraints(),
		Scheduler:           scheduler,
		CacheCapacityBlocks: 64,
		CacheHashKey:        []byte("0123456789abcdef0123456789abcdef"),
	}
}

func mustCoordinatorScheduler(t *testing.T, identity ModelIdentity, baseCompletionTPS float64) *LearnedScheduler {
	t.Helper()
	profile := testLearnedProfile()
	profile.Identity = identity
	profile.BaseCompletionTPS = baseCompletionTPS
	config := testResidualConfig()
	config.Identity = identity
	return mustLearnedScheduler(t, profile, config)
}

func coordinatorModelIdentity() ModelIdentity {
	return ModelIdentity{
		ProfileID:        "coordinator-profile",
		BackendEpoch:     "coordinator-backend-1",
		PredictorVersion: "coordinator-v1",
	}
}

func coordinatorProposal(id string, digestValues ...byte) AdmissionProposal {
	return AdmissionProposal{
		RequestID:          id,
		Analysis:           coordinatorAnalysis(digestValues...),
		DecodeHorizonUpper: 4,
		Confidence:         0.99,
	}
}

func coordinatorAnalysis(digestValues ...byte) TokenBlockAnalysis {
	digests := make([]CacheBlockDigest, 0, len(digestValues))
	for _, value := range digestValues {
		digests = append(digests, testBlockDigest(value))
	}
	return TokenBlockAnalysis{
		ManifestID:       "test-profile",
		BackendEpoch:     coordinatorModelIdentity().BackendEpoch,
		BlockSize:        4,
		ExactInputTokens: int64(len(digests) * 4),
		FullBlockDigests: digests,
	}
}
