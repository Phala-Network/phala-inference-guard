package goodput

import (
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type learnedFrontierResult struct {
	MaximumMatureConcurrency int
	MaximumAttempted         int
	HardBandOutcomes         int
	SoftBandOutcomes         int
	ThroughputFrontierStops  int
	StableCompletionTPS      float64
	AttemptsByConcurrency    map[int]int
	LastDecisionReason       domainpredictive.Reason
	Learning                 runtimepredictive.LearnedSchedulerSnapshot
}

func TestThroughputFirstFrontierReachesSafeDecodeEight(t *testing.T) {
	actualTPS := func(int) float64 { return 50 }
	candidate := runLearnedFrontier(t, 25, actualTPS, 8)
	baseline := runStaticFrontier(t, actualTPS, 8)
	oracleTPS := 8 * actualTPS(8)
	t.Logf(
		"safe_frontier mature=%d attempted=%d completion_tps=%.2f oracle_tps=%.2f ablation_tps=%.2f",
		candidate.MaximumMatureConcurrency, candidate.MaximumAttempted,
		candidate.StableCompletionTPS, oracleTPS, baseline.StableCompletionTPS,
	)

	if candidate.MaximumMatureConcurrency != 8 || candidate.MaximumAttempted != 8 {
		t.Fatalf("safe frontier did not reach decode 8: %+v", candidate)
	}
	if candidate.HardBandOutcomes != 0 || candidate.SoftBandOutcomes != 0 {
		t.Fatalf("safe frontier produced QoS misses: %+v", candidate)
	}
	if candidate.StableCompletionTPS < 0.80*oracleTPS {
		t.Fatalf("safe frontier completion TPS = %.2f, want at least 80%% of oracle %.2f", candidate.StableCompletionTPS, oracleTPS)
	}
	if improvementPercent(int64(candidate.StableCompletionTPS), int64(baseline.StableCompletionTPS)) < 30 {
		t.Fatalf("safe frontier completion TPS candidate/baseline = %.2f/%.2f, want >=30%% improvement", candidate.StableCompletionTPS, baseline.StableCompletionTPS)
	}
}

func TestThroughputFirstFrontierRetreatsFromHarmfulSoftQoSKnee(t *testing.T) {
	actualTPS := func(concurrency int) float64 {
		if concurrency <= 6 {
			return 50
		}
		return 22
	}
	candidate := runLearnedFrontier(t, 25, actualTPS, 8)
	t.Logf(
		"harmful_soft_knee mature=%d attempted=%d completion_tps=%.2f hard_outcomes=%d soft_outcomes=%d frontier_stops=%d attempts=%v",
		candidate.MaximumMatureConcurrency, candidate.MaximumAttempted,
		candidate.StableCompletionTPS, candidate.HardBandOutcomes,
		candidate.SoftBandOutcomes, candidate.ThroughputFrontierStops, candidate.AttemptsByConcurrency,
	)

	if candidate.MaximumMatureConcurrency != 6 || candidate.MaximumAttempted != 7 {
		t.Fatalf("harmful soft knee did not retreat to decode 6: %+v", candidate)
	}
	if candidate.HardBandOutcomes != 0 || candidate.SoftBandOutcomes != 3 {
		t.Fatalf("soft knee hard/soft outcomes = %d/%d, want 0/3", candidate.HardBandOutcomes, candidate.SoftBandOutcomes)
	}
	if candidate.AttemptsByConcurrency[7] != 3 || candidate.AttemptsByConcurrency[8] != 0 ||
		candidate.ThroughputFrontierStops != 1 || candidate.LastDecisionReason != domainpredictive.ReasonThroughputFrontier {
		t.Fatalf("harmful soft knee exposure/frontier = %+v", candidate)
	}
	if candidate.Learning.AdverseEvidenceEvents != 0 || candidate.Learning.SoftExistingTPSMisses != 3 ||
		candidate.Learning.SoftNewTPSMisses != 3 || candidate.Learning.SoftTPOTMisses != 3 {
		t.Fatalf("soft knee learning classification = %+v", candidate.Learning)
	}
	if candidate.StableCompletionTPS != 6*actualTPS(6) || candidate.StableCompletionTPS <= 7*actualTPS(7) {
		t.Fatalf("harmful soft knee kept a lower-throughput bucket: %+v", candidate)
	}
}

func TestThroughputFirstFrontierKeepsBeneficialSoftQoSKnee(t *testing.T) {
	actualTPS := func(concurrency int) float64 {
		switch {
		case concurrency <= 6:
			return 50
		case concurrency == 7:
			return 44
		case concurrency == 8:
			return 40
		default:
			return 33
		}
	}
	candidate := runLearnedFrontier(t, 45, actualTPS, 9)
	t.Logf(
		"beneficial_soft_knee mature=%d attempted=%d completion_tps=%.2f hard_outcomes=%d soft_outcomes=%d frontier_stops=%d attempts=%v",
		candidate.MaximumMatureConcurrency, candidate.MaximumAttempted,
		candidate.StableCompletionTPS, candidate.HardBandOutcomes,
		candidate.SoftBandOutcomes, candidate.ThroughputFrontierStops, candidate.AttemptsByConcurrency,
	)

	if candidate.MaximumMatureConcurrency != 8 || candidate.MaximumAttempted != 9 {
		t.Fatalf("beneficial soft knee did not retain every throughput-improving bucket: %+v", candidate)
	}
	if candidate.HardBandOutcomes != 0 || candidate.SoftBandOutcomes != 9 ||
		candidate.AttemptsByConcurrency[7] != 3 || candidate.AttemptsByConcurrency[8] != 3 ||
		candidate.AttemptsByConcurrency[9] != 3 || candidate.ThroughputFrontierStops != 1 {
		t.Fatalf("beneficial soft knee exposure/classification = %+v", candidate)
	}
	if candidate.StableCompletionTPS != 8*actualTPS(8) ||
		candidate.StableCompletionTPS <= 7*actualTPS(7) ||
		candidate.StableCompletionTPS <= 9*actualTPS(9) {
		t.Fatalf("beneficial soft knee discarded higher total throughput: %+v", candidate)
	}
}

func TestThroughputFirstFrontierRetreatsAfterOneHardKneeProbe(t *testing.T) {
	actualTPS := func(concurrency int) float64 {
		if concurrency <= 6 {
			return 50
		}
		return 10
	}
	candidate := runLearnedFrontier(t, 25, actualTPS, 8)
	t.Logf(
		"hard_knee mature=%d attempted=%d completion_tps=%.2f hard_outcomes=%d soft_outcomes=%d attempts=%v",
		candidate.MaximumMatureConcurrency, candidate.MaximumAttempted,
		candidate.StableCompletionTPS, candidate.HardBandOutcomes,
		candidate.SoftBandOutcomes, candidate.AttemptsByConcurrency,
	)

	if candidate.MaximumMatureConcurrency != 6 || candidate.MaximumAttempted != 7 {
		t.Fatalf("hard knee frontier = %+v, want mature 6 and one attempted bucket 7", candidate)
	}
	if candidate.AttemptsByConcurrency[7] != 1 || candidate.AttemptsByConcurrency[8] != 0 {
		t.Fatalf("hard knee exposure budget = %+v, want one decode-7 probe and no decode-8 probe", candidate.AttemptsByConcurrency)
	}
	if candidate.HardBandOutcomes != 1 || candidate.SoftBandOutcomes != 0 || candidate.Learning.AdverseEvidenceEvents != 2 {
		t.Fatalf("hard knee outcome classification = %+v", candidate)
	}
	wantStableTPS := 6 * actualTPS(6)
	if candidate.StableCompletionTPS != wantStableTPS {
		t.Fatalf("hard knee stable completion TPS = %.2f, want %.2f", candidate.StableCompletionTPS, wantStableTPS)
	}
}

func runStaticFrontier(t *testing.T, actualTPS func(int) float64, maximumConcurrency int) learnedFrontierResult {
	t.Helper()
	const hardTarget = 15.0
	identity := runtimepredictive.ModelIdentity{
		ProfileID: "frontier-static-ablation", BackendEpoch: "frontier-epoch", PredictorVersion: "frontier-v1",
	}
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity: identity, BaseCompletionTPS: 40, PrefillTPSPenaltyPerKToken: 0,
		BaseTTFT: time.Millisecond, BaseTPOT: 25 * time.Millisecond,
		TPOTPerExistingDecodeSequence: 25 * time.Millisecond,
		Confidence:                    0.99,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity: identity, MinimumSamples: 3, MaximumSamplesPerCell: 16, MaximumCells: 64,
		MaxAge: time.Minute, LowerQuantile: 0.10, UpperQuantile: 0.90,
		MinimumTPSMultiplier: 0.10, MaximumTPSMultiplier: 12,
		MinimumLatencyMultiplier: 0.10, MaximumLatencyMultiplier: 8,
		CalibratedConfidence: 0.99, DecodeSequenceBucket: 1,
		ContextTokenBucket: 128, PrefillTokenBucket: 128, KVTokenBucket: 128,
		AdverseEvidenceMaxAge: 5 * time.Second,
		HardUserTPSTarget:     hardTarget, HardTPOTSLO: reciprocalTPSDuration(hardTarget),
	})
	if err != nil {
		t.Fatalf("new static frontier ablation: %v", err)
	}
	cost := domainpredictive.RequestCost{
		ManifestID: "frontier-static-ablation", InputTokens: 64,
		KV:                   domainpredictive.KVIncrement{PhysicalKVUpper: 128, ActiveKVUpper: 128},
		FutureKV:             domainpredictive.KVIncrement{PhysicalKVUpper: 64, ActiveKVUpper: 64},
		UncachedPrefillUpper: 64, DecodeHorizonUpper: 64, DecodeSequencesUpper: 1,
		ActiveContextTokensUpper: 128, FutureContextTokensUpper: 64, Confidence: 0.99,
	}
	result := learnedFrontierResult{AttemptsByConcurrency: make(map[int]int, maximumConcurrency)}
	start := time.Unix(19_000, 0)
	for concurrency := 1; concurrency <= maximumConcurrency; concurrency++ {
		prediction := scheduler.Predict(start.Add(time.Duration(concurrency)*time.Second), frontierState(concurrency-1), cost)
		decision := evaluateFrontierPrediction(prediction, hardTarget, reciprocalTPSDuration(hardTarget))
		if decision.Reason != domainpredictive.ReasonFit {
			result.LastDecisionReason = decision.Reason
			break
		}
		result.MaximumAttempted = concurrency
		result.MaximumMatureConcurrency = concurrency
		result.AttemptsByConcurrency[concurrency]++
	}
	if result.MaximumMatureConcurrency > 0 {
		result.StableCompletionTPS = float64(result.MaximumMatureConcurrency) * actualTPS(result.MaximumMatureConcurrency)
	}
	return result
}

