package lexical

import (
	"math"
	"math/bits"
)

const (
	ASCIIBytesPerToken     = 4
	spaceBytesPerToken     = 32
	spaceOnlyBytesPerToken = 16
	denseMinimumLength     = 256
	denseMinimumDistinct   = 12
)

// EstimateJSONStringTokens returns a rough model-neutral token count for one
// raw JSON string value. The input excludes the surrounding quotes.
func EstimateJSONStringTokens(raw []byte) (int64, bool) {
	tokens, _, valid := EstimateJSONStringTokensWithRisk(raw)
	return tokens, valid
}

// EstimateJSONStringTokensWithRisk also reports lexical shapes for which a
// narrow fixed margin is not a suitable hard KV or Context estimate.
func EstimateJSONStringTokensWithRisk(raw []byte) (tokens int64, conservative bool, valid bool) {
	if len(raw) == 0 {
		return 0, false, true
	}
	if len(raw) <= 3 {
		for _, value := range raw {
			if value == '\\' || value >= 0x80 {
				conservative = true
			}
		}
		return 1, conservative, true
	}

	var quarterTokenUnits int64
	var asciiWordRunBytes int64
	var stringSpaceBytes int64
	var seenLow uint64
	var seenHigh uint64
	denseASCIIBytes := 0
	transitions := 0
	hasEscapeOrNonASCII := false
	whitespaceOnly := true
	spacesOnly := true
	previous := raw[0]
	for index, value := range raw {
		if index > 0 && value != previous {
			transitions++
		}
		previous = value
		if value == '\\' || value >= 0x80 {
			hasEscapeOrNonASCII = true
		}
		if whitespaceOnly {
			if !isJSONSpace(value) {
				whitespaceOnly = false
				spacesOnly = false
			} else if value != ' ' {
				spacesOnly = false
			}
		}
		if value < 0x80 && isASCIIDigit(value) {
			if !addRoundedRun(&quarterTokenUnits, asciiWordRunBytes, ASCIIBytesPerToken) ||
				!addQuarterTokenUnits(&quarterTokenUnits, ASCIIBytesPerToken) {
				return 0, false, false
			}
			asciiWordRunBytes = 0
			denseASCIIBytes++
			if value < 64 {
				seenLow |= uint64(1) << value
			} else {
				seenHigh |= uint64(1) << (value - 64)
			}
			continue
		}
		if value < 0x80 && isASCIIWord(value) {
			if asciiWordRunBytes == math.MaxInt64 {
				return 0, false, false
			}
			asciiWordRunBytes++
			denseASCIIBytes++
			if value < 64 {
				seenLow |= uint64(1) << value
			} else {
				seenHigh |= uint64(1) << (value - 64)
			}
			continue
		}
		if !addRoundedRun(&quarterTokenUnits, asciiWordRunBytes, ASCIIBytesPerToken) {
			return 0, false, false
		}
		asciiWordRunBytes = 0
		switch {
		case value < 0x80 && isDenseASCII(value):
			if !addQuarterTokenUnits(&quarterTokenUnits, ASCIIBytesPerToken) {
				return 0, false, false
			}
			denseASCIIBytes++
			seenLow |= uint64(1) << value
		case value < 0x80 && isJSONSpace(value):
			if stringSpaceBytes == math.MaxInt64 {
				return 0, false, false
			}
			stringSpaceBytes++
		case value < 0x80:
			if !addQuarterTokenUnits(&quarterTokenUnits, ASCIIBytesPerToken) {
				return 0, false, false
			}
		case value&0xc0 != 0x80:
			if !addQuarterTokenUnits(&quarterTokenUnits, ASCIIBytesPerToken) {
				return 0, false, false
			}
		}
	}
	if whitespaceOnly {
		tokens := int64(len(raw))
		if spacesOnly {
			tokens = roundedSpaceOnlyTokens(tokens)
		}
		return tokens, false, true
	}
	if !addRoundedRun(&quarterTokenUnits, asciiWordRunBytes, ASCIIBytesPerToken) ||
		!addRoundedRun(&quarterTokenUnits, stringSpaceBytes, spaceBytesPerToken) {
		return 0, false, false
	}

	distinct := bits.OnesCount64(seenLow) + bits.OnesCount64(seenHigh)
	possibleTransitions := len(raw) - 1
	minimumDenseTransitions := possibleTransitions - possibleTransitions/4
	denseUnbrokenASCII := len(raw) >= denseMinimumLength &&
		denseASCIIBytes == len(raw) && distinct >= denseMinimumDistinct &&
		transitions >= minimumDenseTransitions
	if denseUnbrokenASCII {
		if int64(len(raw)) > math.MaxInt64/3 {
			return 0, false, false
		}
		denseUnits := int64(len(raw)) * 3
		if quarterTokenUnits < denseUnits {
			quarterTokenUnits = denseUnits
		}
	}
	if quarterTokenUnits <= 0 || quarterTokenUnits > math.MaxInt64-(ASCIIBytesPerToken-1) {
		return 0, false, quarterTokenUnits == 0
	}
	return (quarterTokenUnits + ASCIIBytesPerToken - 1) / ASCIIBytesPerToken,
		hasEscapeOrNonASCII || denseUnbrokenASCII,
		true
}

