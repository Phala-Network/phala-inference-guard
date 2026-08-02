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
	if cold.Reason != domain.ReasonNewTPSAtRisk {
		t.Fatalf("cold reason = %s, want %s (estimate=%+v)", cold.Reason, domain.ReasonNewTPSAtRisk, cold.Scheduler)
	}

	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index+1)*time.Second))
		outcome.ExistingUserTPS = prediction.Prior.ExistingUserTPSLower * 1.20
		outcome.ExistingUserTPSValid = true
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
	if learned.Scheduler.NewUserTPSLower <= cold.Scheduler.NewUserTPSLower {
		t.Fatalf("learned TPS %.3f did not exceed cold %.3f", learned.Scheduler.NewUserTPSLower, cold.Scheduler.NewUserTPSLower)
	}
}

func TestJoiningCompletionTPSDoesNotCertifyExistingPrefillSafety(t *testing.T) {
	now := time.Unix(3_500, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 20
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)
	state := domain.VirtualState{DecodeSequences: 1}
	cost := learnedTestCost()
	prior := scheduler.Predict(now, state, cost)

	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
		outcome.UserTPS = 80
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe joining-user completion sample %d: %v", index, err)
		}
	}

	learned := scheduler.Predict(now.Add(10*time.Second), state, cost)
	if learned.Estimate.NewUserTPSLower <= prior.Estimate.NewUserTPSLower {
		t.Fatalf("joining-user completion evidence did not raise its decode bound: prior=%+v learned=%+v", prior, learned)
	}
	if learned.Estimate.ExistingUserTPSLower != prior.Estimate.ExistingUserTPSLower {
		t.Fatalf("joining-user completion evidence certified existing-user prefill safety: prior=%+v learned=%+v", prior, learned)
	}
}

func TestAdversePostPrefillTPSSurvivesSmallerApproximateRequestEstimate(t *testing.T) {
	now := time.Unix(3_625, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 50
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	config.MinimumTPSMultiplier = 0.1
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)
	sampleState := domain.VirtualState{}
	sampleCost := learnedTestCost()
	sampleCost.InputTokens = 64
	sampleCost.RequestComplexityTokensUpper = 64
	sampleCost.UncachedPrefillUpper = 64
	sampleCost.ActiveContextTokensUpper = 128
	sampleCost.KV.PhysicalKVUpper = 128
	sampleCost.KV.ActiveKVUpper = 128
	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), sampleState, sampleCost)
		if err := scheduler.Observe(prediction, SchedulerOutcome{
			Identity: prediction.Identity, ObservedAt: now.Add(time.Duration(index)*time.Second + 500*time.Millisecond), Attributed: true,
			UserTPS: 20, UserTPSValid: true,
		}); err != nil {
			t.Fatalf("observe adverse decode sample %d: %v", index, err)
		}
	}

	queryState := domain.VirtualState{
		DecodeSequences: 1, ActiveContextTokens: 118, PhysicalKVUpper: 128, ActiveKVUpper: 128,
	}
	queryCost := sampleCost
	queryCost.InputTokens = 54
	queryCost.RequestComplexityTokensUpper = 54
	queryCost.UncachedPrefillUpper = 54
	queryCost.ActiveContextTokensUpper = 118
	prediction := scheduler.Predict(now.Add(10*time.Second), queryState, queryCost)
	if prediction.Source != PredictionSourceCalibrated || prediction.Samples != config.MinimumSamples || prediction.Estimate.NewUserTPSLower >= 25 {
		t.Fatalf("smaller approximate estimate erased adverse decode capacity: %+v", prediction)
	}
}

func TestLearnedSchedulerDoesNotReuseLowerPressureEvidenceInsideCoarseLocalCell(t *testing.T) {
	now := time.Unix(3_750, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 20
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)
	state := domain.VirtualState{DecodeSequences: 1}
	small := learnedTestCost()
	small.InputTokens = 100
	small.RequestComplexityTokensUpper = 100
	small.UncachedPrefillUpper = 100
	small.ActiveContextTokensUpper = 356
	small.KV.PhysicalKVUpper = 384
	small.KV.ActiveKVUpper = 384

	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, small)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
		outcome.UserTPS = 80
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe small-request headroom sample %d: %v", index, err)
		}
	}

	large := small
	large.InputTokens = 900
	large.RequestComplexityTokensUpper = 900
	large.UncachedPrefillUpper = 900
	large.ActiveContextTokensUpper = 1_156
	large.KV.PhysicalKVUpper = 1_184
	large.KV.ActiveKVUpper = 1_184
	prediction := scheduler.Predict(now.Add(10*time.Second), state, large)
	if prediction.Source != PredictionSourceStatic || prediction.Estimate != prediction.Prior {
		t.Fatalf("lower-pressure evidence leaked inside one coarse local cell: %+v", prediction)
	}
}

