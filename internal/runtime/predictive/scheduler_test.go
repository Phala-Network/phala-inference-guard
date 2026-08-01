package predictive

import (
	"math"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type blockingOutcomeScheduler struct {
	safeScheduler
	entered  chan struct{}
	release  chan struct{}
	observed atomic.Uint64
}

func (s *blockingOutcomeScheduler) Observe(SchedulerPrediction, SchedulerOutcome) error {
	close(s.entered)
	<-s.release
	s.observed.Add(1)
	return nil
}

func TestLearnedSchedulerChangesAdmissionWithCurrentMetricsHeldConstant(t *testing.T) {
	now := time.Unix(3_000, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	constraints := testLearnedConstraints()
	state := learnedTestState()
	cost := learnedTestCost()

	coldManager := NewManager("test-profile", state, constraints, scheduler)
	cold := coldManager.DecideAndReserve(now, "cold", cost)
	if cold.Reason != domain.ReasonExistingTPSAtRisk {
		t.Fatalf("cold reason = %s, want %s (estimate=%+v)", cold.Reason, domain.ReasonExistingTPSAtRisk, cold.Scheduler)
	}

	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index+1)*time.Second))
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe healthy sample %d: %v", index, err)
		}
	}

	learnedManager := NewManager("test-profile", state, constraints, scheduler)
	learned := learnedManager.DecideAndReserve(now.Add(5*time.Second), "learned", cost)
	if learned.Reason != domain.ReasonFit {
		t.Fatalf("learned reason = %s, want fit (estimate=%+v)", learned.Reason, learned.Scheduler)
	}
	if learned.Projection != cold.Projection {
		t.Fatalf("current KV projection changed: cold=%+v learned=%+v", cold.Projection, learned.Projection)
	}
	if learned.Scheduler.AllUserTPSLower <= cold.Scheduler.AllUserTPSLower {
		t.Fatalf("learned TPS %.3f did not exceed cold %.3f", learned.Scheduler.AllUserTPSLower, cold.Scheduler.AllUserTPSLower)
	}
}

func TestAdverseLearnedResidualChangesFitToTPSRisk(t *testing.T) {
	now := time.Unix(4_000, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 96
	scheduler := mustLearnedScheduler(t, profile, testResidualConfig())
	state := learnedTestState()
	cost := learnedTestCost()

	priorManager := NewManager("test-profile", state, testLearnedConstraints(), scheduler)
	prior := priorManager.DecideAndReserve(now, "prior", cost)
	if prior.Reason != domain.ReasonFit {
		t.Fatalf("prior reason = %s, want fit", prior.Reason)
	}

	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index+1)*time.Second))
		outcome.ExistingUserTPS = prediction.Prior.ExistingUserTPSLower * 0.60
		outcome.AllUserTPS = prediction.Prior.AllUserTPSLower * 0.60
		outcome.TTFT = prediction.Prior.TTFTUpper * 3 / 2
		outcome.TPOT = prediction.Prior.TPOTUpper * 3 / 2
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe adverse sample %d: %v", index, err)
		}
	}

	learnedManager := NewManager("test-profile", state, testLearnedConstraints(), scheduler)
	learned := learnedManager.DecideAndReserve(now.Add(5*time.Second), "adverse", cost)
	if learned.Reason != domain.ReasonExistingTPSAtRisk && learned.Reason != domain.ReasonNewTPSAtRisk {
		t.Fatalf("adverse reason = %s, want TPS risk (estimate=%+v)", learned.Reason, learned.Scheduler)
	}
}

