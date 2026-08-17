package kvadmission

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

const (
	fixedKVReservationMarginNumerator   int64 = 9
	fixedKVReservationMarginDenominator int64 = 8
	conservativeMarginNumerator         int64 = 3
	conservativeMarginDenominator       int64 = 2
)

func EstimateJSON(body []byte, maxOutputTokens int, hasMaxOutputTokens bool, cfg EstimatorConfig) Cost {
	return estimateJSON(body, maxOutputTokens, hasMaxOutputTokens, DefaultRequestShape(), cfg, true)
}

// EstimateValidatedJSON avoids repeating strict JSON validation when the
// caller has already validated the complete body without modifying it.
func EstimateValidatedJSON(body []byte, maxOutputTokens int, hasMaxOutputTokens bool, cfg EstimatorConfig) Cost {
	return estimateJSON(body, maxOutputTokens, hasMaxOutputTokens, DefaultRequestShape(), cfg, false)
}

func EstimateValidatedJSONWithShape(
	body []byte,
	maxOutputTokens int,
	hasMaxOutputTokens bool,
	shape RequestShape,
	cfg EstimatorConfig,
) Cost {
	return estimateJSON(body, maxOutputTokens, hasMaxOutputTokens, shape, cfg, false)
}

func estimateJSON(
	body []byte,
	maxOutputTokens int,
	hasMaxOutputTokens bool,
	shape RequestShape,
	cfg EstimatorConfig,
	validate bool,
) Cost {
	cost := Cost{
		BodyBytes:            len(body),
		MaxOutputTokens:      maxOutputTokens,
		HasMaxOutputTokens:   hasMaxOutputTokens,
		BasePromptCount:      shape.PromptBatchSize,
		DecodeSequences:      shape.DecodeSequences,
		ExplicitPromptTokens: shape.ExplicitPromptTokens,
	}
	if err := validateEstimatorConfig(cfg); err != nil {
		cost.UnsupportedReason = "invalid_estimator_config"
		return cost
	}
	if shape.PromptBatchSize <= 0 || shape.DecodeSequences <= 0 || shape.ExplicitPromptTokens < 0 ||
		shape.MaximumExplicitPromptTokens < 0 ||
		shape.MaximumExplicitPromptTokens > shape.ExplicitPromptTokens ||
		shape.PromptApproximateTokens < 0 || shape.MaximumPromptApproximateTokens < 0 ||
		shape.MaximumPromptApproximateTokens > shape.PromptApproximateTokens ||
		shape.PromptStringBytes < 0 || shape.MaximumPromptStringBytes < 0 ||
		shape.MaximumPromptStringBytes > shape.PromptStringBytes {
		cost.UnsupportedReason = "invalid_request_shape"
		return cost
	}
	if len(body) == 0 {
		cost.UnsupportedReason = "empty_body"
		return cost
	}
	if validate && !json.Valid(body) {
		cost.UnsupportedReason = "invalid_json"
		return cost
	}

	features, valid := scanJSONFeatures(body)
	if !valid {
		return estimateGenericJSONValue(cost, body, cfg)
	}
	cost.TextBytes = features.StringValueBytes
	cost.ToolSchemaBytes = features.ToolSchemaBytes
	cost.MessageCount = features.MessageCount
	cost.ToolCount = features.ToolCount
	cost.ModalityCount = features.ModalityCount
	cost.ApproximateInputTokens = features.ApproximateInputTokens
	cost.ApproximateInputTokensKnown = features.ApproximateInputTokensKnown && features.ApproximateInputTokens > 0
	if shape.ExplicitPromptTokens > 0 {
		if !addApproximateInputTokens(&cost.ApproximateInputTokens, shape.ExplicitPromptTokens) {
			cost.UnsupportedReason = "request_estimate_overflow"
			return cost
		}
		cost.ApproximateInputTokensKnown = true
	}

	textLow := ceilDiv(cost.TextBytes, cfg.MaxBytesPerToken)
	textHigh := ceilDiv(cost.TextBytes, cfg.MinBytesPerToken)
	toolLow := ceilDiv(cost.ToolSchemaBytes, cfg.ToolMaxBytesPerToken)
	toolHigh := ceilDiv(cost.ToolSchemaBytes, cfg.ToolMinBytesPerToken)
	templateLow := cost.MessageCount * cfg.TemplateTokensPerMessageLow
	templateHigh := cost.MessageCount * cfg.TemplateTokensPerMessageHigh
	modalityLow := cost.ModalityCount * cfg.ModalityTokensLow
	modalityHigh := cost.ModalityCount * cfg.ModalityTokensHigh

	low := int64(textLow + toolLow + templateLow + modalityLow)
	high := int64(textHigh + toolHigh + templateHigh + modalityHigh)
	if low > math.MaxInt64-shape.ExplicitPromptTokens || high > math.MaxInt64-shape.ExplicitPromptTokens {
		cost.UnsupportedReason = "request_estimate_overflow"
		return cost
	}
	low += shape.ExplicitPromptTokens
	high += shape.ExplicitPromptTokens
	wholeBodyHigh := int64(ceilDiv(len(body), cfg.MinBytesPerToken) + templateHigh + modalityHigh)
	if high < wholeBodyHigh {
		high = wholeBodyHigh
	}
	if low < 1 {
		low = 1
	}
	if high < low {
		high = low
	}

	decode := cfg.BlindOutputTokens
	if hasMaxOutputTokens && maxOutputTokens >= 0 && maxOutputTokens < decode {
		decode = maxOutputTokens
	}
	cost.EstimatedInputLow = low
	cost.EstimatedInputHigh = high
	cost.BoundedDecodeTokens = int64(decode)
	maximumSequenceInput, valid := maximumSequenceInputTokens(cost, shape)
	maximumSequenceHigh, highValid := maximumSequenceEstimatedInputHigh(
		cost,
		shape,
		features,
		cfg,
		len(body),
	)
	conservative := features.ConservativeInputEstimate || cost.ToolSchemaBytes > 0 || cost.ModalityCount > 0
	if !valid || !highValid || !setTextPredictiveEstimate(
		&cost,
		maximumSequenceInput,
		maximumSequenceHigh,
		conservative,
	) {
		cost.UnsupportedReason = "request_estimate_overflow"
		return cost
	}
	cost.Supported = true
	return cost
}