func TestExistingPrefillZeroTPSImmediatelyTightensNextCompatiblePrediction(t *testing.T) {
	now := time.Unix(3_800, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	state := domain.VirtualState{
		DecodeSequences: 1, ActiveContextTokens: 10_000, PhysicalKVUpper: 12_000, ActiveKVUpper: 12_000,
	}
	cost := learnedTestCost()
	prior := scheduler.Predict(now, state, cost)
	if err := scheduler.ObserveExistingPrefill(ExistingPrefillOutcome{
		Identity:                scheduler.Identity(),
		StartedAt:               now,
		ObservedAt:              now.Add(time.Second),
		Features:                prior.Features,
		ExistingDecodeSequences: 1,
		PendingPrefillSequences: 1,
		PendingPrefillTokens:    1_000,
		ExistingUserTPS:         0,
	}); err != nil {
		t.Fatalf("observe zero-generation prefill stall: %v", err)
	}
	next := scheduler.Predict(now.Add(2*time.Second), state, cost)
	if next.Source != PredictionSourceCalibrated || next.Samples != 1 || next.Estimate.ExistingUserTPSLower >= prior.Estimate.ExistingUserTPSLower {
		t.Fatalf("single adverse prefill outcome did not immediately tighten next prediction: prior=%+v next=%+v", prior, next)
	}
	if next.Estimate.ExistingUserTPSLower <= 0 {
		t.Fatalf("zero TPS feedback created a sticky zero floor: %+v", next)
	}
	snapshot := scheduler.Snapshot()
	if snapshot.ExistingUserTPSSamples != 1 || snapshot.NewUserTPSSamples != 0 {
		t.Fatalf("phase-specific learner counters = %+v", snapshot)
	}
}

func TestStaticTPSProtectsOnlyExistingSequencesThatCompletedPrefill(t *testing.T) {
	now := time.Unix(3_825, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	cost := learnedTestCost()
	allPending := scheduler.Predict(now, domain.VirtualState{
		DecodeSequences: 2, PendingPrefillSequences: 2, UncachedPrefillTokens: 2_000,
	}, cost)
	if !allPending.Prior.ExistingUserTPSNotApplicable || allPending.Prior.ExistingUserTPSLower != 0 {
		t.Fatalf("all-pending prior protected nonexistent existing decoders: %+v", allPending.Prior)
	}
	if allPending.Prior.NewUserTPSLower != scheduler.profile.BaseCompletionTPS/3 {
		t.Fatalf("all-pending prior lost post-join total concurrency: %+v", allPending.Prior)
	}

	oneReady := scheduler.Predict(now, domain.VirtualState{
		DecodeSequences: 2, PendingPrefillSequences: 1, UncachedPrefillTokens: 2_000,
	}, cost)
	twoReady := scheduler.Predict(now, domain.VirtualState{
		DecodeSequences: 2, PendingPrefillSequences: 0, UncachedPrefillTokens: 2_000,
	}, cost)
	if oneReady.Prior.ExistingUserTPSNotApplicable || twoReady.Prior.ExistingUserTPSNotApplicable ||
		math.Abs(oneReady.Prior.ExistingUserTPSLower-2*twoReady.Prior.ExistingUserTPSLower) > 1e-9 {
		t.Fatalf("ready-decoder TPS denominator mismatch: one=%+v two=%+v", oneReady.Prior, twoReady.Prior)
	}
	if oneReady.Prior.NewUserTPSLower != allPending.Prior.NewUserTPSLower || twoReady.Prior.NewUserTPSLower != allPending.Prior.NewUserTPSLower {
		t.Fatalf("pending phase changed post-join total TPS: all=%+v one=%+v two=%+v", allPending.Prior, oneReady.Prior, twoReady.Prior)
	}
}

func TestExistingPrefillOptimisticRelaxationRequiresMinimumSamples(t *testing.T) {
	now := time.Unix(3_850, 0)
	config := testResidualConfig()
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), config)
	state := domain.VirtualState{DecodeSequences: 1, ActiveContextTokens: 10_000, PhysicalKVUpper: 12_000, ActiveKVUpper: 12_000}
	cost := learnedTestCost()
	prior := scheduler.Predict(now, state, cost)
	for index := 0; index < config.MinimumSamples; index++ {
		if err := scheduler.ObserveExistingPrefill(ExistingPrefillOutcome{
			Identity:                scheduler.Identity(),
			StartedAt:               now.Add(time.Duration(index) * 2 * time.Second),
			ObservedAt:              now.Add(time.Duration(index)*2*time.Second + time.Second),
			Features:                prior.Features,
			ExistingDecodeSequences: 1,
			PendingPrefillSequences: 1,
			PendingPrefillTokens:    1_000,
			ExistingUserTPS:         prior.Prior.ExistingUserTPSLower * 1.20,
		}); err != nil {
			t.Fatalf("observe optimistic prefill sample %d: %v", index, err)
		}
		prediction := scheduler.Predict(now.Add(time.Duration(index)*2*time.Second+1500*time.Millisecond), state, cost)
		if index+1 < config.MinimumSamples && prediction.Estimate.ExistingUserTPSLower != prior.Estimate.ExistingUserTPSLower {
			t.Fatalf("immature optimistic evidence relaxed existing TPS after %d samples: %+v", index+1, prediction)
		}
	}
	learned := scheduler.Predict(now.Add(10*time.Second), state, cost)
	if learned.Source != PredictionSourceCalibrated || learned.Estimate.ExistingUserTPSLower <= prior.Estimate.ExistingUserTPSLower {
		t.Fatalf("mature optimistic prefill evidence did not relax prediction: prior=%+v learned=%+v", prior, learned)
	}
}

