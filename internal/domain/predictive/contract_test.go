package predictive

import (
	"testing"
	"time"
)

func TestTokenizerManifestCompatibilityRequiresExactProfile(t *testing.T) {
	base := TokenizerManifest{
		ProfileID:              "gemma4-vllm",
		ServedModel:            "google/gemma-4-31B-it",
		ModelRepository:        "RedHatAI/gemma-4-31B-it-FP8-dynamic",
		ModelRevision:          "model-rev",
		TokenizerRepository:    "RedHatAI/gemma-4-31B-it-FP8-dynamic",
		TokenizerRevision:      "tokenizer-rev",
		TokenizerSHA256:        "tokenizer-sha",
		TokenizerConfigSHA256:  "tokenizer-config-sha",
		SpecialTokensSHA256:    "special-tokens-sha",
		TemplateSHA256:         "template-sha",
		TemplateSource:         "vllm@backend-rev:examples/tool_chat_template_gemma4.jinja",
		TemplateRuntime:        "minijinja-vllm-profile",
		TemplateRuntimeVersion: "v1",
		SpecialTokenPolicies: SpecialTokenPolicies{
			Completions:     SpecialTokenPolicyAdd,
			ChatCompletions: SpecialTokenPolicyOmit,
		},
		SpecialTokens: SpecialTokenBindings{
			BOS: TokenBinding{Value: "<bos>", ID: 2},
			EOS: TokenBinding{Value: "<eos>", ID: 1},
			UNK: TokenBinding{Value: "<unk>", ID: 3},
			PAD: TokenBinding{Value: "<pad>", ID: 0},
		},
		Capabilities: TokenizerCapabilities{
			Completions:     true,
			ChatCompletions: true,
			Tools:           true,
			ToolChoice:      true,
			ResponseFormat:  true,
			JSONSchema:      true,
			Reasoning:       true,
		},
		BackendKind:           "vllm",
		BackendVersion:        "0.25.1",
		BackendSourceRevision: "backend-rev",
		BackendImageDigest:    "sha256:backend-image",
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
		{name: "template source", mutate: func(value *TokenizerManifest) { value.TemplateSource = "other" }},
		{name: "template runtime", mutate: func(value *TokenizerManifest) { value.TemplateRuntime = "other" }},
		{name: "template runtime version", mutate: func(value *TokenizerManifest) { value.TemplateRuntimeVersion = "other" }},
		{name: "completion special token policy", mutate: func(value *TokenizerManifest) { value.SpecialTokenPolicies.Completions = SpecialTokenPolicyOmit }},
		{name: "chat special token policy", mutate: func(value *TokenizerManifest) { value.SpecialTokenPolicies.ChatCompletions = SpecialTokenPolicyAdd }},
		{name: "bos token", mutate: func(value *TokenizerManifest) { value.SpecialTokens.BOS.Value = "<s>" }},
		{name: "eos token id", mutate: func(value *TokenizerManifest) { value.SpecialTokens.EOS.ID = 212 }},
		{name: "tools capability", mutate: func(value *TokenizerManifest) { value.Capabilities.Tools = false }},
		{name: "backend source revision", mutate: func(value *TokenizerManifest) { value.BackendSourceRevision = "other" }},
		{name: "backend image digest", mutate: func(value *TokenizerManifest) { value.BackendImageDigest = "sha256:other" }},
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

	invalid = base
	invalid.SpecialTokens.BOS.ID = -1
	if err := invalid.Validate(); err == nil {
		t.Fatal("manifest with a negative declared special-token id must be invalid")
	}

	invalid = base
	invalid.SpecialTokenPolicies.ChatCompletions = "heuristic"
	if err := invalid.Validate(); err == nil {
		t.Fatal("manifest with an unknown special-token policy must be invalid")
	}

	invalid = base
	invalid.Capabilities.ChatCompletions = false
	if err := invalid.Validate(); err == nil {
		t.Fatal("tools without chat-completions capability must be invalid")
	}

	invalid = base
	invalid.Capabilities.Multimodal = true
	if err := invalid.Validate(); err == nil {
		t.Fatal("multimodal capability with a text-only processor profile must be invalid")
	}
}

func TestTokenizerManifestRequiresSpecialTokenPolicyExactlyForEnabledRequestClasses(t *testing.T) {
	base := TokenizerManifest{
		ProfileID:              "gemma4-vllm",
		ServedModel:            "google/gemma-4-31B-it",
		ModelRepository:        "RedHatAI/gemma-4-31B-it-FP8-block",
		ModelRevision:          "b92691b6de6294798f45df81accf88cbc3e1d901",
		TokenizerRepository:    "RedHatAI/gemma-4-31B-it-FP8-block",
		TokenizerRevision:      "b92691b6de6294798f45df81accf88cbc3e1d901",
		TokenizerSHA256:        "cc8d3a0ce36466ccc1278bf987df5f71db1719b9ca6b4118264f45cb627bfe0f",
		TokenizerConfigSHA256:  "e467669cfe172dfb0c4e7de7bfbe7553c42bfa5de95acd71f423f58a434d80de",
		SpecialTokensSHA256:    "special-tokens-sha",
		TemplateSHA256:         "afdbb2abe3667ccde95cc2f86919f05370339399bab5f750950a4390523b8927",
		TemplateSource:         "vllm@6586e54ee274d75b71bd0b77600a6cc71f57c4bc:examples/tool_chat_template_gemma4.jinja",
		TemplateRuntime:        "transformers-jinja-vllm-profile",
		TemplateRuntimeVersion: "oracle-pending",
		SpecialTokens: SpecialTokenBindings{
			BOS: TokenBinding{Value: "<bos>", ID: 2},
			EOS: TokenBinding{Value: "<eos>", ID: 1},
			UNK: TokenBinding{Value: "<unk>", ID: 3},
			PAD: TokenBinding{Value: "<pad>", ID: 0},
		},
		BackendKind:           "vllm",
		BackendVersion:        "0.24.0-cu129-ubuntu2404-phala.6",
		BackendSourceRevision: "6586e54ee274d75b71bd0b77600a6cc71f57c4bc",
		BackendImageDigest:    "sha256:66fa87a8eb31b1c9849c907c63a18a6d03c1696a50246ca094c5789b0efd7368",
		BlockSize:             64,
		MultimodalProfile:     "text-only",
		PredictorVersion:      "v0.9.1-test",
	}

	tests := []struct {
		name         string
		capabilities TokenizerCapabilities
		policies     SpecialTokenPolicies
		wantValid    bool
	}{
		{
			name:         "completion only",
			capabilities: TokenizerCapabilities{Completions: true},
			policies:     SpecialTokenPolicies{Completions: SpecialTokenPolicyAdd},
			wantValid:    true,
		},
		{
			name:         "chat only",
			capabilities: TokenizerCapabilities{ChatCompletions: true},
			policies:     SpecialTokenPolicies{ChatCompletions: SpecialTokenPolicyOmit},
			wantValid:    true,
		},
		{
			name:         "both request classes",
			capabilities: TokenizerCapabilities{Completions: true, ChatCompletions: true},
			policies: SpecialTokenPolicies{
				Completions:     SpecialTokenPolicyAdd,
				ChatCompletions: SpecialTokenPolicyOmit,
			},
			wantValid: true,
		},
		{
			name:         "enabled completion missing policy",
			capabilities: TokenizerCapabilities{Completions: true},
		},
		{
			name:         "disabled chat retains policy",
			capabilities: TokenizerCapabilities{Completions: true},
			policies: SpecialTokenPolicies{
				Completions:     SpecialTokenPolicyAdd,
				ChatCompletions: SpecialTokenPolicyOmit,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manifest := base
			manifest.Capabilities = tt.capabilities
			manifest.SpecialTokenPolicies = tt.policies
			err := manifest.Validate()
			if tt.wantValid && err != nil {
				t.Fatalf("valid manifest rejected: %v", err)
			}
			if !tt.wantValid && err == nil {
				t.Fatal("invalid request-class policy manifest accepted")
			}
		})
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