func estimateGenericJSONValue(cost Cost, body []byte, cfg EstimatorConfig) Cost {
	valueBytes := len(bytes.TrimSpace(body))
	low := int64(ceilDiv(valueBytes, cfg.MaxBytesPerToken))
	high := int64(ceilDiv(valueBytes, cfg.MinBytesPerToken))
	if low > math.MaxInt64-cost.ExplicitPromptTokens || high > math.MaxInt64-cost.ExplicitPromptTokens {
		cost.UnsupportedReason = "request_estimate_overflow"
		return cost
	}
	low += cost.ExplicitPromptTokens
	high += cost.ExplicitPromptTokens
	if low < 1 {
		low = 1
	}
	if high < low {
		high = low
	}
	cost.TextBytes = valueBytes
	cost.EstimatedInputLow = low
	cost.EstimatedInputHigh = high
	cost.ApproximateInputTokens = high
	cost.ApproximateInputTokensKnown = true
	cost.BoundedDecodeTokens = int64(cfg.BlindOutputTokens)
	cost.Estimate = predictive.RequestEstimate{
		SelectionInputTokens:                    high,
		MaximumSequenceInputTokens:              high,
		KVReservationInputTokens:                high,
		MaximumSequenceKVReservationInputTokens: high,
		DecodeHorizonTokens:                     cost.BoundedDecodeTokens,
		BasePromptCount:                         cost.BasePromptCount,
		DecodeSequences:                         cost.DecodeSequences,
	}
	cost.InputEstimateConfidence = InputEstimateConservative
	if cost.Estimate.Validate() != nil {
		cost.UnsupportedReason = "request_estimate_overflow"
		return cost
	}
	cost.Supported = true
	return cost
}