func TestOptimisticExistingPrefillResidualCannotIncreaseDecodeConcurrency(t *testing.T) {
	now := time.Unix(3_875, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 20
	config := testResidualConfig()
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)
	cost := learnedTestCost()

	sampleState := domain.VirtualState{DecodeSequences: 1}
	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), sampleState, cost)
		outcome := SchedulerOutcome{
			Identity: prediction.Identity, ObservedAt: now.Add(time.Duration(index)*time.Second + 500*time.Millisecond), Attributed: true,
			ExistingUserTPS: prediction.Prior.ExistingUserTPSLower * 4, ExistingUserTPSValid: true,
		}
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe low-concurrency optimistic sample %d: %v", index, err)
		}
	}

	higherConcurrency := scheduler.Predict(now.Add(10*time.Second), domain.VirtualState{DecodeSequences: 2}, cost)
	if higherConcurrency.Estimate.ExistingUserTPSLower != higherConcurrency.Prior.ExistingUserTPSLower {
		t.Fatalf("lower-concurrency optimistic evidence leaked into higher concurrency: %+v", higherConcurrency)
	}
}

func TestMatureHigherPressureOptimisticEvidenceTransfersToLowerPressureQuery(t *testing.T) {
	now := time.Unix(3_890, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 20
	config := testResidualConfig()
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)
	cost := learnedTestCost()
	higherPressure := learnedTestState()
	higherPressure.UncachedPrefillTokens = 3_000

	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), higherPressure, cost)
		outcome := SchedulerOutcome{
			Identity: prediction.Identity, ObservedAt: now.Add(time.Duration(index)*time.Second + 500*time.Millisecond), Attributed: true,
			ExistingUserTPS: prediction.Prior.ExistingUserTPSLower * 2, ExistingUserTPSValid: true,
		}
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe higher-pressure optimistic sample %d: %v", index, err)
		}
	}

	lowerPressure := scheduler.Predict(now.Add(10*time.Second), domain.VirtualState{DecodeSequences: 1}, cost)
	if lowerPressure.Source != PredictionSourceCalibrated || lowerPressure.Estimate.ExistingUserTPSLower <= lowerPressure.Prior.ExistingUserTPSLower {
		t.Fatalf("mature higher-pressure evidence did not safely transfer downward: %+v", lowerPressure)
	}
}

