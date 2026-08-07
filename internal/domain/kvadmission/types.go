package kvadmission

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
	BoundedDecodeTokens         int64
}

// ApproximateInputTokenHint returns a model-neutral lexical-size hint. The
// value is optional evidence for a later combined forecast; it is neither an
// exact tokenizer result nor an admission decision.
func (c Cost) ApproximateInputTokenHint() (int64, bool) {
	return c.ApproximateInputTokens, c.ApproximateInputTokensKnown && c.ApproximateInputTokens > 0
}

// ApproximatePrefillTokenHint returns the model-neutral work estimate used to
// predict Prefill interference. Text-only requests keep the bounded lexical
// hint. For a recognized multimodal request, the lexical URL or marker is not
// representative of backend media expansion, so use the existing conservative
// input upper bound. Hard-KV accounting remains independent of this hint.
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
