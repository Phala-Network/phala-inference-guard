package predictive

import (
	"fmt"
	"time"
)

type SpecialTokenPolicy string

const (
	SpecialTokenPolicyAdd  SpecialTokenPolicy = "add"
	SpecialTokenPolicyOmit SpecialTokenPolicy = "omit"
)

func (p SpecialTokenPolicy) Validate() error {
	switch p {
	case SpecialTokenPolicyAdd, SpecialTokenPolicyOmit:
		return nil
	default:
		return fmt.Errorf("tokenizer manifest special-token policy %q is invalid", p)
	}
}

func (p SpecialTokenPolicy) AddSpecialTokens() bool {
	return p == SpecialTokenPolicyAdd
}

type TokenBinding struct {
	Value string
	ID    int64
}

func (b TokenBinding) validate(role string) error {
	if b.Value == "" {
		return fmt.Errorf("tokenizer manifest %s token value is required", role)
	}
	if b.ID < 0 || b.ID > int64(^uint32(0)) {
		return fmt.Errorf("tokenizer manifest %s token id must be an unsigned 32-bit value", role)
	}
	return nil
}

type SpecialTokenBindings struct {
	BOS TokenBinding
	EOS TokenBinding
	UNK TokenBinding
	PAD TokenBinding
}

func (b SpecialTokenBindings) Validate() error {
	for _, binding := range []struct {
		role  string
		value TokenBinding
	}{
		{role: "bos", value: b.BOS},
		{role: "eos", value: b.EOS},
		{role: "unk", value: b.UNK},
		{role: "pad", value: b.PAD},
	} {
		if err := binding.value.validate(binding.role); err != nil {
			return err
		}
	}
	return nil
}

type TokenizerCapabilities struct {
	Completions     bool
	ChatCompletions bool
	Tools           bool
	ToolChoice      bool
	ResponseFormat  bool
	JSONSchema      bool
	Reasoning       bool
	Multimodal      bool
}

func (c TokenizerCapabilities) Validate(multimodalProfile string) error {
	if !c.Completions && !c.ChatCompletions {
		return fmt.Errorf("tokenizer manifest must enable at least one request class")
	}
	if !c.ChatCompletions && (c.Tools || c.ToolChoice || c.ResponseFormat || c.JSONSchema || c.Reasoning || c.Multimodal) {
		return fmt.Errorf("tokenizer manifest chat-only capabilities require chat completions")
	}
	if c.ToolChoice && !c.Tools {
		return fmt.Errorf("tokenizer manifest tool-choice capability requires tools")
	}
	if c.JSONSchema && !c.ResponseFormat {
		return fmt.Errorf("tokenizer manifest json-schema capability requires response format")
	}
	if c.Multimodal && multimodalProfile == "text-only" {
		return fmt.Errorf("tokenizer manifest multimodal capability requires a verified multimodal profile")
	}
	return nil
}

type TokenizerManifest struct {
	ProfileID              string
	ServedModel            string
	ModelRepository        string
	ModelRevision          string
	TokenizerRepository    string
	TokenizerRevision      string
	TokenizerSHA256        string
	TokenizerConfigSHA256  string
	SpecialTokensSHA256    string
	TemplateSHA256         string
	TemplateRuntime        string
	TemplateRuntimeVersion string
	SpecialTokenPolicy     SpecialTokenPolicy
	SpecialTokens          SpecialTokenBindings
	Capabilities           TokenizerCapabilities
	BackendKind            string
	BackendVersion         string
	BlockSize              int64
	MultimodalProfile      string
	PredictorVersion       string
}

func (m TokenizerManifest) Validate() error {
	required := []struct {
		name  string
		value string
	}{
		{name: "profile id", value: m.ProfileID},
		{name: "served model", value: m.ServedModel},
		{name: "model repository", value: m.ModelRepository},
		{name: "model revision", value: m.ModelRevision},
		{name: "tokenizer repository", value: m.TokenizerRepository},
		{name: "tokenizer revision", value: m.TokenizerRevision},
		{name: "tokenizer sha256", value: m.TokenizerSHA256},
		{name: "tokenizer config sha256", value: m.TokenizerConfigSHA256},
		{name: "special tokens sha256", value: m.SpecialTokensSHA256},
		{name: "template sha256", value: m.TemplateSHA256},
		{name: "template runtime", value: m.TemplateRuntime},
		{name: "template runtime version", value: m.TemplateRuntimeVersion},
		{name: "backend kind", value: m.BackendKind},
		{name: "backend version", value: m.BackendVersion},
		{name: "multimodal profile", value: m.MultimodalProfile},
		{name: "predictor version", value: m.PredictorVersion},
	}
	for _, field := range required {
		if field.value == "" {
			return fmt.Errorf("tokenizer manifest %s is required", field.name)
		}
	}
	if m.BlockSize <= 0 {
		return fmt.Errorf("tokenizer manifest block size must be positive")
	}
	if err := m.SpecialTokenPolicy.Validate(); err != nil {
		return err
	}
	if err := m.SpecialTokens.Validate(); err != nil {
		return err
	}
	if err := m.Capabilities.Validate(m.MultimodalProfile); err != nil {
		return err
	}
	return nil
}