func TestPostPrefillTPSResidualIgnoresDecisionPhasePendingCount(t *testing.T) {
	sample := SchedulerFeatures{
		DecodeSequences:         2,
		PendingPrefillSequences: 1,
		UncachedPrefillTokens:   2_000,
		ActiveContextTokens:     4_000,
		PhysicalKVUpper:         4_096,
		ActiveKVUpper:           4_096,
	}
	query := sample
	query.PendingPrefillSequences = 2
	if !tpsResidualCompatible(sample, query, 1.25) {
		t.Fatal("post-prefill decode-capacity evidence was coupled to decision-phase pending count")
	}
}

func TestPostPrefillResidualsIgnoreDecisionPhasePrefillPressure(t *testing.T) {
	sample := SchedulerFeatures{
		DecodeSequences:         4,
		PendingPrefillSequences: 1,
		UncachedPrefillTokens:   3_074,
		ActiveContextTokens:     14_592,
		PhysicalKVUpper:         14_592,
		ActiveKVUpper:           14_592,
	}
	query := sample
	query.PendingPrefillSequences = 4
	query.UncachedPrefillTokens = 4 * sample.UncachedPrefillTokens
	if !tpsResidualCompatible(sample, query, 1.25) {
		t.Fatal("post-prefill TPS evidence was coupled to prefill-only pressure")
	}
	if !latencyResidualCompatible(sample, query, 0.50) {
		t.Fatal("post-prefill TPOT evidence was coupled to prefill-only pressure")
	}
}

func TestOptimisticPostPrefillResidualRequiresDecodePressureDominance(t *testing.T) {
	sample := SchedulerFeatures{
		DecodeSequences:     4,
		ActiveContextTokens: 14_592,
		PhysicalKVUpper:     14_592,
		ActiveKVUpper:       14_592,
	}
	query := sample
	query.ActiveContextTokens++
	query.PhysicalKVUpper++
	query.ActiveKVUpper++
	if tpsResidualCompatible(sample, query, 1.25) {
		t.Fatal("optimistic post-prefill TPS evidence crossed into higher decode pressure")
	}
	if latencyResidualCompatible(sample, query, 0.50) {
		t.Fatal("optimistic post-prefill TPOT evidence crossed into higher decode pressure")
	}
}

func TestExistingPrefillTPSResidualRequiresPendingPressureDominance(t *testing.T) {
	sample := SchedulerFeatures{
		DecodeSequences:         2,
		PendingPrefillSequences: 1,
		UncachedPrefillTokens:   2_000,
		ActiveContextTokens:     4_000,
		PhysicalKVUpper:         4_096,
		ActiveKVUpper:           4_096,
	}
	query := sample
	query.PendingPrefillSequences = 2
	if existingTPSResidualCompatible(sample, query, 1.25) {
		t.Fatal("optimistic existing-prefill evidence crossed into higher pending-prefill pressure")
	}
}