func runLearnedFrontier(t *testing.T, softTarget float64, actualTPS func(int) float64, maximumConcurrency int) learnedFrontierResult {
	t.Helper()
	const hardTarget = 15.0
	hardTPOT := reciprocalTPSDuration(hardTarget)
	softTPOT := reciprocalTPSDuration(softTarget)
	identity := runtimepredictive.ModelIdentity{
		ProfileID: "frontier-simulation", BackendEpoch: "frontier-epoch", PredictorVersion: "frontier-v1",
	}
	config := runtimepredictive.ResidualCalibratorConfig{
		Identity: identity, MinimumSamples: 3, MaximumSamplesPerCell: 16, MaximumCells: 64,
		MaxAge: time.Minute, LowerQuantile: 0.10, UpperQuantile: 0.90,
		MinimumTPSMultiplier: 0.10, MaximumTPSMultiplier: 12,
		MinimumLatencyMultiplier: 0.10, MaximumLatencyMultiplier: 8,
		CalibratedConfidence: 0.99, DecodeSequenceBucket: 1,
		ContextTokenBucket: 128, PrefillTokenBucket: 128, KVTokenBucket: 128,
		AdverseEvidenceMaxAge: 5 * time.Second,
		HardUserTPSTarget:     hardTarget, HardTPOTSLO: hardTPOT,
		ExplorationUserTPSTarget: softTarget, ExplorationTPOTSLO: softTPOT,
	}
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity: identity, BaseCompletionTPS: 40, PrefillTPSPenaltyPerKToken: 0,
		BaseTTFT: time.Millisecond, BaseTPOT: 25 * time.Millisecond,
		TPOTPerExistingDecodeSequence: 25 * time.Millisecond,
		Confidence:                    0.99,
	}, config)
	if err != nil {
		t.Fatalf("new frontier scheduler: %v", err)
	}
	cost := domainpredictive.RequestCost{
		ManifestID: "frontier-simulation", InputTokens: 64,
		KV:                   domainpredictive.KVIncrement{PhysicalKVUpper: 128, ActiveKVUpper: 128},
		FutureKV:             domainpredictive.KVIncrement{PhysicalKVUpper: 64, ActiveKVUpper: 64},
		UncachedPrefillUpper: 64, DecodeHorizonUpper: 64, DecodeSequencesUpper: 1,
		ActiveContextTokensUpper: 128, FutureContextTokensUpper: 64, Confidence: 0.99,
	}
	result := learnedFrontierResult{AttemptsByConcurrency: make(map[int]int, maximumConcurrency)}
	start := time.Unix(20_000, 0)
	step := 0
	for concurrency := 1; concurrency <= maximumConcurrency; concurrency++ {
		matureSamples := 0
		for sample := 0; sample < config.MinimumSamples; sample++ {
			at := start.Add(time.Duration(step) * time.Second)
			step++
			state := frontierState(concurrency - 1)
			prediction := scheduler.Predict(at, state, cost)
			decision := evaluateFrontierPrediction(prediction, hardTarget, hardTPOT)
			if decision.Reason != domainpredictive.ReasonFit {
				result.LastDecisionReason = decision.Reason
				if decision.Reason == domainpredictive.ReasonThroughputFrontier {
					result.ThroughputFrontierStops++
				}
				break
			}

			result.MaximumAttempted = concurrency
			result.AttemptsByConcurrency[concurrency]++
			actual := actualTPS(concurrency)
			if actual < hardTarget {
				result.HardBandOutcomes++
			} else if actual < softTarget {
				result.SoftBandOutcomes++
			}
			outcome := runtimepredictive.SchedulerOutcome{
				Identity: identity, ObservedAt: at.Add(time.Millisecond), Attributed: true,
				UserTPS: actual, UserTPSValid: true,
				TPOT: reciprocalTPSDuration(actual), TPOTValid: true,
			}
			if concurrency > 1 {
				if err := scheduler.ObserveExistingPrefill(runtimepredictive.ExistingPrefillOutcome{
					Identity: identity, StartedAt: at, ObservedAt: at.Add(500 * time.Microsecond),
					Features: prediction.Features, ExistingDecodeSequences: concurrency - 1,
					PendingPrefillSequences: 1, PendingPrefillTokens: cost.UncachedPrefillUpper,
					ExistingUserTPS: actual,
				}); err != nil {
					t.Fatalf("observe existing-prefill frontier concurrency %d sample %d: %v", concurrency, sample, err)
				}
			}
			if err := scheduler.Observe(prediction, outcome); err != nil {
				t.Fatalf("observe frontier concurrency %d sample %d: %v", concurrency, sample, err)
			}
			if err := scheduler.ObserveAggregateThroughput(runtimepredictive.AggregateThroughputOutcome{
				Identity: identity, StartedAt: at.Add(time.Millisecond), ObservedAt: at.Add(2 * time.Millisecond),
				DecodeSequences: concurrency, AggregateCompletionTPS: float64(concurrency) * actual,
			}); err != nil {
				t.Fatalf("observe aggregate frontier concurrency %d sample %d: %v", concurrency, sample, err)
			}
			matureSamples++
		}
		if matureSamples < config.MinimumSamples {
			break
		}
		stabilizeAt := start.Add(time.Duration(step) * time.Second)
		step++
		stabilized := scheduler.Predict(stabilizeAt, frontierState(concurrency-1), cost)
		stabilizedDecision := evaluateFrontierPrediction(stabilized, hardTarget, hardTPOT)
		if stabilizedDecision.Reason != domainpredictive.ReasonFit {
			result.LastDecisionReason = stabilizedDecision.Reason
			if stabilizedDecision.Reason == domainpredictive.ReasonThroughputFrontier {
				result.ThroughputFrontierStops++
			}
			break
		}
		result.MaximumMatureConcurrency = concurrency
	}
	if result.MaximumMatureConcurrency > 0 {
		result.StableCompletionTPS = float64(result.MaximumMatureConcurrency) * actualTPS(result.MaximumMatureConcurrency)
	}
	result.Learning = scheduler.Snapshot()
	return result
}

func evaluateFrontierPrediction(prediction runtimepredictive.SchedulerPrediction, hardTarget float64, hardTPOT time.Duration) domainpredictive.Decision {
	return domainpredictive.Evaluate(domainpredictive.EvaluationInput{
		Projection: domainpredictive.Projection{
			PhysicalKVUpper: prediction.Features.PhysicalKVUpper,
			ActiveKVUpper:   prediction.Features.ActiveKVUpper,
		},
		Scheduler: prediction.Estimate,
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard: 1_000_000, ActiveKVHard: 1_000_000,
			UserTPSTarget: hardTarget, TPOTSLO: hardTPOT,
			WorkspaceRiskBudget: 1, PreemptionRiskBudget: 1, MinimumConfidence: 0.90,
		},
		Confidence: prediction.Confidence,
	})
}

func reciprocalTPSDuration(tps float64) time.Duration {
	return time.Duration(float64(time.Second) / tps)
}

func frontierState(existing int) domainpredictive.VirtualState {
	return domainpredictive.VirtualState{
		PhysicalKVUpper: int64(existing) * 128, ActiveKVUpper: int64(existing) * 128,
		DecodeSequences: existing, ActiveContextTokens: int64(existing) * 128,
	}
}
