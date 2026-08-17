package kvadmission

import predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"

type Cost struct {
	Supported                   bool
	UnsupportedReason           string
	BodyBytes                   int
	TextBytes                   int
	ToolSchemaBytes             int
	MessageCount                int
	ToolCount                   int
	ModalityCount               int
	MaxOutputTokens             int
	HasMaxOutputTokens          bool
	EstimatedInputLow           int64
	EstimatedInputHigh          int64
	ApproximateInputTokens      int64
	ApproximateInputTokensKnown bool
	ExplicitPromptTokens        int64
	BoundedDecodeTokens         int64
	DecodeSequences             int64
	Estimate                    predictive.RequestEstimate
}

type RequestShape struct {
	PromptBatchSize                int64
	PromptStringBytes              int64
	MaximumPromptStringBytes       int64
	PromptApproximateTokens        int64
	MaximumPromptApproximateTokens int64
	ExplicitPromptTokens           int64
	MaximumExplicitPromptTokens    int64
	DecodeSequences                int64
}

func DefaultRequestShape() RequestShape {
	return RequestShape{PromptBatchSize: 1, DecodeSequences: 1}
}

// PredictiveEstimate returns the one complete estimator output accepted by the
// new admission core. Legacy interval fields remain only until the atomic HTTP
// cutover; callers must not rebuild this record from parallel scalar values.
func (c Cost) PredictiveEstimate() (predictive.RequestEstimate, bool) {
	if !c.Supported || c.Estimate.Validate() != nil {
		return predictive.RequestEstimate{}, false
	}
	return c.Estimate, true
}

// ApproximateInputTokenHint returns the model-neutral lexical-size estimate.
// It is not an exact tokenizer result. EstimateJSON combines it with the fixed
// reservation margin when constructing the one admission RequestEstimate.
func (c Cost) ApproximateInputTokenHint() (int64, bool) {
	return c.ApproximateInputTokens, c.ApproximateInputTokensKnown && c.ApproximateInputTokens > 0
}

// ApproximatePrefillTokenHint returns the model-neutral work estimate used to
// predict Prefill interference. Text-only requests keep the bounded lexical
// estimate. For a recognized multimodal request, the lexical URL or marker is
// not representative of backend media expansion, so use the conservative input
// upper bound. The Controller later derives block-rounded KV work from the
// separately margined KV-reservation estimate.
func (c Cost) ApproximatePrefillTokenHint() (int64, bool) {
	if c.ModalityCount > 0 && c.EstimatedInputHigh > 0 {
		return c.EstimatedInputHigh, true
	}
	return c.ApproximateInputTokenHint()
}

type EstimatorConfig struct {
	MinBytesPerToken             int
	MaxBytesPerToken             int
	ToolMinBytesPerToken         int
	ToolMaxBytesPerToken         int
	TemplateTokensPerMessageLow  int
	TemplateTokensPerMessageHigh int
	ModalityTokensLow            int
	ModalityTokensHigh           int
	BlindOutputTokens            int
}

func DefaultEstimatorConfig() EstimatorConfig {
	return EstimatorConfig{
		MinBytesPerToken:             2,
		MaxBytesPerToken:             6,
		ToolMinBytesPerToken:         2,
		ToolMaxBytesPerToken:         6,
		TemplateTokensPerMessageLow:  3,
		TemplateTokensPerMessageHigh: 8,
		ModalityTokensLow:            256,
		ModalityTokensHigh:           4096,
		BlindOutputTokens:            256,
	}
}