func TestAdverseLowerPressureEvidenceTransfersToLargerQuery(t *testing.T) {
	now := time.Unix(3_900, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	state := domain.VirtualState{DecodeSequences: 1}
	small := learnedTestCost()
	smallPrediction := scheduler.Predict(now, state, small)
	if err := scheduler.Observe(smallPrediction, SchedulerOutcome{
		Identity:             scheduler.Identity(),
		ObservedAt:           now.Add(time.Second),
		Attributed:           true,
		ExistingUserTPS:      smallPrediction.Prior.ExistingUserTPSLower * 0.50,
		ExistingUserTPSValid: true,
	}); err != nil {
		t.Fatalf("observe adverse small-request prefill evidence: %v", err)
	}
	large := small
	large.InputTokens = 2_000
	large.RequestComplexityTokensUpper = 2_000
	large.UncachedPrefillUpper = 2_000
	large.ActiveContextTokensUpper = 2_256
	large.KV.PhysicalKVUpper = 2_304
	large.KV.ActiveKVUpper = 2_304
	largePrediction := scheduler.Predict(now.Add(2*time.Second), state, large)
	if largePrediction.Source != PredictionSourceCalibrated || largePrediction.Samples != 1 || largePrediction.Estimate.ExistingUserTPSLower >= largePrediction.Prior.ExistingUserTPSLower {
		t.Fatalf("larger query ignored compatible adverse lower-pressure evidence: %+v", largePrediction)
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
		outcome.ExistingUserTPS = prediction.Prior.ExistingUserTPSLower * 0.40
		outcome.ExistingUserTPSValid = true
		outcome.UserTPS = prediction.Prior.NewUserTPSLower * 0.90
		outcome.TTFT = prediction.Prior.TTFTUpper * 3 / 2
		outcome.TPOT = prediction.Prior.TPOTUpper * 3 / 2
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe adverse sample %d: %v", index, err)
		}
	}

	learnedManager := NewManager("test-profile", state, testLearnedConstraints(), scheduler)
	learned := learnedManager.DecideAndReserve(now.Add(5*time.Second), "adverse", cost)
	if learned.Reason != domain.ReasonExistingTPSAtRisk {
		t.Fatalf("adverse reason = %s, want existing-user TPS risk (estimate=%+v)", learned.Reason, learned.Scheduler)
	}
	if learned.Scheduler.ExistingUserTPSLower >= prior.Scheduler.ExistingUserTPSLower || learned.Scheduler.NewUserTPSLower >= prior.Scheduler.NewUserTPSLower {
		t.Fatalf("one per-user residual did not lower both TPS bounds: prior=%+v learned=%+v", prior.Scheduler, learned.Scheduler)
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

func TestStaticSchedulerSeparatesExistingPrefillTPSFromPostPrefillNewUserTPS(t *testing.T) {
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
	wantExistingTPS := (testLearnedProfile().BaseCompletionTPS - testLearnedProfile().PrefillTPSPenaltyPerKToken) / 2
	if math.Abs(prediction.Estimate.ExistingUserTPSLower-wantExistingTPS) > 1e-9 {
		t.Fatalf("existing-user TPS = %.6f, want prefill-era adjusted %.6f", prediction.Estimate.ExistingUserTPSLower, wantExistingTPS)
	}
	wantNewTPS := testLearnedProfile().BaseCompletionTPS / 3
	if math.Abs(prediction.Estimate.NewUserTPSLower-wantNewTPS) > 1e-9 {
		t.Fatalf("new-user TPS = %.6f, want post-prefill decode %.6f", prediction.Estimate.NewUserTPSLower, wantNewTPS)
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

func TestZeroExistingTPSLowerBoundIsAProspectiveRiskRatherThanInvalidPrediction(t *testing.T) {
	now := time.Unix(8_000, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 1
	profile.PrefillTPSPenaltyPerKToken = 2
	scheduler := mustLearnedScheduler(t, profile, testResidualConfig())
	manager := NewManager("test-profile", domain.VirtualState{DecodeSequences: 1}, testLearnedConstraints(), scheduler)
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
	if decision.Reason != domain.ReasonExistingTPSAtRisk || decision.Scheduler.ExistingUserTPSLower != 0 {
		t.Fatalf("zero-TPS decision = %+v, want explicit %s at 0 TPS", decision, domain.ReasonExistingTPSAtRisk)
	}
}

func TestLearnedSchedulerUsesQualifiedGlobalHeadroomToProgressBeyondColdConcurrency(t *testing.T) {
	now := time.Unix(9_000, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 20
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)
	state := domain.VirtualState{}
	cost := learnedTestCost()

	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
		outcome.UserTPS = 60
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe idle headroom sample %d: %v", index, err)
		}
	}

	constraints := domain.Constraints{
		PhysicalKVHard:       100_000,
		ActiveKVHard:         100_000,
		UserTPSTarget:        20,
		WorkspaceRiskBudget:  profile.WorkspaceRiskUpper,
		PreemptionRiskBudget: profile.PreemptionRiskUpper,
		MinimumConfidence:    0.95,
	}
	manager := NewManager("test-profile", state, constraints, scheduler)
	first := manager.decideAndReserve(now.Add(10*time.Second), "first", cost)
	if first.Decision.Reason != domain.ReasonFit {
		t.Fatalf("first learned request = %+v, want fit", first)
	}
	second := manager.decideAndReserve(now.Add(10*time.Second), "second", cost)
	if second.Decision.Reason != domain.ReasonFit || second.Prediction.Source != PredictionSourceCalibrated {
		t.Fatalf("second progressive request = %+v, want globally calibrated fit", second)
	}
	if second.Prediction.Estimate.NewUserTPSLower < constraints.UserTPSTarget {
		t.Fatalf("second predicted TPS = %.3f, want at least %.3f", second.Prediction.Estimate.NewUserTPSLower, constraints.UserTPSTarget)
	}
}

func TestLearnedSchedulerDominantShapeDoesNotEraseMatureMinorityFallback(t *testing.T) {
	now := time.Unix(9_250, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 20
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	config.MinimumSamples = 4
	config.MaximumSamplesPerCell = 16
	config.MaximumCells = 256
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)

	large := learnedTestCost()
	large.InputTokens = 100_000
	large.RequestComplexityTokensUpper = 100_000
	large.UncachedPrefillUpper = 100_000
	large.ActiveContextTokensUpper = 100_256
	large.KV.PhysicalKVUpper = 100_288
	large.KV.ActiveKVUpper = 100_288
	for index := 0; index < config.MinimumSamples; index++ {
		predictedAt := now.Add(time.Duration(index) * 10 * time.Millisecond)
		prediction := scheduler.Predict(predictedAt, domain.VirtualState{}, large)
		outcome := healthyLearnedOutcome(prediction, predictedAt.Add(time.Millisecond))
		outcome.UserTPS = 80
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe mature large-shape sample %d: %v", index, err)
		}
	}

	small := learnedTestCost()
	small.InputTokens = 100
	small.RequestComplexityTokensUpper = 100
	small.UncachedPrefillUpper = 100
	small.ActiveContextTokensUpper = 356
	small.KV.PhysicalKVUpper = 384
	small.KV.ActiveKVUpper = 384
	for index := 0; index < 4*config.MaximumSamplesPerCell; index++ {
		predictedAt := now.Add(time.Duration(config.MinimumSamples+index) * 10 * time.Millisecond)
		prediction := scheduler.Predict(predictedAt, domain.VirtualState{}, small)
		outcome := healthyLearnedOutcome(prediction, predictedAt.Add(time.Millisecond))
		outcome.UserTPS = 80
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe dominant small-shape sample %d: %v", index, err)
		}
	}

	queryState := domain.VirtualState{
		PhysicalKVUpper:       large.KV.PhysicalKVUpper,
		ActiveKVUpper:         large.KV.ActiveKVUpper,
		DecodeSequences:       1,
		ActiveContextTokens:   large.ActiveContextTokensUpper,
		UncachedPrefillTokens: large.UncachedPrefillUpper,
	}
	prediction := scheduler.Predict(now.Add(2*time.Second), queryState, small)
	if prediction.Source != PredictionSourceCalibrated || prediction.Samples < config.MinimumSamples {
		t.Fatalf("mature minority fallback was erased by dominant shape: %+v", prediction)
	}
	if prediction.Estimate.NewUserTPSLower < 20 {
		t.Fatalf("retained minority fallback TPS = %.3f, want at least 20", prediction.Estimate.NewUserTPSLower)
	}
	if snapshot := scheduler.Snapshot(); snapshot.GlobalSamples <= config.MaximumSamplesPerCell || snapshot.GlobalSamples > maximumGlobalResidualSamples {
		t.Fatalf("stratified global sample bound = %+v", snapshot)
	}
	counted := 0
	for _, count := range scheduler.globalCounts {
		counted += count
	}
	if counted != len(scheduler.globalSamples) {
		t.Fatalf("global sample count index = %d, want retained sample length %d", counted, len(scheduler.globalSamples))
	}
}

func TestFreshCompatibleResidualRatiosSelectsMatureLevelPerDimensionInOnePass(t *testing.T) {
	now := time.Unix(9_400, 0)
	samples := make([]residualSample, 0, 6)
	for index := 0; index < 3; index++ {
		samples = append(samples, residualSample{
			ObservedAt: now,
			Features: SchedulerFeatures{
				DecodeSequences: 4,
			},
			UserTPSRatio: 2 + float64(index),
			UserTPSValid: true,
		})
		samples = append(samples, residualSample{
			ObservedAt: now,
			Features: SchedulerFeatures{
				DecodeSequences: 3,
			},
			TTFTRatio: 3 + float64(index),
			TPOTRatio: 4 + float64(index),
			TTFTValid: true,
			TPOTValid: true,
		})
	}

	got := freshCompatibleResidualRatios(samples, now, time.Minute, SchedulerFeatures{DecodeSequences: 2}, 3)
	if len(got.UserTPS) != 3 || got.UserTPS[0] != 2 {
		t.Fatalf("TPS ratios = %v, want mature decode-sequence level 4", got.UserTPS)
	}
	if len(got.TTFT) != 3 || got.TTFT[0] != 3 {
		t.Fatalf("TTFT ratios = %v, want mature decode-sequence level 3", got.TTFT)
	}
	if len(got.TPOT) != 3 || got.TPOT[0] != 4 {
		t.Fatalf("TPOT ratios = %v, want mature decode-sequence level 3", got.TPOT)
	}
}

func TestGlobalFallbackScanIsNotTriggeredOnlyForObservationalTTFT(t *testing.T) {
	mature := make([]float64, 3)
	if requiresGlobalResidualFallback(residualRatios{UserTPS: mature, TPOT: mature}, 3, false) {
		t.Fatal("mature protected dimensions scanned global history only for TTFT")
	}
	if !requiresGlobalResidualFallback(residualRatios{UserTPS: mature[:2], TPOT: mature}, 3, false) {
		t.Fatal("immature TPS did not request global fallback")
	}
	if !requiresGlobalResidualFallback(residualRatios{UserTPS: mature, TPOT: mature[:2]}, 3, false) {
		t.Fatal("immature TPOT did not request global fallback")
	}
}

func TestLearnedSchedulerDoesNotApplySmallRequestGlobalHeadroomToHigherPressureRequest(t *testing.T) {
	now := time.Unix(9_500, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 20
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)
	state := domain.VirtualState{}
	small := learnedTestCost()

	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, small)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
		outcome.UserTPS = 80
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe small-request headroom sample %d: %v", index, err)
		}
	}

	compatibleState := domain.VirtualState{
		DecodeSequences:       1,
		ActiveContextTokens:   small.ActiveContextTokensUpper,
		UncachedPrefillTokens: small.UncachedPrefillUpper,
		PhysicalKVUpper:       small.KV.PhysicalKVUpper,
		ActiveKVUpper:         small.KV.ActiveKVUpper,
	}
	compatible := scheduler.Predict(now.Add(10*time.Second), compatibleState, small)
	if compatible.Source != PredictionSourceCalibrated || compatible.Estimate.NewUserTPSLower <= compatible.Prior.NewUserTPSLower {
		t.Fatalf("compatible progressive request did not use global headroom: %+v", compatible)
	}

	large := small
	large.InputTokens = small.InputTokens * 1_000
	large.UncachedPrefillUpper = small.UncachedPrefillUpper * 1_000
	large.ActiveContextTokensUpper = small.ActiveContextTokensUpper * 1_000
	large.KV.PhysicalKVUpper = small.KV.PhysicalKVUpper * 1_000
	large.KV.ActiveKVUpper = small.KV.ActiveKVUpper * 1_000
	largePrediction := scheduler.Predict(now.Add(10*time.Second), state, large)
	if largePrediction.Source != PredictionSourceStatic || largePrediction.Estimate != largePrediction.Prior {
		t.Fatalf("small-request global headroom leaked into higher-pressure request: %+v", largePrediction)
	}
}

