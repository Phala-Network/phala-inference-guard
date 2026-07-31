package predictive

import (
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type prefillSensitiveScheduler struct{}

func (prefillSensitiveScheduler) Predict(_ domain.VirtualState, request domain.RequestCost) domain.SchedulerEstimate {
	tps := 30 - float64(request.UncachedPrefillUpper)/5_000
	return domain.SchedulerEstimate{
		ExistingUserTPSLower: tps,
		AllUserTPSLower:      tps,
		TTFTUpper:            100 * time.Millisecond,
		TPOTUpper:            25 * time.Millisecond,
	}
}

func simulationConstraints() domain.Constraints {
	return domain.Constraints{
		PhysicalKVHard:       100_000,
		ActiveKVHard:         100_000,
		UserTPSTarget:        25,
		TTFTSLO:              time.Second,
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
		Initial: domain.VirtualState{
			PhysicalKVUpper: 70_000,
			ActiveKVUpper:   70_000,
		},
		Constraints: domain.Constraints{
			PhysicalKVHard:    85_000,
			ActiveKVHard:      85_000,
			UserTPSTarget:     25,
			TTFTSLO:           time.Second,
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

func TestScenarioCertainCacheHitProtectsPrefillTPS(t *testing.T) {
	cold := cacheAwareCost(t, 64_000, 0)
	hot := cacheAwareCost(t, 64_000, 60_000)

	coldManager := runtimepredictive.NewManager(domain.VirtualState{}, simulationConstraints(), prefillSensitiveScheduler{})
	coldDecision := coldManager.DecideAndReserve(time.Unix(0, 0), "cold", cold)
	if coldDecision.Reason != domain.ReasonExistingTPSAtRisk {
		t.Fatalf("cold reason = %s, want existing TPS risk", coldDecision.Reason)
	}

	hotManager := runtimepredictive.NewManager(domain.VirtualState{}, simulationConstraints(), prefillSensitiveScheduler{})
	hotDecision := hotManager.DecideAndReserve(time.Unix(0, 0), "hot", hot)
	if hotDecision.Reason != domain.ReasonFit {
		t.Fatalf("hot reason = %s, want fit", hotDecision.Reason)
	}
	if hot.UncachedPrefillUpper != 4_000 {
		t.Fatalf("hot uncached prefill = %d, want 4000", hot.UncachedPrefillUpper)
	}
}

type constantSafeScheduler struct{}

func (constantSafeScheduler) Predict(_ domain.VirtualState, _ domain.RequestCost) domain.SchedulerEstimate {
	return domain.SchedulerEstimate{
		ExistingUserTPSLower: 30,
		AllUserTPSLower:      30,
		TTFTUpper:            100 * time.Millisecond,
		TPOTUpper:            25 * time.Millisecond,
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
		UncachedPrefillUpper: tokens,
		Confidence:           0.99,
	}
}

func cacheAwareCost(t *testing.T, inputTokens, certainHits int64) domain.RequestCost {
	t.Helper()
	increment, err := domain.ProjectVLLM(domain.VLLMProjectionInput{
		InputTokens: inputTokens,
		CacheHits: domain.CacheHitInterval{
			Certain:  certainHits,
			Lower:    certainHits,
			Expected: certainHits,
			Upper:    certainHits,
		},
		DecodeHorizonUpper: 256,
		BlockSize:          64,
	})
	if err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	return domain.RequestCost{
		ManifestID:           "test-profile",
		InputTokens:          inputTokens,
		KV:                   increment,
		UncachedPrefillUpper: inputTokens - certainHits,
		Confidence:           0.99,
	}
}
