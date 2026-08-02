package predictive

import (
	"math"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestLearnedSchedulerUsesEligibleResidualsAndFallsBackWhenStale(t *testing.T) {
	now := time.Unix(1_000, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	state := learnedTestState()
	cost := learnedTestCost()

	cold := scheduler.Predict(now, state, cost)
	if cold.Source != PredictionSourceStatic || cold.Samples != 0 {
		t.Fatalf("cold prediction source/samples = %s/%d, want static/0", cold.Source, cold.Samples)
	}
	if cold.Estimate != cold.Prior {
		t.Fatalf("cold estimate = %+v, want exact prior %+v", cold.Estimate, cold.Prior)
	}

	for index := 0; index < 2; index++ {
		prediction := scheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		if err := scheduler.Observe(prediction, healthyLearnedOutcome(prediction, now.Add(time.Duration(index+1)*time.Second))); err != nil {
			t.Fatalf("observe sparse sample %d: %v", index, err)
		}
	}
	sparse := scheduler.Predict(now.Add(3*time.Second), state, cost)
	if sparse.Source != PredictionSourceStatic || sparse.Estimate != sparse.Prior {
		t.Fatalf("sparse prediction = %+v, want exact static prior", sparse)
	}

	prediction := scheduler.Predict(now.Add(3*time.Second), state, cost)
	if err := scheduler.Observe(prediction, healthyLearnedOutcome(prediction, now.Add(4*time.Second))); err != nil {
		t.Fatalf("observe eligibility sample: %v", err)
	}
	calibrated := scheduler.Predict(now.Add(5*time.Second), state, cost)
	if calibrated.Source != PredictionSourceCalibrated || calibrated.Samples != 3 {
		t.Fatalf("calibrated source/samples = %s/%d, want calibrated/3", calibrated.Source, calibrated.Samples)
	}
	if calibrated.Estimate.NewUserTPSLower <= calibrated.Prior.NewUserTPSLower || calibrated.Estimate.ExistingUserTPSLower != calibrated.Prior.ExistingUserTPSLower {
		t.Fatalf("joining completion residual did not raise only the new-user TPS bound: prior=%+v estimate=%+v", calibrated.Prior, calibrated.Estimate)
	}
	if calibrated.Estimate.TTFTUpper >= calibrated.Prior.TTFTUpper {
		t.Fatalf("calibrated TTFT = %s, want below prior %s", calibrated.Estimate.TTFTUpper, calibrated.Prior.TTFTUpper)
	}

	staleAt := now.Add(testResidualConfig().MaxAge + 5*time.Second)
	stale := scheduler.Predict(staleAt, state, cost)
	if stale.Source != PredictionSourceStatic || stale.Estimate != stale.Prior {
		t.Fatalf("stale prediction = %+v, want exact static prior", stale)
	}
}

func TestLearnedSchedulerRejectsWrongEpochInvalidAndUnattributedOutcomes(t *testing.T) {
	now := time.Unix(2_000, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	prediction := scheduler.Predict(now, learnedTestState(), learnedTestCost())

	wrongEpoch := healthyLearnedOutcome(prediction, now.Add(time.Second))
	wrongEpoch.Identity.BackendEpoch = "other-epoch"
	if err := scheduler.Observe(prediction, wrongEpoch); err == nil {
		t.Fatal("wrong-epoch outcome must fail")
	}

	invalid := healthyLearnedOutcome(prediction, now.Add(2*time.Second))
	invalid.UserTPS = math.NaN()
	if err := scheduler.Observe(prediction, invalid); err == nil {
		t.Fatal("NaN outcome must fail")
	}

	unattributed := healthyLearnedOutcome(prediction, now.Add(3*time.Second))
	unattributed.Attributed = false
	if err := scheduler.Observe(prediction, unattributed); err == nil {
		t.Fatal("unattributed outcome must fail")
	}

	snapshot := scheduler.Snapshot()
	if snapshot.SamplesAccepted != 0 || snapshot.SamplesRejected != 3 || snapshot.Cells != 0 {
		t.Fatalf("snapshot after rejected outcomes = %+v", snapshot)
	}
	got := scheduler.Predict(now.Add(4*time.Second), learnedTestState(), learnedTestCost())
	if got.Source != PredictionSourceStatic || got.Estimate != got.Prior {
		t.Fatalf("rejected outcomes changed prediction: %+v", got)
	}
}

func TestLearnedSchedulerCalibratesPerUserTPSAndLatencyTargets(t *testing.T) {
	now := time.Unix(2_500, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	state := domain.VirtualState{}
	cost := learnedTestCost()
	cost.DecodeSequencesUpper = 1

	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		predictedAt := now.Add(time.Duration(index) * time.Second)
		prediction := scheduler.Predict(predictedAt, state, cost)
		outcome := SchedulerOutcome{
			Identity:     prediction.Identity,
			ObservedAt:   predictedAt.Add(500 * time.Millisecond),
			Attributed:   true,
			UserTPS:      prediction.Prior.NewUserTPSLower * 1.20,
			UserTPSValid: true,
			TTFT:         prediction.Prior.TTFTUpper * 8 / 10,
			TTFTValid:    true,
			TPOT:         prediction.Prior.TPOTUpper * 8 / 10,
			TPOTValid:    true,
		}
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe partial target set %d: %v", index, err)
		}
	}

	calibrated := scheduler.Predict(now.Add(5*time.Second), state, cost)
	if calibrated.Source != PredictionSourceCalibrated || calibrated.Samples != testResidualConfig().MinimumSamples {
		t.Fatalf("partial calibrated source/samples = %s/%d", calibrated.Source, calibrated.Samples)
	}
	if calibrated.Estimate.NewUserTPSLower <= calibrated.Prior.NewUserTPSLower || calibrated.Estimate.TTFTUpper >= calibrated.Prior.TTFTUpper || calibrated.Estimate.TPOTUpper >= calibrated.Prior.TPOTUpper {
		t.Fatalf("available targets were not independently calibrated: %+v", calibrated)
	}
}

func TestLearnedSchedulerBoundsCellsAndRetainsBoundedGlobalFallback(t *testing.T) {
	now := time.Unix(2_700, 0)
	config := testResidualConfig()
	config.MinimumSamples = 1
	config.MaximumCells = 2
	config.ContextTokenBucket = 1
	config.KVTokenBucket = 1
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), config)
	cost := learnedTestCost()

	states := []domain.VirtualState{
		{PhysicalKVUpper: 100, ActiveKVUpper: 100, DecodeSequences: 1, ActiveContextTokens: 100},
		{PhysicalKVUpper: 200, ActiveKVUpper: 200, DecodeSequences: 1, ActiveContextTokens: 200},
		{PhysicalKVUpper: 300, ActiveKVUpper: 300, DecodeSequences: 1, ActiveContextTokens: 300},
	}
	for index, state := range states {
		predictedAt := now.Add(time.Duration(index) * time.Second)
		prediction := scheduler.Predict(predictedAt, state, cost)
		if err := scheduler.Observe(prediction, healthyLearnedOutcome(prediction, predictedAt.Add(500*time.Millisecond))); err != nil {
			t.Fatalf("observe cell %d: %v", index, err)
		}
	}
	if snapshot := scheduler.Snapshot(); snapshot.Cells != 2 || snapshot.GlobalSamples != len(states) || snapshot.GlobalSamples > config.MaximumSamplesPerCell {
		t.Fatalf("globally bounded snapshot = %+v, want two cells and bounded fallback", snapshot)
	}
	oldestKey := scheduler.featureCell(schedulerFeatures(states[0], cost))
	newestKey := scheduler.featureCell(schedulerFeatures(states[2], cost))
	scheduler.mu.Lock()
	_, oldestExists := scheduler.cells[oldestKey]
	_, newestExists := scheduler.cells[newestKey]
	scheduler.mu.Unlock()
	if oldestExists || !newestExists {
		t.Fatalf("deterministic cell eviction = oldest %t newest %t, want false/true", oldestExists, newestExists)
	}
	if oldest := scheduler.Predict(now.Add(10*time.Second), states[0], cost); oldest.Source != PredictionSourceCalibrated {
		t.Fatalf("evicted cell did not use bounded global fallback: %+v", oldest)
	}
	if newest := scheduler.Predict(now.Add(10*time.Second), states[2], cost); newest.Source != PredictionSourceCalibrated {
		t.Fatalf("newest cell was unexpectedly evicted: %+v", newest)
	}
}