// EstimateDecodedJSONStringTokensWithRisk estimates the decoded JSON string
// without allocating a complete decoded copy. It also returns the decoded
// UTF-8 byte count so callers do not have to use transport-encoded bytes as a
// Prompt-size proxy.
func EstimateDecodedJSONStringTokensWithRisk(
	raw []byte,
) (tokens, decodedBytes int64, conservative, valid bool) {
	estimator := jsonStringTokenEstimator{whitespaceOnly: true, spacesOnly: true}
	for index := 0; index < len(raw); {
		value := raw[index]
		if value != '\\' {
			if value >= 0x80 {
				estimator.conservative = true
			}
			if !estimator.add(value) {
				return 0, 0, false, false
			}
			index++
			continue
		}

		estimator.conservative = true
		index++
		if index >= len(raw) {
			return 0, 0, false, false
		}
		escape := raw[index]
		index++
		var decoded byte
		switch escape {
		case '"', '\\', '/':
			decoded = escape
		case 'b':
			decoded = '\b'
		case 'f':
			decoded = '\f'
		case 'n':
			decoded = '\n'
		case 'r':
			decoded = '\r'
		case 't':
			decoded = '\t'
		case 'u':
			codeUnit, next, ok := decodedJSONStringCodeUnit(raw, index)
			if !ok {
				return 0, 0, false, false
			}
			index = next
			codePoint := int64(codeUnit)
			switch {
			case codeUnit >= 0xd800 && codeUnit <= 0xdbff:
				if len(raw)-index < 6 || raw[index] != '\\' || raw[index+1] != 'u' {
					return 0, 0, false, false
				}
				low, lowNext, lowOK := decodedJSONStringCodeUnit(raw, index+2)
				if !lowOK || low < 0xdc00 || low > 0xdfff {
					return 0, 0, false, false
				}
				index = lowNext
				codePoint = 0x10000 + int64(codeUnit-0xd800)*0x400 + int64(low-0xdc00)
			case codeUnit >= 0xdc00 && codeUnit <= 0xdfff:
				return 0, 0, false, false
			}
			if !estimator.addCodePoint(codePoint) {
				return 0, 0, false, false
			}
			continue
		default:
			return 0, 0, false, false
		}
		if !estimator.add(decoded) {
			return 0, 0, false, false
		}
	}
	tokens, conservative, valid = estimator.finish()
	return tokens, estimator.decodedBytes, conservative, valid
}

type jsonStringTokenEstimator struct {
	quarterTokenUnits int64
	asciiWordRunBytes int64
	stringSpaceBytes  int64
	decodedBytes      int64
	transitions       int64
	seenLow           uint64
	seenHigh          uint64
	denseASCIIBytes   int64
	previous          byte
	hasPrevious       bool
	conservative      bool
	whitespaceOnly    bool
	spacesOnly        bool
}

func (e *jsonStringTokenEstimator) add(value byte) bool {
	if e == nil || e.decodedBytes == math.MaxInt64 {
		return false
	}
	if e.hasPrevious && value != e.previous {
		if e.transitions == math.MaxInt64 {
			return false
		}
		e.transitions++
	}
	e.previous = value
	e.hasPrevious = true
	e.decodedBytes++
	if e.whitespaceOnly {
		if !isJSONSpace(value) {
			e.whitespaceOnly = false
			e.spacesOnly = false
		} else if value != ' ' {
			e.spacesOnly = false
		}
	}

	if value < 0x80 && isASCIIDigit(value) {
		if !addRoundedRun(&e.quarterTokenUnits, e.asciiWordRunBytes, ASCIIBytesPerToken) ||
			!addQuarterTokenUnits(&e.quarterTokenUnits, ASCIIBytesPerToken) {
			return false
		}
		e.asciiWordRunBytes = 0
		e.denseASCIIBytes++
		if value < 64 {
			e.seenLow |= uint64(1) << value
		} else {
			e.seenHigh |= uint64(1) << (value - 64)
		}
		return true
	}
	if value < 0x80 && isASCIIWord(value) {
		if e.asciiWordRunBytes == math.MaxInt64 {
			return false
		}
		e.asciiWordRunBytes++
		e.denseASCIIBytes++
		if value < 64 {
			e.seenLow |= uint64(1) << value
		} else {
			e.seenHigh |= uint64(1) << (value - 64)
		}
		return true
	}
	if !addRoundedRun(&e.quarterTokenUnits, e.asciiWordRunBytes, ASCIIBytesPerToken) {
		return false
	}
	e.asciiWordRunBytes = 0
	switch {
	case value < 0x80 && isDenseASCII(value):
		if !addQuarterTokenUnits(&e.quarterTokenUnits, ASCIIBytesPerToken) {
			return false
		}
		e.denseASCIIBytes++
		e.seenLow |= uint64(1) << value
	case value < 0x80 && isJSONSpace(value):
		if e.stringSpaceBytes == math.MaxInt64 {
			return false
		}
		e.stringSpaceBytes++
	case value < 0x80:
		if !addQuarterTokenUnits(&e.quarterTokenUnits, ASCIIBytesPerToken) {
			return false
		}
	case value&0xc0 != 0x80:
		if !addQuarterTokenUnits(&e.quarterTokenUnits, ASCIIBytesPerToken) {
			return false
		}
	}
	return true
}