func setTextPredictiveEstimate(
	cost *Cost,
	maximumSequenceInput,
	maximumSequenceHigh int64,
	conservative bool,
) bool {
	if cost == nil || cost.EstimatedInputHigh <= 0 || maximumSequenceInput <= 0 ||
		maximumSequenceHigh <= 0 || maximumSequenceHigh > cost.EstimatedInputHigh ||
		cost.BoundedDecodeTokens < 0 {
		return false
	}
	selection, known := cost.ApproximateInputTokenHint()
	maximumSelection := maximumSequenceInput
	reservation := int64(0)
	maximumReservation := int64(0)
	if cost.ModalityCount > 0 || !known || selection <= 0 {
		selection = cost.EstimatedInputHigh
		maximumSelection = maximumSequenceHigh
		reservation = cost.EstimatedInputHigh
		maximumReservation = maximumSequenceHigh
		cost.InputEstimateConfidence = InputEstimateConservative
	} else {
		marginNumerator := fixedKVReservationMarginNumerator
		marginDenominator := fixedKVReservationMarginDenominator
		cost.InputEstimateConfidence = InputEstimateLexical
		if conservative {
			marginNumerator = conservativeMarginNumerator
			marginDenominator = conservativeMarginDenominator
			cost.InputEstimateConfidence = InputEstimateConservative
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
			maximumSequenceInput,
			marginNumerator,
			marginDenominator,
		)
		if conservative {
			if cost.EstimatedInputHigh > reservation {
				reservation = cost.EstimatedInputHigh
			}
			if maximumSequenceHigh > maximumReservation {
				maximumReservation = maximumSequenceHigh
			}
		}
		if !ok || maximumReservation > reservation {
			return false
		}
	}
	cost.Estimate = predictive.RequestEstimate{
		SelectionInputTokens:                    selection,
		MaximumSequenceInputTokens:              maximumSelection,
		KVReservationInputTokens:                reservation,
		MaximumSequenceKVReservationInputTokens: maximumReservation,
		DecodeHorizonTokens:                     cost.BoundedDecodeTokens,
		BasePromptCount:                         cost.BasePromptCount,
		DecodeSequences:                         cost.DecodeSequences,
	}
	return cost.Estimate.Validate() == nil
}

func maximumSequenceInputTokens(
	cost Cost,
	shape RequestShape,
) (int64, bool) {
	selection, known := cost.ApproximateInputTokenHint()
	if !known || selection <= 0 {
		selection = cost.EstimatedInputHigh
	}
	if selection <= 0 {
		return 0, false
	}
	if shape.PromptBatchSize <= 1 {
		return selection, true
	}
	promptAggregate, ok := addRequestShapeTokens(
		shape.PromptApproximateTokens,
		shape.ExplicitPromptTokens,
	)
	if !ok || promptAggregate > selection {
		return 0, false
	}
	promptMaximum, ok := addRequestShapeTokens(
		shape.MaximumPromptApproximateTokens,
		shape.MaximumExplicitPromptTokens,
	)
	if !ok || promptMaximum > promptAggregate {
		return 0, false
	}
	maximum := selection - promptAggregate + promptMaximum
	if maximum < 1 {
		maximum = 1
	}
	return maximum, true
}

func maximumSequenceEstimatedInputHigh(
	cost Cost,
	shape RequestShape,
	features jsonFeatures,
	cfg EstimatorConfig,
	bodyBytes int,
) (int64, bool) {
	if cost.EstimatedInputHigh <= 0 || shape.PromptBatchSize <= 0 ||
		shape.PromptStringBytes < 0 || shape.MaximumPromptStringBytes < 0 ||
		shape.MaximumPromptStringBytes > shape.PromptStringBytes ||
		shape.PromptStringBytes > int64(features.StringValueBytes) ||
		shape.PromptStringBytes > int64(bodyBytes) ||
		shape.PromptStringBytes > int64(math.MaxInt) ||
		shape.MaximumPromptStringBytes > int64(math.MaxInt) {
		return 0, false
	}
	if shape.PromptBatchSize == 1 {
		return cost.EstimatedInputHigh, true
	}

	promptBytes := int(shape.PromptStringBytes)
	maximumPromptBytes := int(shape.MaximumPromptStringBytes)
	maximumStringBytes := features.StringValueBytes - promptBytes + maximumPromptBytes
	maximumBodyBytes := bodyBytes - promptBytes + maximumPromptBytes
	textHigh := ceilDiv(maximumStringBytes, cfg.MinBytesPerToken)
	toolHigh := ceilDiv(cost.ToolSchemaBytes, cfg.ToolMinBytesPerToken)
	templateHigh := cost.MessageCount * cfg.TemplateTokensPerMessageHigh
	modalityHigh := cost.ModalityCount * cfg.ModalityTokensHigh
	high := int64(textHigh + toolHigh + templateHigh + modalityHigh)
	wholeBodyHigh := int64(ceilDiv(maximumBodyBytes, cfg.MinBytesPerToken) + templateHigh + modalityHigh)
	if high < wholeBodyHigh {
		high = wholeBodyHigh
	}
	if high > math.MaxInt64-shape.MaximumExplicitPromptTokens {
		return 0, false
	}
	high += shape.MaximumExplicitPromptTokens
	if high < 1 {
		high = 1
	}
	if high > cost.EstimatedInputHigh {
		return 0, false
	}
	return high, true
}