func TestResidualCalibratorRequiresGlobalCellBound(t *testing.T) {
	config := testResidualConfig()
	config.MaximumCells = 0
	if _, err := NewLearnedScheduler(testLearnedProfile(), config); err == nil {
		t.Fatal("zero global calibrator cell bound was accepted")
	}
}

func TestFreshCensoredOutcomeIsRejectedWithoutChangingQualifiedHeadroom(t *testing.T) {
	now := time.Unix(2_800, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	state := learnedTestState()
	cost := learnedTestCost()
	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		predictedAt := now.Add(time.Duration(index) * time.Second)
		prediction := scheduler.Predict(predictedAt, state, cost)
		if err := scheduler.Observe(prediction, healthyLearnedOutcome(prediction, predictedAt.Add(500*time.Millisecond))); err != nil {
			t.Fatalf("observe healthy sample %d: %v", index, err)
		}
	}
	prediction := scheduler.Predict(now.Add(4*time.Second), state, cost)
	censored := healthyLearnedOutcome(prediction, now.Add(4500*time.Millisecond))
	censored.Censored = true
	if err := scheduler.Observe(prediction, censored); err == nil {
		t.Fatal("censored terminal was accepted as training")
	}

	guarded := scheduler.Predict(now.Add(5*time.Second), state, cost)
	if guarded.Estimate.ExistingUserTPSLower != guarded.Prior.ExistingUserTPSLower || guarded.Estimate.NewUserTPSLower <= guarded.Prior.NewUserTPSLower {
		t.Fatalf("censored outcome poisoned qualified TPS headroom: %+v", guarded)
	}
	if guarded.Estimate.TTFTUpper >= guarded.Prior.TTFTUpper || guarded.Estimate.TPOTUpper >= guarded.Prior.TPOTUpper {
		t.Fatalf("censored outcome poisoned qualified latency headroom: %+v", guarded)
	}
}