func TestLearnedSchedulerIdleFloorPreventsAdverseTPSStickyZero(t *testing.T) {
	now := time.Unix(10_000, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 20
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	config.MinimumTPSMultiplier = 0.10
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)
	state := domain.VirtualState{}
	cost := learnedTestCost()

	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
		outcome.UserTPS = 5
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe adverse idle sample %d: %v", index, err)
		}
	}

	constraints := domain.Constraints{
		PhysicalKVHard:       100_000,
		ActiveKVHard:         100_000,
		UserTPSTarget:        20,
		WorkspaceRiskBudget:  profile.WorkspaceRiskUpper,
		PreemptionRiskBudget: profile.PreemptionRiskUpper,
		MinimumConfidence:    0.95,
	}
	manager := NewManager("test-profile", state, constraints, scheduler)
	first := manager.decideAndReserve(now.Add(10*time.Second), "idle-probe", cost)
	if first.Decision.Reason != domain.ReasonFit || first.Prediction.Estimate.NewUserTPSLower < constraints.UserTPSTarget {
		t.Fatalf("post-drain idle probe = %+v, want cold-safe progress", first)
	}
	second := manager.decideAndReserve(now.Add(10*time.Second), "concurrent", cost)
	if second.Decision.Reason != domain.ReasonExistingTPSAtRisk && second.Decision.Reason != domain.ReasonNewTPSAtRisk {
		t.Fatalf("adverse learned concurrent request = %+v, want TPS protection", second)
	}
}

