package kvadmission

import (
	"math"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type SemanticInputFeatures struct {
	PromptBytes            int64
	TextBytes              int64
	ToolSchemaBytes        int64
	MessageCount           int64
	ToolCount              int64
	ModalityCount          int64
	ApproximateInputTokens int64
	ExplicitPromptTokens   int64
	Conservative           bool
}

type SemanticRequestShape struct {
	BodyBytes       int
	BasePromptCount int64
	DecodeSequences int64
	Aggregate       SemanticInputFeatures
	MaximumSequence SemanticInputFeatures
}

func EstimateSemanticRequest(
	shape SemanticRequestShape,
	maxOutputTokens int,
	hasMaxOutputTokens bool,
	cfg EstimatorConfig,
) Cost {
	cost := Cost{
		BodyBytes:            shape.BodyBytes,
		MaxOutputTokens:      maxOutputTokens,
		HasMaxOutputTokens:   hasMaxOutputTokens,
		BasePromptCount:      shape.BasePromptCount,
		DecodeSequences:      shape.DecodeSequences,
		ExplicitPromptTokens: shape.Aggregate.ExplicitPromptTokens,
	}
	if err := validateEstimatorConfig(cfg); err != nil {
		cost.UnsupportedReason = "invalid_estimator_config"
		return cost
	}
	if !semanticRequestShapeValid(shape) {
		cost.UnsupportedReason = "invalid_request_shape"
		return cost
	}
	if shape.BodyBytes <= 0 {
		cost.UnsupportedReason = "empty_body"
		return cost
	}
	if shape.Aggregate.TextBytes > int64(math.MaxInt) ||
		shape.Aggregate.ToolSchemaBytes > int64(math.MaxInt) ||
		shape.Aggregate.MessageCount > int64(math.MaxInt) ||
		shape.Aggregate.ToolCount > int64(math.MaxInt) ||
		shape.Aggregate.ModalityCount > int64(math.MaxInt) {
		cost.UnsupportedReason = "request_estimate_overflow"
		return cost
	}
	cost.TextBytes = int(shape.Aggregate.TextBytes)
	cost.ToolSchemaBytes = int(shape.Aggregate.ToolSchemaBytes)
	cost.MessageCount = int(shape.Aggregate.MessageCount)
	cost.ToolCount = int(shape.Aggregate.ToolCount)
	cost.ModalityCount = int(shape.Aggregate.ModalityCount)

	aggregate, ok := estimateSemanticInput(shape.Aggregate, cfg)
	if !ok {
		cost.UnsupportedReason = "request_estimate_overflow"
		return cost
	}
	maximum, ok := estimateSemanticInput(shape.MaximumSequence, cfg)
	if !ok || maximum.selection > aggregate.selection || maximum.high > aggregate.high {
		cost.UnsupportedReason = "request_estimate_overflow"
		return cost
	}
	cost.EstimatedInputLow = aggregate.low
	cost.EstimatedInputHigh = aggregate.high
	cost.ApproximateInputTokens = aggregate.selection
	cost.ApproximateInputTokensKnown = true

	decode := cfg.BlindOutputTokens
	if hasMaxOutputTokens && maxOutputTokens >= 0 && maxOutputTokens < decode {
		decode = maxOutputTokens
	}
	cost.BoundedDecodeTokens = int64(decode)
	conservative := shape.Aggregate.Conservative || shape.Aggregate.ToolSchemaBytes > 0 ||
		shape.Aggregate.ModalityCount > 0
	if !setSemanticPredictiveEstimate(&cost, maximum.selection, maximum.high, conservative) {
		cost.UnsupportedReason = "request_estimate_overflow"
		return cost
	}
	cost.Supported = true
	return cost
}

type semanticInputEstimate struct {
	low       int64
	high      int64
	selection int64
}

func estimateSemanticInput(features SemanticInputFeatures, cfg EstimatorConfig) (semanticInputEstimate, bool) {
	textLow, ok := semanticCeilDiv(features.TextBytes, int64(cfg.MaxBytesPerToken))
	if !ok {
		return semanticInputEstimate{}, false
	}
	textHigh, ok := semanticCeilDiv(features.TextBytes, int64(cfg.MinBytesPerToken))
	if !ok {
		return semanticInputEstimate{}, false
	}
	toolLow, ok := semanticCeilDiv(features.ToolSchemaBytes, int64(cfg.ToolMaxBytesPerToken))
	if !ok {
		return semanticInputEstimate{}, false
	}
	toolHigh, ok := semanticCeilDiv(features.ToolSchemaBytes, int64(cfg.ToolMinBytesPerToken))
	if !ok {
		return semanticInputEstimate{}, false
	}
	templateLow, ok := semanticMultiply(features.MessageCount, int64(cfg.TemplateTokensPerMessageLow))
	if !ok {
		return semanticInputEstimate{}, false
	}
	templateHigh, ok := semanticMultiply(features.MessageCount, int64(cfg.TemplateTokensPerMessageHigh))
	if !ok {
		return semanticInputEstimate{}, false
	}
	modalityLow, ok := semanticMultiply(features.ModalityCount, int64(cfg.ModalityTokensLow))
	if !ok {
		return semanticInputEstimate{}, false
	}
	modalityHigh, ok := semanticMultiply(features.ModalityCount, int64(cfg.ModalityTokensHigh))
	if !ok {
		return semanticInputEstimate{}, false
	}
	low, ok := semanticSum(textLow, toolLow, templateLow, modalityLow, features.ExplicitPromptTokens)
	if !ok {
		return semanticInputEstimate{}, false
	}
	high, ok := semanticSum(textHigh, toolHigh, templateHigh, modalityHigh, features.ExplicitPromptTokens)
	if !ok {
		return semanticInputEstimate{}, false
	}
	semanticBytes, ok := semanticSum(features.PromptBytes, features.ToolSchemaBytes)
	if !ok {
		return semanticInputEstimate{}, false
	}
	promptHigh, ok := semanticCeilDiv(semanticBytes, int64(cfg.MinBytesPerToken))
	if !ok {
		return semanticInputEstimate{}, false
	}
	promptHigh, ok = semanticSum(promptHigh, templateHigh, modalityHigh, features.ExplicitPromptTokens)
	if !ok {
		return semanticInputEstimate{}, false
	}
	if promptHigh > high {
		high = promptHigh
	}
	structureBytes := features.PromptBytes - features.TextBytes
	if structureBytes < 0 {
		structureBytes = 0
	}
	structureTokens, ok := semanticCeilDiv(structureBytes, int64(approximateASCIIBytesPerToken))
	if !ok {
		return semanticInputEstimate{}, false
	}
	toolTokens, ok := semanticCeilDiv(features.ToolSchemaBytes, int64(approximateASCIIBytesPerToken))
	if !ok {
		return semanticInputEstimate{}, false
	}
	templateHint, ok := semanticMultiply(features.MessageCount, 4)
	if !ok {
		return semanticInputEstimate{}, false
	}
	selection, ok := semanticSum(
		features.ApproximateInputTokens,
		features.ExplicitPromptTokens,
		structureTokens,
		toolTokens,
		templateHint,
	)
	if !ok {
		return semanticInputEstimate{}, false
	}
	if selection < 1 {
		selection = 1
	}
	if low < 1 {
		low = 1
	}
	if high < selection {
		high = selection
	}
	if high < low {
		high = low
	}
	return semanticInputEstimate{low: low, high: high, selection: selection}, true
}

func setSemanticPredictiveEstimate(
	cost *Cost,
	maximumSelection,
	maximumHigh int64,
	conservative bool,
) bool {
	if cost == nil || cost.ApproximateInputTokens <= 0 || maximumSelection <= 0 ||
		maximumSelection > cost.ApproximateInputTokens || cost.EstimatedInputHigh <= 0 ||
		maximumHigh <= 0 || maximumHigh > cost.EstimatedInputHigh || cost.BoundedDecodeTokens < 0 {
		return false
	}
	selection := cost.ApproximateInputTokens
	reservation := int64(0)
	maximumReservation := int64(0)
	inputConfidence := InputEstimateLexical
	if cost.ModalityCount > 0 {
		selection = cost.EstimatedInputHigh
		maximumSelection = maximumHigh
		reservation = cost.EstimatedInputHigh
		maximumReservation = maximumHigh
		inputConfidence = InputEstimateConservative
	} else {
		marginNumerator := fixedKVReservationMarginNumerator
		marginDenominator := fixedKVReservationMarginDenominator
		if conservative {
			marginNumerator = conservativeMarginNumerator
			marginDenominator = conservativeMarginDenominator
			inputConfidence = InputEstimateConservative
		}
		var ok bool
		reservation, ok = fixedMarginTokensForSequences(
			selection,
			cost.BasePromptCount,
			marginNumerator,
			marginDenominator,
		)
		if !ok {
			return false
		}
		maximumReservation, ok = fixedMarginTokens(
			maximumSelection,
			marginNumerator,
			marginDenominator,
		)
		if !ok {
			return false
		}
		if conservative {
			if cost.EstimatedInputHigh > reservation {
				reservation = cost.EstimatedInputHigh
			}
			if maximumHigh > maximumReservation {
				maximumReservation = maximumHigh
			}
		}
	}
	var ok bool
	reservation, ok = boundAggregateReservationByMaximumSequence(
		reservation,
		maximumReservation,
		cost.BasePromptCount,
	)
	if !ok || maximumReservation > reservation {
		return false
	}
	outputLimit, outputLimitKnown := predictiveOutputLimit(*cost)
	cost.Estimate = predictive.RequestEstimate{
		SelectionInputTokens:                    selection,
		MaximumSequenceInputTokens:              maximumSelection,
		KVReservationInputTokens:                reservation,
		MaximumSequenceKVReservationInputTokens: maximumReservation,
		DecodeHorizonTokens:                     cost.BoundedDecodeTokens,
		OutputLimitTokens:                       outputLimit,
		OutputLimitKnown:                        outputLimitKnown,
		BasePromptCount:                         cost.BasePromptCount,
		DecodeSequences:                         cost.DecodeSequences,
		InputEstimateConfidence:                 inputConfidence,
	}
	return cost.Estimate.Validate() == nil
}

func semanticRequestShapeValid(shape SemanticRequestShape) bool {
	if shape.BodyBytes < 0 || shape.BasePromptCount <= 0 || shape.DecodeSequences < shape.BasePromptCount ||
		shape.DecodeSequences%shape.BasePromptCount != 0 ||
		!semanticInputFeaturesValid(shape.Aggregate) ||
		!semanticInputFeaturesValid(shape.MaximumSequence) {
		return false
	}
	return semanticInputFeaturesWithin(shape.MaximumSequence, shape.Aggregate)
}

func semanticInputFeaturesValid(features SemanticInputFeatures) bool {
	return features.PromptBytes >= 0 && features.TextBytes >= 0 && features.ToolSchemaBytes >= 0 &&
		features.MessageCount >= 0 && features.ToolCount >= 0 && features.ModalityCount >= 0 &&
		features.ApproximateInputTokens >= 0 && features.ExplicitPromptTokens >= 0
}

func semanticInputFeaturesWithin(maximum, aggregate SemanticInputFeatures) bool {
	return maximum.PromptBytes <= aggregate.PromptBytes && maximum.TextBytes <= aggregate.TextBytes &&
		maximum.ToolSchemaBytes <= aggregate.ToolSchemaBytes && maximum.MessageCount <= aggregate.MessageCount &&
		maximum.ToolCount <= aggregate.ToolCount && maximum.ModalityCount <= aggregate.ModalityCount &&
		maximum.ApproximateInputTokens <= aggregate.ApproximateInputTokens &&
		maximum.ExplicitPromptTokens <= aggregate.ExplicitPromptTokens
}

func semanticCeilDiv(value, divisor int64) (int64, bool) {
	if value < 0 || divisor <= 0 || value > math.MaxInt64-(divisor-1) {
		return 0, false
	}
	return (value + divisor - 1) / divisor, true
}

func semanticMultiply(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || (right > 0 && left > math.MaxInt64/right) {
		return 0, false
	}
	return left * right, true
}

func semanticSum(values ...int64) (int64, bool) {
	result := int64(0)
	for _, value := range values {
		if value < 0 || result > math.MaxInt64-value {
			return 0, false
		}
		result += value
	}
	return result, true
}
