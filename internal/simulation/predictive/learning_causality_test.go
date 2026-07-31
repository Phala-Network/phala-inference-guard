package predictive

import (
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestSampleEventReconcilesVirtualStateInsteadOfOnlyIncrementingCounter(t *testing.T) {
	scenario := Scenario{
		ManifestID: "test-profile",
		Initial: domain.VirtualState{
			PhysicalKVUpper: 50_000,
			ActiveKVUpper:   50_000,
		},
		Constraints: simulationConstraints(),
		Events: []Event{{
			At:   time.Second,
			Kind: EventSample,
			Sample: runtimepredictive.SampleWindow{
				Observed: domain.VirtualState{
					PhysicalKVUpper: 40_000,
					ActiveKVUpper:   38_000,
				},
				StartedSequence:  0,
				FinishedSequence: 0,
			},
		}},
	}
	result, err := Run(time.Unix(6_000, 0), scenario, constantSafeScheduler{})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.SampleEvents != 1 {
		t.Fatalf("sample events = %d, want 1", result.SampleEvents)
	}
	if result.FinalSnapshot.Virtual.Lower.PhysicalKVUpper != 40_000 || result.FinalSnapshot.Virtual.Upper.ActiveKVUpper != 38_000 {
		t.Fatalf("final virtual state = %+v, want reconciled sample", result.FinalSnapshot.Virtual)
	}
}

func TestSimulationDecisionChangesOnlyAfterEligibleLearning(t *testing.T) {
	now := time.Unix(7_000, 0)
	profile := runtimepredictive.StaticSchedulerProfile{
		Identity: runtimepredictive.ModelIdentity{
			ProfileID:        "simulation-profile",
			BackendEpoch:     "simulation-epoch",
			PredictorVersion: "simulation-v1",
		},
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
	config := runtimepredictive.ResidualCalibratorConfig{
		Identity:                 profile.Identity,
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
	coldScheduler, err := runtimepredictive.NewLearnedScheduler(profile, config)
	if err != nil {
		t.Fatalf("new cold scheduler: %v", err)
	}
	trainedScheduler, err := runtimepredictive.NewLearnedScheduler(profile, config)
	if err != nil {
		t.Fatalf("new trained scheduler: %v", err)
	}
	state := domain.VirtualState{
		PhysicalKVUpper:     40_000,
		ActiveKVUpper:       36_000,
		DecodeSequences:     2,
		ActiveContextTokens: 24_000,
	}
	cost := domain.RequestCost{
		ManifestID:            "test-profile",
		InputTokens:           8_000,
		KV:                    domain.KVIncrement{PhysicalKVUpper: 8_256, ActiveKVUpper: 8_256},
		UncachedPrefillUpper:  1_000,
		CachedPrefillExpected: 7_000,
		DecodeHorizonUpper:    256,
		Confidence:            0.99,
	}
	for index := 0; index < config.MinimumSamples; index++ {
		prediction := trainedScheduler.Predict(now.Add(time.Duration(index)*time.Second), state, cost)
		outcome := runtimepredictive.SchedulerOutcome{
			Identity:             prediction.Identity,
			ObservedAt:           now.Add(time.Duration(index+1) * time.Second),
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
		if err := trainedScheduler.Observe(prediction, outcome); err != nil {
			t.Fatalf("observe %d: %v", index, err)
		}
	}
	scenario := Scenario{
		ManifestID:  "test-profile",
		Initial:     state,
		Constraints: simulationConstraints(),
		Events:      []Event{{At: 0, Kind: EventAdmit, ID: "candidate", Cost: cost}},
	}
	cold, err := Run(now.Add(5*time.Second), scenario, coldScheduler)
	if err != nil {
		t.Fatalf("run cold: %v", err)
	}
	learned, err := Run(now.Add(5*time.Second), scenario, trainedScheduler)
	if err != nil {
		t.Fatalf("run learned: %v", err)
	}
	if cold.Decisions[0].Reason != domain.ReasonNewTPSAtRisk || learned.Decisions[0].Reason != domain.ReasonFit {
		t.Fatalf("cold/learned reasons = %s/%s, want new_tps_at_risk/fit", cold.Decisions[0].Reason, learned.Decisions[0].Reason)
	}
	if cold.Decisions[0].Projection != learned.Decisions[0].Projection {
		t.Fatalf("current projection changed: cold=%+v learned=%+v", cold.Decisions[0].Projection, learned.Decisions[0].Projection)
	}
}
