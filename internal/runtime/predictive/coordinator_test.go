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

func TestCoordinatorLearnsEligibleOutcomesBeforeLaterAdmission(t *testing.T) {
	now := time.Unix(13_000, 0)
	identity := coordinatorModelIdentity()
	scheduler := mustCoordinatorScheduler(t, identity, 96)
	coordinator, err := NewCoordinator(coordinatorConfig(identity, scheduler, learnedTestState()))
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}

	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		admittedAt := now.Add(time.Duration(index) * 10 * time.Second)
		result := coordinator.DecideAndReserve(admittedAt, coordinatorProposal(
			string(rune('k'+index)),
			byte(20+index*2),
			byte(21+index*2),
		))
		if result.Decision.Reason != domain.ReasonFit || !result.Reserved {
			t.Fatalf("training admission %d = %+v, want committed fit", index, result)
		}
		outcome := SchedulerOutcome{
			Identity:             identity,
			ObservedAt:           admittedAt.Add(time.Second),
			Attributed:           true,
			ExistingUserTPS:      result.Decision.Scheduler.ExistingUserTPSLower * 6 / 10,
			ExistingUserTPSValid: true,
			AllUserTPS:           result.Decision.Scheduler.AllUserTPSLower * 6 / 10,
			AllUserTPSValid:      true,
			TTFT:                 result.Decision.Scheduler.TTFTUpper * 3 / 2,
			TTFTValid:            true,
			TPOT:                 result.Decision.Scheduler.TPOTUpper * 3 / 2,
			TPOTValid:            true,
		}
		requestID := string(rune('k' + index))
		if index == 0 {
			wrongIdentity := outcome
			wrongIdentity.Identity.BackendEpoch = "wrong-epoch"
			if coordinator.ObserveOutcome(requestID, wrongIdentity) {
				t.Fatal("wrong-identity outcome must not be learned")
			}
		}
		if !coordinator.ObserveOutcome(requestID, outcome) {
			t.Fatalf("eligible outcome %d was not learned", index)
		}
		if coordinator.ObserveOutcome(requestID, outcome) {
			t.Fatalf("duplicate outcome %d was learned twice", index)
		}
		if !coordinator.Complete(requestID) {
			t.Fatalf("training request %d did not complete", index)
		}
	}

	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != uint64(testResidualConfig().MinimumSamples) || snapshot.Cells != 1 {
		t.Fatalf("learned scheduler snapshot = %+v", snapshot)
	}
	adverse := coordinator.DecideAndReserve(now.Add(40*time.Second), coordinatorProposal("adverse", 30, 31))
	if adverse.Decision.Reason != domain.ReasonNewTPSAtRisk || adverse.Reserved {
		t.Fatalf("learned decision = %+v, want new-user TPS risk without reservation", adverse)
	}
	if adverse.Decision.Scheduler.AllUserTPSLower >= 25 {
		t.Fatalf("learned all-user TPS = %.3f, want below target", adverse.Decision.Scheduler.AllUserTPSLower)
	}
}

func TestCoordinatorSampleAndCompletionDoNotDoubleCountPhaseState(t *testing.T) {
	now := time.Unix(14_000, 0)
	coordinator := mustCoordinator(t, 120, domain.VirtualState{})
	result := coordinator.DecideAndReserve(now, coordinatorProposal("sampled", 40, 41))
	if result.Decision.Reason != domain.ReasonFit || !result.Reserved {
		t.Fatalf("admission = %+v, want committed fit", result)
	}
	if !coordinator.MarkPrefillComplete("sampled") {
		t.Fatal("semantic first output was not applied")
	}
	watermark := coordinator.EventSequence()
	observed := coordinator.Snapshot().Manager.Virtual.Upper
	if err := coordinator.ReconcileSample(SampleWindow{
		Observed:         observed,
		StartedSequence:  watermark,
		FinishedSequence: watermark,
	}); err != nil {
		t.Fatalf("reconcile absorbed sample: %v", err)
	}
	if after := coordinator.Snapshot().Manager.Virtual; after.Lower != observed || after.Upper != observed {
		t.Fatalf("absorbed interval = %+v, want exact %+v", after, observed)
	}
	if !coordinator.Complete("sampled") {
		t.Fatal("sampled request did not complete")
	}
	if after := coordinator.Snapshot(); after.Manager.Virtual != (domain.VirtualStateInterval{}) || after.Cache.Requests != 0 {
		t.Fatalf("completion leaked phase/cache state: %+v", after)
	}
	if err := coordinator.ReconcileSample(SampleWindow{
		Observed:         observed,
		StartedSequence:  watermark,
		FinishedSequence: watermark,
	}); err != nil {
		t.Fatalf("reconcile late overlapping sample: %v", err)
	}
	if after := coordinator.Snapshot(); after.Manager.Virtual != (domain.VirtualStateInterval{}) || after.Cache.Requests != 0 {
		t.Fatalf("late sample reintroduced completed work: %+v", after)
	}
}

