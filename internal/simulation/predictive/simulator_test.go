package predictive

import (
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func simulationConstraints() domain.Constraints {
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

func TestScenarioRequiresTokenizerManifestIdentity(t *testing.T) {
	if _, err := Run(time.Unix(0, 0), Scenario{}, constantSafeScheduler{}); err == nil {
		t.Fatal("scenario without tokenizer manifest identity must fail")
	}
}

func TestScenarioCompletionReopensBeforeNextPoll(t *testing.T) {
	scenario := Scenario{
		ManifestID: "test-profile",
		Initial: domain.VirtualState{
			PhysicalKVUpper: 70_000,
			ActiveKVUpper:   70_000,
		},
		Constraints: domain.Constraints{
			PhysicalKVHard:    85_000,
			ActiveKVHard:      85_000,
			UserTPSTarget:     25,
			TPOTSLO:           50 * time.Millisecond,
			MinimumConfidence: 0.95,
		},
		Events: []Event{
			{At: 0, Kind: EventAdmit, ID: "first", Cost: fixedCost(10_000)},
			{At: time.Millisecond, Kind: EventAdmit, ID: "blocked", Cost: fixedCost(10_000)},
			{At: 2 * time.Millisecond, Kind: EventComplete, ID: "first"},
			{At: 3 * time.Millisecond, Kind: EventAdmit, ID: "reopened", Cost: fixedCost(10_000)},
		},
	}
	result, err := Run(time.Unix(0, 0), scenario, constantSafeScheduler{})
	if err != nil {
		t.Fatalf("run failed: %v", err)
	}
	want := []domain.Reason{domain.ReasonFit, domain.ReasonKVOverBudget, domain.ReasonFit}
	if len(result.Decisions) != len(want) {
		t.Fatalf("decisions = %v, want %v", result.Decisions, want)
	}
	for index := range want {
		if result.Decisions[index].Reason != want[index] {
			t.Fatalf("decision %d = %s, want %s", index, result.Decisions[index].Reason, want[index])
		}
	}
	if result.SampleEvents != 0 || result.Completions != 1 {
		t.Fatalf("sample/completion events = %d/%d, want 0/1", result.SampleEvents, result.Completions)
	}
}

type constantSafeScheduler struct{}

func (constantSafeScheduler) Identity() runtimepredictive.ModelIdentity {
	return simulationSchedulerIdentity("constant-safe")
}

func (constantSafeScheduler) Predict(now time.Time, _ domain.VirtualState, _ domain.RequestCost) runtimepredictive.SchedulerPrediction {
	return runtimepredictive.SchedulerPrediction{
		Identity:    simulationSchedulerIdentity("constant-safe"),
		PredictedAt: now,
		Estimate: domain.SchedulerEstimate{
			ExistingUserTPSLower: 30,
			NewUserTPSLower:      30,
			TTFTUpper:            100 * time.Millisecond,
			TPOTUpper:            25 * time.Millisecond,
		},
		Confidence: 0.99,
	}
}

func simulationSchedulerIdentity(profile string) runtimepredictive.ModelIdentity {
	return runtimepredictive.ModelIdentity{
		ProfileID:        profile,
		BackendEpoch:     "simulation-backend-1",
		PredictorVersion: "simulation-v1",
	}
}

func fixedCost(tokens int64) domain.RequestCost {
	return domain.RequestCost{
		ManifestID:  "test-profile",
		InputTokens: tokens,
		KV: domain.KVIncrement{
			PhysicalKVUpper: tokens,
			ActiveKVUpper:   tokens,
		},
		UncachedPrefillUpper:     tokens,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: tokens,
		Confidence:               0.99,
	}
}