func TestManagerStoresPredictionAndLearnsFromOutcomeExactlyOnce(t *testing.T) {
	now := time.Unix(5_000, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 96
	scheduler := mustLearnedScheduler(t, profile, testResidualConfig())
	manager := NewManager("test-profile", learnedTestState(), testLearnedConstraints(), scheduler)

	result := manager.decideAndReserve(now, "owned", learnedTestCost())
	if result.Decision.Reason != domain.ReasonFit {
		t.Fatalf("decision reason = %s, want fit", result.Decision.Reason)
	}
	stored := result.Prediction
	if stored.Identity != testPredictorIdentity() || stored.PredictedAt != now {
		t.Fatalf("admission prediction = %+v", stored)
	}
	if !manager.MarkForwarded("owned") {
		t.Fatal("owned prediction was not marked forwarded before outcome")
	}
	outcome := healthyLearnedOutcome(stored, now.Add(time.Second))
	if !manager.ObserveOutcome("owned", outcome) {
		t.Fatal("first matching outcome must be learned")
	}
	if manager.ObserveOutcome("owned", outcome) {
		t.Fatal("duplicate matching outcome must not be learned twice")
	}
	if got := scheduler.Snapshot().SamplesAccepted; got != 1 {
		t.Fatalf("accepted samples = %d, want 1", got)
	}
	if !manager.Complete("owned") {
		t.Fatal("completion must still release the reservation")
	}
	if manager.ObserveOutcome("owned", outcome) {
		t.Fatal("completed duplicate outcome must not be learned twice")
	}
}

func TestTerminalOutcomeCommitAndReservationReleaseAreAtomicAgainstReconciliation(t *testing.T) {
	scheduler := &blockingOutcomeScheduler{entered: make(chan struct{}), release: make(chan struct{})}
	manager := NewManager("test-profile", domain.VirtualState{}, testConstraints(), scheduler)
	result := manager.decideAndReserve(time.Unix(5_500, 0), "atomic-terminal", testRequest())
	if result.Decision.Reason != domain.ReasonFit || !manager.MarkForwarded("atomic-terminal") {
		t.Fatalf("atomic terminal reservation setup failed: %+v", result)
	}
	watermark := manager.EventSequence()
	outcome := SchedulerOutcome{
		Identity:   result.Prediction.Identity,
		ObservedAt: time.Unix(5_501, 0),
		Attributed: true,
		Censored:   true,
	}
	terminated := make(chan bool, 1)
	go func() {
		terminated <- manager.TerminateWithOutcome("atomic-terminal", TerminalUpstreamFailure, &outcome)
	}()
	<-scheduler.entered

	reconciled := make(chan error, 1)
	go func() {
		reconciled <- manager.ReconcileSample(SampleWindow{StartedSequence: watermark, FinishedSequence: watermark})
	}()
	select {
	case err := <-reconciled:
		t.Fatalf("reconciliation interleaved between outcome and terminal release: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(scheduler.release)
	if !<-terminated {
		t.Fatal("atomic terminal did not complete")
	}
	if err := <-reconciled; err != nil {
		t.Fatalf("post-terminal reconciliation failed: %v", err)
	}
	if scheduler.observed.Load() != 1 {
		t.Fatalf("atomic terminal outcomes = %d, want 1", scheduler.observed.Load())
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 {
		t.Fatalf("atomic terminal leaked reservation: %+v", snapshot)
	}
}

func TestStaticSchedulerAppliesPrefillPenaltyToEveryPostJoinUser(t *testing.T) {
	now := time.Unix(6_000, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	state := domain.VirtualState{DecodeSequences: 2}
	cost := domain.RequestCost{
		ManifestID:           "test-profile",
		InputTokens:          1_000,
		UncachedPrefillUpper: 1_000,
		DecodeHorizonUpper:   256,
		DecodeSequencesUpper: 1,
		Confidence:           0.99,
	}
	prediction := scheduler.Predict(now, state, cost)
	wantTPS := (testLearnedProfile().BaseCompletionTPS - testLearnedProfile().PrefillTPSPenaltyPerKToken) / 3
	if math.Abs(prediction.Estimate.ExistingUserTPSLower-wantTPS) > 1e-9 {
		t.Fatalf("existing-user TPS = %.6f, want post-join prefill-adjusted %.6f", prediction.Estimate.ExistingUserTPSLower, wantTPS)
	}
	if math.Abs(prediction.Estimate.AllUserTPSLower-wantTPS) > 1e-9 {
		t.Fatalf("all-user TPS = %.6f, want post-join prefill-adjusted %.6f", prediction.Estimate.AllUserTPSLower, wantTPS)
	}
}

func TestStaticSchedulerChargesAccruedLocalAdmissionLatencyToTTFT(t *testing.T) {
	now := time.Unix(7_000, 0)
	profile := testLearnedProfile()
	scheduler := mustLearnedScheduler(t, profile, testResidualConfig())
	cost := learnedTestCost()
	cost.AccruedLocalAdmissionLatency = 75 * time.Millisecond

	prediction := scheduler.Predict(now, learnedTestState(), cost)
	want := addDurationSaturating(
		addDurationSaturating(profile.BaseTTFT, multiplyDurationSaturating(profile.TTFTPerUncachedPrefillToken, cost.UncachedPrefillUpper)),
		cost.AccruedLocalAdmissionLatency,
	)
	if prediction.Prior.TTFTUpper != want || prediction.Estimate.TTFTUpper != want {
		t.Fatalf("TTFT prior/estimate = %s/%s, want backend plus accrued local latency %s", prediction.Prior.TTFTUpper, prediction.Estimate.TTFTUpper, want)
	}
	if prediction.Features.AccruedLocalAdmissionLatency != cost.AccruedLocalAdmissionLatency {
		t.Fatalf("scheduler accrued local latency = %s, want %s", prediction.Features.AccruedLocalAdmissionLatency, cost.AccruedLocalAdmissionLatency)
	}
}

func TestZeroTPSLowerBoundIsAProspectiveRiskRatherThanInvalidPrediction(t *testing.T) {
	now := time.Unix(8_000, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 1
	profile.PrefillTPSPenaltyPerKToken = 2
	scheduler := mustLearnedScheduler(t, profile, testResidualConfig())
	manager := NewManager("test-profile", domain.VirtualState{}, testLearnedConstraints(), scheduler)
	decision := manager.DecideAndReserve(now, "zero-tps", domain.RequestCost{
		ManifestID:               "test-profile",
		InputTokens:              1_000,
		KV:                       domain.KVIncrement{PhysicalKVUpper: 1_024, ActiveKVUpper: 1_024},
		UncachedPrefillUpper:     1_000,
		DecodeHorizonUpper:       16,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: 1_016,
		FutureContextTokensUpper: 16,
		Confidence:               0.99,
	})
	if decision.Reason != domain.ReasonNewTPSAtRisk || decision.Scheduler.AllUserTPSLower != 0 {
		t.Fatalf("zero-TPS decision = %+v, want explicit %s at 0 TPS", decision, domain.ReasonNewTPSAtRisk)
	}
}

func testLearnedConstraints() domain.Constraints {
	return domain.Constraints{
		PhysicalKVHard:       100_000,
		ActiveKVHard:         100_000,
		UserTPSTarget:        25,
		TTFTSLO:              500 * time.Millisecond,
		TPOTSLO:              50 * time.Millisecond,
		WorkspaceRiskBudget:  0.02,
		PreemptionRiskBudget: 0.002,
		MinimumConfidence:    0.95,
	}
}
