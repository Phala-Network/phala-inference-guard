package kvadmission

import "math"

const (
	approximateASCIIBytesPerToken    = 4
	approximateLexicalPerStringLimit = 64
	approximateLexicalWindows        = 4
)

// approximateJSONStringTokens returns a deliberately rough model-neutral
// lexical-size hint. Long strings are sampled with fixed work; exact token
// IDs, vocabulary, templates, full Unicode traversal, allocation, network,
// RPC, and FFI are all outside this contract.
func approximateJSONStringTokens(raw []byte) (int64, bool) {
	if len(raw) <= 3 {
		if len(raw) == 0 {
			return 0, true
		}
		return 1, true
	}
	tokens, _, known := approximateJSONStringTokensWithBudget(raw, approximateLexicalPerStringLimit)
	return tokens, known
}

func approximateJSONStringTokensWithBudget(raw []byte, remainingBudget int) (int64, int, bool) {
	if len(raw) == 0 {
		return 0, 0, true
	}
	sampleBytes := remainingBudget
	if sampleBytes > approximateLexicalPerStringLimit {
		sampleBytes = approximateLexicalPerStringLimit
	}
	if sampleBytes > len(raw) {
		sampleBytes = len(raw)
	}
	if sampleBytes <= 0 {
		return int64((len(raw) + approximateASCIIBytesPerToken - 1) / approximateASCIIBytesPerToken), 0, true
	}

	windows := approximateLexicalWindows
	if len(raw) <= sampleBytes {
		windows = 1
	} else if windows > sampleBytes {
		windows = sampleBytes
	}
	baseWindow := sampleBytes / windows
	remainder := sampleBytes % windows
	sampled := 0
	var quarterTokenUnits int64
	for window := 0; window < windows; window++ {
		windowBytes := baseWindow
		if window < remainder {
			windowBytes++
		}
		start := 0
		if windows > 1 {
			start = window * (len(raw) - windowBytes) / (windows - 1)
		}
		for _, value := range raw[start : start+windowBytes] {
			switch {
			case value < 0x80 && isApproximateASCIIWord(value):
				quarterTokenUnits++
			case value < 0x80 && isJSONSpace(value):
			case value < 0x80:
				quarterTokenUnits += approximateASCIIBytesPerToken
			case value&0xc0 != 0x80:
				quarterTokenUnits += approximateASCIIBytesPerToken
			}
		}
		sampled += windowBytes
	}
	if sampled <= 0 || quarterTokenUnits == 0 {
		return 0, sampled, true
	}
	length := int64(len(raw))
	if quarterTokenUnits > math.MaxInt64/length {
		return 0, sampled, false
	}
	numerator := quarterTokenUnits * length
	denominator := int64(sampled * approximateASCIIBytesPerToken)
	if numerator > math.MaxInt64-(denominator-1) {
		return 0, sampled, false
	}
	return (numerator + denominator - 1) / denominator, sampled, true
}

func isApproximateASCIIWord(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '_'
}

func addApproximateInputTokens(total *int64, increment int64) bool {
	if total == nil || increment < 0 || *total > math.MaxInt64-increment {
		return false
	}
	*total += increment
	return true
}
