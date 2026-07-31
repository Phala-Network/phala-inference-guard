package predictive

import (
	"testing"
	"time"
)

func TestTokenizerManifestCompatibilityRequiresExactProfile(t *testing.T) {
	base := TokenizerManifest{
		ProfileID:             "gemma4-vllm",
		ServedModel:           "google/gemma-4-31B-it",
		ModelRepository:       "RedHatAI/gemma-4-31B-it-FP8-dynamic",
		ModelRevision:         "model-rev",
		TokenizerRepository:   "RedHatAI/gemma-4-31B-it-FP8-dynamic",
		TokenizerRevision:     "tokenizer-rev",
		TokenizerSHA256:       "tokenizer-sha",
		TokenizerConfigSHA256: "tokenizer-config-sha",
		SpecialTokensSHA256:   "special-tokens-sha",
		TemplateSHA256:        "template-sha",
		BackendKind:           "vllm",
		BackendVersion:        "0.25.1",
		BlockSize:             64,
		MultimodalProfile:     "text-only",
		PredictorVersion:      "v0.9.1-test",
	}
	if !base.Compatible(base) {
		t.Fatal("identical manifest must be compatible")
	}

	cases := []struct {
		name   string
		mutate func(*TokenizerManifest)
	}{
		{name: "served model", mutate: func(value *TokenizerManifest) { value.ServedModel = "other" }},
		{name: "model repository", mutate: func(value *TokenizerManifest) { value.ModelRepository = "other" }},
		{name: "backend version", mutate: func(value *TokenizerManifest) { value.BackendVersion = "0.26.0" }},
		{name: "model revision", mutate: func(value *TokenizerManifest) { value.ModelRevision = "other" }},
		{name: "tokenizer repository", mutate: func(value *TokenizerManifest) { value.TokenizerRepository = "other" }},
		{name: "tokenizer revision", mutate: func(value *TokenizerManifest) { value.TokenizerRevision = "other" }},
		{name: "tokenizer", mutate: func(value *TokenizerManifest) { value.TokenizerSHA256 = "other" }},
		{name: "tokenizer config", mutate: func(value *TokenizerManifest) { value.TokenizerConfigSHA256 = "other" }},
		{name: "special tokens", mutate: func(value *TokenizerManifest) { value.SpecialTokensSHA256 = "other" }},
		{name: "template", mutate: func(value *TokenizerManifest) { value.TemplateSHA256 = "other" }},
		{name: "block size", mutate: func(value *TokenizerManifest) { value.BlockSize = 32 }},
		{name: "multimodal profile", mutate: func(value *TokenizerManifest) { value.MultimodalProfile = "image" }},
		{name: "predictor version", mutate: func(value *TokenizerManifest) { value.PredictorVersion = "other" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.mutate(&changed)
			if base.Compatible(changed) {
				t.Fatalf("manifest with changed %s must be incompatible", tc.name)
			}
		})
	}
	invalid := base
	invalid.TokenizerRevision = ""
	if err := invalid.Validate(); err == nil {
		t.Fatal("manifest with missing required field must be invalid")
	}
}

func TestCacheHitIntervalValidation(t *testing.T) {
	valid := CacheHitInterval{
		Certain:  4096,
		Lower:    4096,
		Expected: 6144,
		Upper:    8192,
	}
	if err := valid.Validate(8192); err != nil {
		t.Fatalf("valid interval rejected: %v", err)
	}

	invalid := []CacheHitInterval{
		{Certain: -1},
		{Certain: 2, Lower: 1, Expected: 2, Upper: 2},
		{Certain: 1, Lower: 2, Expected: 1, Upper: 2},
		{Certain: 1, Lower: 2, Expected: 3, Upper: 2},
		{Certain: 1, Lower: 2, Expected: 3, Upper: 8193},
	}
	for index, interval := range invalid {
		if err := interval.Validate(8192); err == nil {
			t.Fatalf("invalid interval %d accepted: %+v", index, interval)
		}
	}
}

func TestVLLMExactTokenProjectionUsesOnlyCertainCacheAndRoundsBlocks(t *testing.T) {
	increment, err := ProjectVLLM(VLLMProjectionInput{
		InputTokens:        4097,
		CacheHits:          CacheHitInterval{Certain: 4096, Lower: 4096, Expected: 4096, Upper: 4096},
		DecodeHorizonUpper: 1,
		BlockSize:          64,
	})
	if err != nil {
		t.Fatalf("projection failed: %v", err)
	}
	if increment.PhysicalKVUpper != 64 {
		t.Fatalf("physical upper = %d, want one rounded block", increment.PhysicalKVUpper)
	}
	if increment.CacheDiscountTokens != 4096 {
		t.Fatalf("cache discount = %d, want certain hit only", increment.CacheDiscountTokens)
	}
}

func TestEvaluateProtectsExistingUsersBeforeOtherSoftConstraints(t *testing.T) {
	decision := Evaluate(EvaluationInput{
		Projection: Projection{
			PhysicalKVUpper: 75_000,
			ActiveKVUpper:   75_000,
		},
		Scheduler: SchedulerEstimate{
			ExistingUserTPSLower: 24.9,
			AllUserTPSLower:      30,
			TTFTUpper:            200 * time.Millisecond,
			TPOTUpper:            30 * time.Millisecond,
		},
		Constraints: Constraints{
			PhysicalKVHard:    88_000,
			ActiveKVHard:      88_000,
			UserTPSTarget:     25,
			TTFTSLO:           time.Second,
			TPOTSLO:           50 * time.Millisecond,
			MinimumConfidence: 0.90,
		},
		Confidence: 0.99,
	})
	if decision.Reason != ReasonExistingTPSAtRisk {
		t.Fatalf("reason = %s, want %s", decision.Reason, ReasonExistingTPSAtRisk)
	}
}

func TestEvaluateFitsOnlyWhenEveryBoundPasses(t *testing.T) {
	decision := Evaluate(EvaluationInput{
		Projection: Projection{
			PhysicalKVUpper: 75_000,
			ActiveKVUpper:   76_000,
		},
		Scheduler: SchedulerEstimate{
			ExistingUserTPSLower: 27,
			AllUserTPSLower:      26,
			TTFTUpper:            500 * time.Millisecond,
			TPOTUpper:            35 * time.Millisecond,
			WorkspaceRiskUpper:   0.01,
			PreemptionRiskUpper:  0.001,
		},
		Constraints: Constraints{
			PhysicalKVHard:       88_000,
			ActiveKVHard:         84_000,
			UserTPSTarget:        25,
			TTFTSLO:              time.Second,
			TPOTSLO:              40 * time.Millisecond,
			WorkspaceRiskBudget:  0.02,
			PreemptionRiskBudget: 0.002,
			MinimumConfidence:    0.95,
		},
		Confidence: 0.99,
	})
	if decision.Reason != ReasonFit {
		t.Fatalf("reason = %s, want %s", decision.Reason, ReasonFit)
	}
}
