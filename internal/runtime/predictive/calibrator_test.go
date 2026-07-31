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
	if calibrated.Estimate.AllUserTPSLower <= calibrated.Prior.AllUserTPSLower {
		t.Fatalf("calibrated all-user TPS = %.3f, want above prior %.3f", calibrated.Estimate.AllUserTPSLower, calibrated.Prior.AllUserTPSLower)
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
	invalid.AllUserTPS = math.NaN()
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

func TestLearnedSchedulerCalibratesAvailableTargetsIndependently(t *testing.T) {
	now := time.Unix(2_500, 0)
	scheduler := mustLearnedScheduler(t, testLearnedProfile(), testResidualConfig())
	state := domain.VirtualState{}
	cost := learnedTestCost()
	cost.DecodeSequencesUpper = 1

	for index := 0; index < testResidualConfig().MinimumSamples; index++ {
		predictedAt := now.Add(time.Duration(index) * time.Second)
		prediction := scheduler.Predict(predictedAt, state, cost)
		outcome := SchedulerOutcome{
			Identity:        prediction.Identity,
			ObservedAt:      predictedAt.Add(500 * time.Millisecond),
			Attributed:      true,
			AllUserTPS:      prediction.Prior.AllUserTPSLower * 1.20,
			AllUserTPSValid: true,
			TTFT:            prediction.Prior.TTFTUpper * 8 / 10,
			TTFTValid:       true,
			TPOT:            prediction.Prior.TPOTUpper * 8 / 10,
			TPOTValid:       true,
		}
		if err := scheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe partial target set %d: %v", index, err)
		}
	}

	calibrated := scheduler.Predict(now.Add(5*time.Second), state, cost)
	if calibrated.Source != PredictionSourceCalibrated || calibrated.Samples != testResidualConfig().MinimumSamples {
		t.Fatalf("partial calibrated source/samples = %s/%d", calibrated.Source, calibrated.Samples)
	}
	if calibrated.Estimate.ExistingUserTPSLower != calibrated.Prior.ExistingUserTPSLower {
		t.Fatalf("unobserved existing-user TPS changed: prior=%f estimate=%f", calibrated.Prior.ExistingUserTPSLower, calibrated.Estimate.ExistingUserTPSLower)
	}
	if calibrated.Estimate.AllUserTPSLower <= calibrated.Prior.AllUserTPSLower || calibrated.Estimate.TTFTUpper >= calibrated.Prior.TTFTUpper || calibrated.Estimate.TPOTUpper >= calibrated.Prior.TPOTUpper {
		t.Fatalf("available targets were not independently calibrated: %+v", calibrated)
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
		ManifestID:           "test-profile",
		InputTokens:          1_000,
		KV:                   domain.KVIncrement{PhysicalKVUpper: 8_256, ActiveKVUpper: 8_256},
		UncachedPrefillUpper: 1_000,
		DecodeHorizonUpper:   256,
		Confidence:           0.99,
	}
}

func healthyLearnedOutcome(prediction SchedulerPrediction, observedAt time.Time) SchedulerOutcome {
	return SchedulerOutcome{
		Identity:             prediction.Identity,
		ObservedAt:           observedAt,
		Attributed:           true,
		ExistingUserTPS:      prediction.Prior.ExistingUserTPSLower * 1.20,
		ExistingUserTPSValid: true,
		AllUserTPS:           prediction.Prior.AllUserTPSLower * 1.20,
		AllUserTPSValid:      true,
		TTFT:                 prediction.Prior.TTFTUpper * 8 / 10,
		TTFTValid:            true,
		TPOT:                 prediction.Prior.TPOTUpper * 8 / 10,
		TPOTValid:            true,
	}
}