func (m TokenizerManifest) Compatible(other TokenizerManifest) bool {
	return m.Validate() == nil && other.Validate() == nil && m == other
}

type CacheHitInterval struct {
	Certain  int64
	Lower    int64
	Expected int64
	Upper    int64
}

func (i CacheHitInterval) Validate(inputTokens int64) error {
	if inputTokens < 0 {
		return fmt.Errorf("input tokens must be non-negative")
	}
	if i.Certain < 0 || i.Certain > i.Lower || i.Lower > i.Expected || i.Expected > i.Upper || i.Upper > inputTokens {
		return fmt.Errorf("invalid cache hit interval")
	}
	return nil
}

type VLLMProjectionInput struct {
	InputTokens        int64
	CacheHits          CacheHitInterval
	DecodeHorizonUpper int64
	BlockSize          int64
}

type KVIncrement struct {
	PhysicalKVUpper     int64
	ActiveKVUpper       int64
	CacheDiscountTokens int64
}

func ProjectVLLM(input VLLMProjectionInput) (KVIncrement, error) {
	if input.BlockSize <= 0 {
		return KVIncrement{}, fmt.Errorf("block size must be positive")
	}
	if input.DecodeHorizonUpper < 0 {
		return KVIncrement{}, fmt.Errorf("decode horizon must be non-negative")
	}
	if err := input.CacheHits.Validate(input.InputTokens); err != nil {
		return KVIncrement{}, err
	}
	tokens := input.InputTokens - input.CacheHits.Certain + input.DecodeHorizonUpper
	rounded := roundUp(tokens, input.BlockSize)
	return KVIncrement{
		PhysicalKVUpper:     rounded,
		ActiveKVUpper:       rounded,
		CacheDiscountTokens: input.CacheHits.Certain,
	}, nil
}

func roundUp(value, unit int64) int64 {
	if value <= 0 {
		return 0
	}
	return ((value + unit - 1) / unit) * unit
}

type Projection struct {
	PhysicalKVUpper int64
	ActiveKVUpper   int64
}

type VirtualState struct {
	PhysicalKVUpper int64
	ActiveKVUpper   int64
}

type VirtualStateInterval struct {
	Lower VirtualState
	Upper VirtualState
}

type RequestCost struct {
	ManifestID           string
	InputTokens          int64
	KV                   KVIncrement
	UncachedPrefillUpper int64
	Confidence           float64
}

type SchedulerEstimate struct {
	ExistingUserTPSLower float64
	AllUserTPSLower      float64
	TTFTUpper            time.Duration
	TPOTUpper            time.Duration
	WorkspaceRiskUpper   float64
	PreemptionRiskUpper  float64
}

type Constraints struct {
	PhysicalKVHard       int64
	ActiveKVHard         int64
	UserTPSTarget        float64
	TTFTSLO              time.Duration
	TPOTSLO              time.Duration
	WorkspaceRiskBudget  float64
	PreemptionRiskBudget float64
	MinimumConfidence    float64
}

type Reason string

const (
	ReasonFit                     Reason = "fit"
	ReasonKVOverBudget            Reason = "kv_over_budget"
	ReasonActiveKVOverBudget      Reason = "active_kv_over_budget"
	ReasonExistingTPSAtRisk       Reason = "existing_tps_at_risk"
	ReasonNewTPSAtRisk            Reason = "new_tps_at_risk"
	ReasonTTFTAtRisk              Reason = "ttft_at_risk"
	ReasonTPOTAtRisk              Reason = "tpot_at_risk"
	ReasonWorkspaceAtRisk         Reason = "workspace_at_risk"
	ReasonPreemptionAtRisk        Reason = "preemption_at_risk"
	ReasonTokenizerProfileUnknown Reason = "tokenizer_profile_unknown"
	ReasonPredictorProfileUnknown Reason = "predictor_profile_unknown"
	ReasonDuplicateRequest        Reason = "duplicate_request"
)

type EvaluationInput struct {
	Projection  Projection
	Scheduler   SchedulerEstimate
	Constraints Constraints
	Confidence  float64
}

type Decision struct {
	Reason     Reason
	Projection Projection
	Scheduler  SchedulerEstimate
	Confidence float64
}