func TestLearnedSchedulerKeepsShapeSpecificTTFTTelemetryWithoutAdmissionProtection(t *testing.T) {
	now := time.Unix(10_500, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 25
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	scheduler := mustLearnedScheduler(t, profile, config)
	state := domain.VirtualState{}
	cost := learnedTestCost()

	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
		outcome.TTFT = prediction.Prior.TTFTUpper * 3
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe adverse TTFT sample %d: %v", index, err)
		}
	}

	prediction := scheduler.Predict(now.Add(10*time.Second), state, cost)
	if prediction.Source != PredictionSourceCalibrated || prediction.Estimate.TTFTUpper <= prediction.Prior.TTFTUpper {
		t.Fatalf("idle shape-specific adverse TTFT was discarded: %+v", prediction)
	}
	constraints := testLearnedConstraints()
	constraints.UserTPSTarget = 25
	manager := NewManager("test-profile", state, constraints, scheduler)
	decision := manager.DecideAndReserve(now.Add(10*time.Second), "known-slow-ttft", cost)
	if decision.Reason != domain.ReasonFit {
		t.Fatalf("known adverse idle TTFT decision = %+v, want TTFT-observational fit", decision)
	}
}

func TestLearnedSchedulerRetainsQoSCellWhenKVCalibrationNarrowsInputUpper(t *testing.T) {
	now := time.Unix(10_750, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 100
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	scheduler := mustLearnedScheduler(t, profile, config)
	state := domain.VirtualState{}
	cold := learnedTestCost()
	cold.InputTokens = 100
	cold.RequestComplexityTokensUpper = 150
	cold.UncachedPrefillUpper = 100
	cold.ActiveContextTokensUpper = 100 + cold.DecodeHorizonUpper

	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cold)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
		outcome.TTFT = prediction.Prior.TTFTUpper * 3
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe pre-calibration TTFT sample %d: %v", index, err)
		}
	}

	narrowed := cold
	narrowed.InputTokens = 75
	narrowed.UncachedPrefillUpper = 75
	narrowed.ActiveContextTokensUpper = 75 + narrowed.DecodeHorizonUpper
	prediction := scheduler.Predict(now.Add(10*time.Second), state, narrowed)
	if prediction.Source != PredictionSourceCalibrated || prediction.Estimate.TTFTUpper <= prediction.Prior.TTFTUpper {
		t.Fatalf("KV calibration discarded stable raw-complexity QoS cell: %+v", prediction)
	}
}

