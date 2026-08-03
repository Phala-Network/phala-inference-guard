package kvadmission

import (
	"bytes"
	"fmt"
)

func EstimateJSON(body []byte, maxOutputTokens int, hasMaxOutputTokens bool, cfg EstimatorConfig) Cost {
	cost := Cost{
		BodyBytes:          len(body),
		MaxOutputTokens:    maxOutputTokens,
		HasMaxOutputTokens: hasMaxOutputTokens,
	}
	if err := validateEstimatorConfig(cfg); err != nil {
		cost.UnsupportedReason = "invalid_estimator_config"
		return cost
	}
	if len(body) == 0 {
		cost.UnsupportedReason = "empty_body"
		return cost
	}

	features, valid := scanJSONFeatures(body)
	if !valid {
		cost.UnsupportedReason = "invalid_json"
		return cost
	}
	cost.TextBytes = features.StringValueBytes
	cost.ToolSchemaBytes = features.ToolSchemaBytes
	cost.MessageCount = features.MessageCount
	cost.ToolCount = features.ToolCount
	cost.ModalityCount = features.ModalityCount
	cost.ApproximateInputTokens = features.ApproximateInputTokens
	cost.ApproximateInputTokensKnown = features.ApproximateInputTokensKnown && features.ApproximateInputTokens > 0

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
	cost.Supported = true
	return cost
}

type jsonFeatures struct {
	StringValueBytes            int
	ToolSchemaBytes             int
	MessageCount                int
	ToolCount                   int
	ModalityCount               int
	ApproximateInputTokens      int64
	ApproximateInputSampleBytes int
	ApproximateInputTokensKnown bool
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
			closing, ok := jsonStringEnd(body, index)
			if !ok || closing > end {
				return features, false
			}
			raw := body[index+1 : closing]
			next := skipJSONSpace(body, closing+1)
			if next >= len(body) || body[next] != ':' {
				features.StringValueBytes += len(raw)
				remainingBudget := approximateLexicalRequestBudget - features.ApproximateInputSampleBytes
				if hint, sampled, known := approximateJSONStringTokensWithBudget(raw, remainingBudget); known {
					features.ApproximateInputSampleBytes += sampled
					if !addApproximateInputTokens(&features.ApproximateInputTokens, hint) {
						features.ApproximateInputTokensKnown = false
					}
				} else {
					features.ApproximateInputTokensKnown = false
				}
				if modalityMarker(raw) {
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
				features.ToolSchemaBytes += jsonValueEnd(body, valueStart) - valueStart
			}
			if modalityMarker(raw) {
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