func (e *jsonStringTokenEstimator) addCodePoint(codePoint int64) bool {
	switch {
	case codePoint < 0 || codePoint > 0x10ffff:
		return false
	case codePoint <= 0x7f:
		return e.add(byte(codePoint))
	case codePoint <= 0x7ff:
		return e.add(0xc0|byte(codePoint>>6)) &&
			e.add(0x80|byte(codePoint&0x3f))
	case codePoint <= 0xffff:
		return e.add(0xe0|byte(codePoint>>12)) &&
			e.add(0x80|byte((codePoint>>6)&0x3f)) &&
			e.add(0x80|byte(codePoint&0x3f))
	default:
		return e.add(0xf0|byte(codePoint>>18)) &&
			e.add(0x80|byte((codePoint>>12)&0x3f)) &&
			e.add(0x80|byte((codePoint>>6)&0x3f)) &&
			e.add(0x80|byte(codePoint&0x3f))
	}
}

func (e *jsonStringTokenEstimator) finish() (tokens int64, conservative, valid bool) {
	if e == nil {
		return 0, false, false
	}
	if e.decodedBytes == 0 {
		return 0, false, true
	}
	if e.decodedBytes <= 3 {
		return 1, e.conservative, true
	}
	if e.whitespaceOnly {
		tokens := e.decodedBytes
		if e.spacesOnly {
			tokens = roundedSpaceOnlyTokens(tokens)
		}
		return tokens, e.conservative, true
	}
	if !addRoundedRun(&e.quarterTokenUnits, e.asciiWordRunBytes, ASCIIBytesPerToken) ||
		!addRoundedRun(&e.quarterTokenUnits, e.stringSpaceBytes, spaceBytesPerToken) {
		return 0, false, false
	}
	distinct := bits.OnesCount64(e.seenLow) + bits.OnesCount64(e.seenHigh)
	possibleTransitions := e.decodedBytes - 1
	minimumDenseTransitions := possibleTransitions - possibleTransitions/4
	denseUnbrokenASCII := e.decodedBytes >= denseMinimumLength &&
		e.denseASCIIBytes == e.decodedBytes && distinct >= denseMinimumDistinct &&
		e.transitions >= minimumDenseTransitions
	if denseUnbrokenASCII {
		if e.decodedBytes > math.MaxInt64/3 {
			return 0, false, false
		}
		denseUnits := e.decodedBytes * 3
		if e.quarterTokenUnits < denseUnits {
			e.quarterTokenUnits = denseUnits
		}
	}
	if e.quarterTokenUnits <= 0 || e.quarterTokenUnits > math.MaxInt64-(ASCIIBytesPerToken-1) {
		return 0, false, e.quarterTokenUnits == 0
	}
	return (e.quarterTokenUnits + ASCIIBytesPerToken - 1) / ASCIIBytesPerToken,
		e.conservative || denseUnbrokenASCII,
		true
}

func roundedSpaceOnlyTokens(spaceBytes int64) int64 {
	tokens := spaceBytes / spaceOnlyBytesPerToken
	if spaceBytes%spaceOnlyBytesPerToken != 0 {
		tokens++
	}
	return tokens
}

func decodedJSONStringCodeUnit(raw []byte, start int) (uint16, int, bool) {
	if start < 0 || len(raw)-start < 4 {
		return 0, start, false
	}
	var result uint16
	for offset := 0; offset < 4; offset++ {
		value, ok := decodedJSONStringHex(raw[start+offset])
		if !ok {
			return 0, start, false
		}
		result = result<<4 | uint16(value)
	}
	return result, start + 4, true
}

func decodedJSONStringHex(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func addRoundedRun(total *int64, runBytes, bytesPerToken int64) bool {
	if runBytes == 0 {
		return true
	}
	if runBytes < 0 || bytesPerToken <= 0 ||
		runBytes > math.MaxInt64-(bytesPerToken-1) {
		return false
	}
	tokens := (runBytes + bytesPerToken - 1) / bytesPerToken
	if tokens > math.MaxInt64/ASCIIBytesPerToken {
		return false
	}
	return addQuarterTokenUnits(total, tokens*ASCIIBytesPerToken)
}

func isASCIIWord(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value == '_'
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isDenseASCII(value byte) bool {
	return value == '+' || value == '/' || value == '-' || value == '='
}

func isJSONSpace(value byte) bool {
	return value == ' ' || value == '\n' || value == '\r' || value == '\t'
}

func addQuarterTokenUnits(total *int64, increment int64) bool {
	if total == nil || increment < 0 || *total > math.MaxInt64-increment {
		return false
	}
	*total += increment
	return true
}