func TestLearnedSchedulerRejectsCensoredOutcomeWithoutPoisoningHeadroom(t *testing.T) {
	now := time.Unix(11_000, 0)
	profile := testLearnedProfile()
	profile.BaseCompletionTPS = 20
	profile.PrefillTPSPenaltyPerKToken = 0
	config := testResidualConfig()
	config.MaximumTPSMultiplier = 8
	scheduler := mustLearnedScheduler(t, profile, config)
	state := domain.VirtualState{}
	cost := learnedTestCost()

	for index := 0; index < config.MinimumSamples; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		outcome := healthyLearnedOutcome(prediction, now.Add(time.Duration(index)*time.Second+500*time.Millisecond))
		outcome.UserTPS = 60
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe qualified headroom sample %d: %v", index, err)
		}
	}
	censoredPrediction := scheduler.Predict(now.Add(4*time.Second), state, cost)
	if err := scheduler.Observe(censoredPrediction, SchedulerOutcome{
		Identity: censoredPrediction.Identity, ObservedAt: now.Add(5 * time.Second),
		Attributed: true, Censored: true,
	}); err == nil {
		t.Fatal("censored outcome was accepted as scheduler training")
	}

	after := scheduler.Predict(now.Add(6*time.Second), state, cost)
	if after.Estimate.NewUserTPSLower <= after.Prior.NewUserTPSLower || after.Source != PredictionSourceCalibrated {
		t.Fatalf("censored outcome poisoned qualified headroom: %+v", after)
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != uint64(config.MinimumSamples) || snapshot.SamplesRejected != 1 {
		t.Fatalf("censored accounting = %+v", snapshot)
	}
}

func testLearnedConstraints() domain.Constraints {
	return domain.Constraints{
		PhysicalKVHard:       100_000,
		ActiveKVHard:         100_000,
		UserTPSTarget:        25,
		TPOTSLO:              50 * time.Millisecond,
		WorkspaceRiskBudget:  0.02,
		PreemptionRiskBudget: 0.002,
		MinimumConfidence:    0.95,
	}
}