func TestFreshPartialOutcomeWithoutTPSDoesNotPoisonQualifiedTPSHeadroom(t *testing.T) {
	now := time.Unix(2_825, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	state := learnedTestState()
	cost := learnedTestCost()
	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		predictedAt := now.Add(time.Duration(index) * time.Second)
		prediction := scheduler.Predict(predictedAt, state, cost)
		if err := scheduler.Observe(prediction, healthyLearnedOutcome(prediction, predictedAt.Add(500*time.Millisecond))); err != nil {
			t.Fatalf("observe healthy sample %d: %v", index, err)
		}
	}
	optimistic := scheduler.Predict(now.Add(4*time.Second), state, cost)
	if optimistic.Estimate.NewUserTPSLower <= optimistic.Prior.NewUserTPSLower {
		t.Fatalf("healthy samples did not establish optimistic TPS headroom: %+v", optimistic)
	}
	if err := scheduler.Observe(optimistic, SchedulerOutcome{
		Identity:   optimistic.Identity,
		ObservedAt: now.Add(4500 * time.Millisecond),
		Attributed: true,
		TTFT:       optimistic.Prior.TTFTUpper,
		TTFTValid:  true,
	}); err != nil {
		t.Fatalf("observe fresh TTFT-only outcome: %v", err)
	}

	guarded := scheduler.Predict(now.Add(5*time.Second), state, cost)
	if guarded.Estimate.ExistingUserTPSLower != guarded.Prior.ExistingUserTPSLower || guarded.Estimate.NewUserTPSLower <= guarded.Prior.NewUserTPSLower {
		t.Fatalf("fresh outcome without TPS poisoned qualified TPS headroom: %+v", guarded)
	}
}