func TestCoordinatorTypedTerminalCausesReleaseExactlyOnce(t *testing.T) {
	causes := []TerminalCause{
		TerminalLocalQoSReject,
		TerminalClientCancelled,
		TerminalClientDisconnected,
		TerminalUpstreamFailure,
		TerminalTimeout,
		TerminalExpired,
	}
	for index, cause := range causes {
		t.Run(string(cause), func(t *testing.T) {
			coordinator := mustCoordinator(t, 120, domain.VirtualState{})
			requestID := string(rune('u' + index))
			result := coordinator.DecideAndReserve(time.Unix(15_000+int64(index), 0), coordinatorProposal(requestID, byte(50+index), byte(70+index)))
			if result.Decision.Reason != domain.ReasonFit || !result.Reserved {
				t.Fatalf("admission = %+v, want committed fit", result)
			}
			if !coordinator.Terminate(requestID, cause) {
				t.Fatalf("first %s terminal event did not release", cause)
			}
			if coordinator.Terminate(requestID, cause) {
				t.Fatalf("duplicate %s terminal event released twice", cause)
			}
			if after := coordinator.Snapshot(); after.Manager.Reservations != 0 || after.Manager.Virtual != (domain.VirtualStateInterval{}) || after.Cache.Requests != 0 {
				t.Fatalf("terminal cause %s leaked state: %+v", cause, after)
			}
		})
	}
}

func TestCoordinatorLocalQoSRejectCannotBecomeLearningOutcome(t *testing.T) {
	now := time.Unix(16_000, 0)
	identity := coordinatorModelIdentity()
	scheduler := mustCoordinatorScheduler(t, identity, 120)
	coordinator, err := NewCoordinator(coordinatorConfig(identity, scheduler, domain.VirtualState{}))
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	result := coordinator.DecideAndReserve(now, coordinatorProposal("local-reject", 80, 81))
	if result.Decision.Reason != domain.ReasonFit || !result.Reserved {
		t.Fatalf("admission = %+v, want committed fit", result)
	}
	if !coordinator.Terminate("local-reject", TerminalLocalQoSReject) {
		t.Fatal("local QoS reject did not release reservation")
	}
	outcome := SchedulerOutcome{
		Identity:             identity,
		ObservedAt:           now.Add(time.Second),
		Attributed:           true,
		ExistingUserTPS:      100,
		ExistingUserTPSValid: true,
		AllUserTPS:           100,
		AllUserTPSValid:      true,
		TTFT:                 time.Millisecond,
		TTFTValid:            true,
		TPOT:                 time.Millisecond,
		TPOTValid:            true,
	}
	if coordinator.ObserveOutcome("local-reject", outcome) {
		t.Fatal("local QoS reject was mislabeled as an upstream learning outcome")
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 0 || snapshot.Cells != 0 {
		t.Fatalf("local reject changed learned state: %+v", snapshot)
	}
}

func TestCoordinatorInvalidTerminalCauseDoesNotRelease(t *testing.T) {
	coordinator := mustCoordinator(t, 120, domain.VirtualState{})
	result := coordinator.DecideAndReserve(time.Unix(17_000, 0), coordinatorProposal("invalid-terminal", 82, 83))
	if result.Decision.Reason != domain.ReasonFit || !result.Reserved {
		t.Fatalf("admission = %+v, want committed fit", result)
	}
	if coordinator.Terminate("invalid-terminal", TerminalCause("unbounded-error-text")) {
		t.Fatal("invalid terminal cause released reservation")
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Reservations != 1 || snapshot.Cache.Requests != 1 {
		t.Fatalf("invalid cause mutated reservation: %+v", snapshot)
	}
	if !coordinator.Complete("invalid-terminal") {
		t.Fatal("cleanup completion failed")
	}
}

func TestCoordinatorConcurrentTerminalEventsReleaseOnlyOnce(t *testing.T) {
	coordinator := mustCoordinator(t, 120, domain.VirtualState{})
	result := coordinator.DecideAndReserve(time.Unix(18_000, 0), coordinatorProposal("terminal-race", 84, 85))
	if result.Decision.Reason != domain.ReasonFit || !result.Reserved {
		t.Fatalf("admission = %+v, want committed fit", result)
	}
	start := make(chan struct{})
	results := make(chan bool, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		<-start
		results <- coordinator.Complete("terminal-race")
	}()
	go func() {
		defer group.Done()
		<-start
		results <- coordinator.Terminate("terminal-race", TerminalClientCancelled)
	}()
	close(start)
	group.Wait()
	close(results)
	succeeded := 0
	for released := range results {
		if released {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("successful terminal events = %d, want 1", succeeded)
	}
	if after := coordinator.Snapshot(); after.Manager.Reservations != 0 || after.Manager.Virtual != (domain.VirtualStateInterval{}) || after.Cache.Requests != 0 {
		t.Fatalf("terminal race leaked state: %+v", after)
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