func addRequestShapeTokens(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func fixedMarginTokens(tokens, numerator, denominator int64) (int64, bool) {
	if tokens <= 0 || numerator < denominator || denominator <= 0 ||
		tokens > math.MaxInt64/numerator {
		return 0, false
	}
	product := tokens * numerator
	if product > math.MaxInt64-(denominator-1) {
		return 0, false
	}
	return (product + denominator - 1) / denominator, true
}

func fixedMarginTokensForSequences(
	tokens,
	sequences,
	numerator,
	denominator int64,
) (int64, bool) {
	margin, ok := fixedMarginTokens(tokens, numerator, denominator)
	if !ok || sequences <= 0 || margin > math.MaxInt64-(sequences-1) {
		return 0, false
	}
	return margin + sequences - 1, true
}

type jsonFeatures struct {
	StringValueBytes            int
	ToolSchemaBytes             int
	MessageCount                int
	ToolCount                   int
	ModalityCount               int
	ApproximateInputTokens      int64
	ApproximateInputTokensKnown bool
	ConservativeInputEstimate   bool
}

func scanJSONFeatures(body []byte) (jsonFeatures, bool) {
	features := jsonFeatures{ApproximateInputTokensKnown: true}
	start := skipJSONSpace(body, 0)
	if start >= len(body) || body[start] != '{' {
		return features, false
	}
	end := len(body) - 1
	for end >= 0 && isJSONSpace(body[end]) {
		end--
	}
	if end <= start || body[end] != '}' {
		return features, false
	}
	var stack [128]byte
	depth := 0
	for index := start; index <= end; index++ {
		switch body[index] {
		case '"':
			// Empty and one-byte strings dominate array-style prompt payloads.
			// The body is already validated, so avoid a general quote search for
			// these unambiguous forms.
			closing := index + 1
			ok := closing <= end && body[closing] == '"'
			if !ok && closing < end && body[closing] != '\\' && body[closing+1] == '"' {
				closing++
				ok = true
			}
			if !ok {
				closing, ok = jsonStringEnd(body, index)
			}
			if !ok || closing > end {
				return features, false
			}
			raw := body[index+1 : closing]
			next := skipJSONSpace(body, closing+1)
			if next >= len(body) || body[next] != ':' {
				features.StringValueBytes += len(raw)
				hint, conservative, known := approximateJSONStringTokensWithRisk(raw)
				features.ConservativeInputEstimate = features.ConservativeInputEstimate || conservative
				if known {
					if !addApproximateInputTokens(&features.ApproximateInputTokens, hint) {
						features.ApproximateInputTokensKnown = false
					}
				} else {
					features.ApproximateInputTokensKnown = false
				}
				if len(raw) >= len("image_url") && modalityMarker(raw) {
					features.ModalityCount++
				}
				index = closing
				continue
			}
			switch {
			case bytes.Equal(raw, []byte("role")):
				features.MessageCount++
			case bytes.Equal(raw, []byte("function")):
				features.ToolCount++
			case bytes.Equal(raw, []byte("tools")), bytes.Equal(raw, []byte("functions")), bytes.Equal(raw, []byte("response_format")):
				valueStart := skipJSONSpace(body, next+1)
				valueBytes := jsonValueEnd(body, valueStart) - valueStart
				if valueBytes < 0 || features.ToolSchemaBytes > math.MaxInt-valueBytes {
					return features, false
				}
				features.ToolSchemaBytes += valueBytes
			}
			if len(raw) >= len("image_url") && modalityMarker(raw) {
				features.ModalityCount++
			}
			index = closing
		case '{':
			if depth >= len(stack) {
				return features, false
			}
			stack[depth] = '}'
			depth++
		case '[':
			if depth >= len(stack) {
				return features, false
			}
			stack[depth] = ']'
			depth++
		case '}', ']':
			if depth == 0 || stack[depth-1] != body[index] {
				return features, false
			}
			depth--
			if depth == 0 && index != end {
				return features, false
			}
		}
	}
	if !addApproximateInputTokens(&features.ApproximateInputTokens, int64(features.MessageCount)*4) {
		features.ApproximateInputTokensKnown = false
	}
	// String-value sampling cannot see JSON property names, delimiters, or the
	// template serialization of tool/response schemas. Charge one token per
	// four raw schema bytes in addition to sampled values. This is fixed,
	// model-neutral structural evidence, not a model-specific tokenizer rule.
	toolStructureTokens := int64(ceilDiv(features.ToolSchemaBytes, approximateASCIIBytesPerToken))
	if !addApproximateInputTokens(&features.ApproximateInputTokens, toolStructureTokens) {
		features.ApproximateInputTokensKnown = false
	}
	return features, depth == 0
}

func modalityMarker(raw []byte) bool {
	return bytes.Equal(raw, []byte("image_url")) ||
		bytes.Equal(raw, []byte("input_image")) ||
		bytes.Equal(raw, []byte("audio_url")) ||
		bytes.Equal(raw, []byte("input_audio")) ||
		bytes.Equal(raw, []byte("video_url")) ||
		bytes.Equal(raw, []byte("input_video"))
}

func jsonStringEnd(body []byte, opening int) (int, bool) {
	search := opening + 1
	for search < len(body) {
		relative := bytes.IndexByte(body[search:], '"')
		if relative < 0 {
			return 0, false
		}
		closing := search + relative
		backslashes := 0
		for index := closing - 1; index >= opening+1 && body[index] == '\\'; index-- {
			backslashes++
		}
		if backslashes%2 == 0 {
			return closing, true
		}
		search = closing + 1
	}
	return 0, false
}

func jsonValueEnd(body []byte, start int) int {
	if start >= len(body) {
		return start
	}
	if body[start] == '"' {
		if end, ok := jsonStringEnd(body, start); ok {
			return end + 1
		}
		return len(body)
	}
	if body[start] != '{' && body[start] != '[' {
		index := start
		for index < len(body) && body[index] != ',' && body[index] != '}' && body[index] != ']' {
			index++
		}
		return index
	}
	depth := 0
	for index := start; index < len(body); index++ {
		switch body[index] {
		case '"':
			if closing, ok := jsonStringEnd(body, index); ok {
				index = closing
			}
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return index + 1
			}
		}
	}
	return len(body)
}