func TestCensoredOutcomeWithoutMetricTargetsIsRejected(t *testing.T) {
	now := time.Unix(2_850, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	prediction := scheduler.Predict(now, learnedTestState(), learnedTestCost())
	if err := scheduler.Observe(prediction, SchedulerOutcome{
		Identity:   prediction.Identity,
		ObservedAt: now.Add(time.Second),
		Attributed: true,
		Censored:   true,
	}); err == nil {
		t.Fatal("targetless censored outcome was accepted")
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 0 || snapshot.SamplesRejected != 1 || snapshot.GlobalSamples != 0 {
		t.Fatalf("targetless censored snapshot = %+v, want one rejected non-sample", snapshot)
	}
}

func TestLearningInvalidationDropsOldHeadroom(t *testing.T) {
	now := time.Unix(2_900, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	state := learnedTestState()
	cost := learnedTestCost()
	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		predictedAt := now.Add(time.Duration(index) * time.Second)
		prediction := scheduler.Predict(predictedAt, state, cost)
		if err := scheduler.Observe(prediction, healthyLearnedOutcome(prediction, predictedAt.Add(500*time.Millisecond))); err != nil {
			t.Fatalf("observe sample %d: %v", index, err)
		}
	}
	scheduler.InvalidateLearning()
	if snapshot := scheduler.Snapshot(); snapshot.Cells != 0 || snapshot.GlobalSamples != 0 || snapshot.Invalidations != 1 || len(scheduler.globalCounts) != 0 {
		t.Fatalf("post-invalidation snapshot = %+v, want zero cells and one invalidation", snapshot)
	}
	if prediction := scheduler.Predict(now.Add(5*time.Second), state, cost); prediction.Source != PredictionSourceStatic || prediction.Estimate != prediction.Prior {
		t.Fatalf("invalidated scheduler retained learned headroom: %+v", prediction)
	}
}

func testPredictorIdentity() ModelIdentity {
	return ModelIdentity{
		ProfileID:        "gemma-vllm-test",
		BackendEpoch:     "backend-epoch-1",
		PredictorVersion: "scheduler-v1",
	}
}

func testLearnedProfile() StaticSchedulerProfile {
	return StaticSchedulerProfile{
		Identity:                      testPredictorIdentity(),
		BaseCompletionTPS:             72,
		PrefillTPSPenaltyPerKToken:    1,
		BaseTTFT:                      80 * time.Millisecond,
		TTFTPerUncachedPrefillToken:   20 * time.Microsecond,
		BaseTPOT:                      20 * time.Millisecond,
		TPOTPerExistingDecodeSequence: 2 * time.Millisecond,
		WorkspaceRiskUpper:            0.01,
		PreemptionRiskUpper:           0.001,
		Confidence:                    0.97,
	}
}

func testResidualConfig() ResidualCalibratorConfig {
	return ResidualCalibratorConfig{
		Identity:                 testPredictorIdentity(),
		MinimumSamples:           3,
		MaximumSamplesPerCell:    16,
		MaximumCells:             64,
		MaxAge:                   time.Minute,
		LowerQuantile:            0.10,
		UpperQuantile:            0.90,
		MinimumTPSMultiplier:     0.50,
		MaximumTPSMultiplier:     1.50,
		MinimumLatencyMultiplier: 0.50,
		MaximumLatencyMultiplier: 2.00,
		CalibratedConfidence:     0.99,
		DecodeSequenceBucket:     1,
		ContextTokenBucket:       4_096,
		PrefillTokenBucket:       1_024,
		KVTokenBucket:            4_096,
	}
}

func mustLearnedScheduler(t *testing.T, profile StaticSchedulerProfile, config ResidualCalibratorConfig) *LearnedScheduler {
	t.Helper()
	scheduler, err := NewLearnedScheduler(profile, config)
	if err != nil {
		t.Fatalf("new learned scheduler: %v", err)
	}
	return scheduler
}

func learnedTestState() domain.VirtualState {
	return domain.VirtualState{
		PhysicalKVUpper:       40_000,
		ActiveKVUpper:         36_000,
		DecodeSequences:       2,
		ActiveContextTokens:   24_000,
		UncachedPrefillTokens: 0,
	}
}

func learnedTestCost() domain.RequestCost {
	return domain.RequestCost{
		ManifestID:               "test-profile",
		InputTokens:              1_000,
		KV:                       domain.KVIncrement{PhysicalKVUpper: 1_280, ActiveKVUpper: 1_280},
		FutureKV:                 domain.KVIncrement{PhysicalKVUpper: 256, ActiveKVUpper: 256},
		UncachedPrefillUpper:     1_000,
		DecodeHorizonUpper:       256,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: 1_256,
		FutureContextTokensUpper: 256,
		Confidence:               0.99,
	}
}

func healthyLearnedOutcome(prediction SchedulerPrediction, observedAt time.Time) SchedulerOutcome {
	return SchedulerOutcome{
		Identity:     prediction.Identity,
		ObservedAt:   observedAt,
		Attributed:   true,
		UserTPS:      prediction.Prior.NewUserTPSLower * 1.20,
		UserTPSValid: true,
		TTFT:         prediction.Prior.TTFTUpper * 8 / 10,
		TTFTValid:    true,
		TPOT:         prediction.Prior.TPOTUpper * 8 / 10,
		TPOTValid:    true,
	}
}