func skipJSONSpace(body []byte, index int) int {
	for index < len(body) && isJSONSpace(body[index]) {
		index++
	}
	return index
}

func isJSONSpace(value byte) bool {
	return value == ' ' || value == '\n' || value == '\r' || value == '\t'
}

func validateEstimatorConfig(cfg EstimatorConfig) error {
	if cfg.MinBytesPerToken <= 0 || cfg.MaxBytesPerToken < cfg.MinBytesPerToken {
		return fmt.Errorf("invalid text byte/token bounds")
	}
	if cfg.ToolMinBytesPerToken <= 0 || cfg.ToolMaxBytesPerToken < cfg.ToolMinBytesPerToken {
		return fmt.Errorf("invalid tool byte/token bounds")
	}
	if cfg.TemplateTokensPerMessageLow < 0 || cfg.TemplateTokensPerMessageHigh < cfg.TemplateTokensPerMessageLow {
		return fmt.Errorf("invalid template bounds")
	}
	if cfg.ModalityTokensLow < 0 || cfg.ModalityTokensHigh < cfg.ModalityTokensLow {
		return fmt.Errorf("invalid modality bounds")
	}
	if cfg.BlindOutputTokens < 0 {
		return fmt.Errorf("invalid blind output tokens")
	}
	return nil
}

func ceilDiv(value, divisor int) int {
	if value <= 0 {
		return 0
	}
	return (value + divisor - 1) / divisor
}
