package lexical

import (
	"math"
	"math/bits"
)

const (
	ASCIIBytesPerToken   = 4
	spaceBytesPerToken   = 32
	denseMinimumLength   = 256
	denseMinimumDistinct = 12
)

// EstimateJSONStringTokens returns a rough model-neutral token count for one
// raw JSON string value. The input excludes the surrounding quotes.
func EstimateJSONStringTokens(raw []byte) (int64, bool) {
	if len(raw) == 0 {
		return 0, true
	}
	if len(raw) <= 3 {
		return 1, true
	}

	var quarterTokenUnits int64
	var asciiWordRunBytes int64
	var stringSpaceBytes int64
	var seenLow uint64
	var seenHigh uint64
	denseASCIIBytes := 0
	transitions := 0
	previous := raw[0]
	for index, value := range raw {
		if index > 0 && value != previous {
			transitions++
		}
		previous = value
		if value < 0x80 && isASCIIDigit(value) {
			if !addRoundedRun(&quarterTokenUnits, asciiWordRunBytes, ASCIIBytesPerToken) ||
				!addQuarterTokenUnits(&quarterTokenUnits, ASCIIBytesPerToken) {
				return 0, false
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
				return 0, false
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
			return 0, false
		}
		asciiWordRunBytes = 0
		switch {
		case value < 0x80 && isDenseASCII(value):
			if !addQuarterTokenUnits(&quarterTokenUnits, ASCIIBytesPerToken) {
				return 0, false
			}
			denseASCIIBytes++
			seenLow |= uint64(1) << value
		case value < 0x80 && isJSONSpace(value):
			if stringSpaceBytes == math.MaxInt64 {
				return 0, false
			}
			stringSpaceBytes++
		case value < 0x80:
			if !addQuarterTokenUnits(&quarterTokenUnits, ASCIIBytesPerToken) {
				return 0, false
			}
		case value&0xc0 != 0x80:
			if !addQuarterTokenUnits(&quarterTokenUnits, ASCIIBytesPerToken) {
				return 0, false
			}
		}
	}
	if !addRoundedRun(&quarterTokenUnits, asciiWordRunBytes, ASCIIBytesPerToken) ||
		!addRoundedRun(&quarterTokenUnits, stringSpaceBytes, spaceBytesPerToken) {
		return 0, false
	}

	distinct := bits.OnesCount64(seenLow) + bits.OnesCount64(seenHigh)
	possibleTransitions := len(raw) - 1
	minimumDenseTransitions := possibleTransitions - possibleTransitions/4
	denseUnbrokenASCII := len(raw) >= denseMinimumLength &&
		denseASCIIBytes == len(raw) && distinct >= denseMinimumDistinct &&
		transitions >= minimumDenseTransitions
	if denseUnbrokenASCII {
		if int64(len(raw)) > math.MaxInt64/3 {
			return 0, false
		}
		denseUnits := int64(len(raw)) * 3
		if quarterTokenUnits < denseUnits {
			quarterTokenUnits = denseUnits
		}
	}
	if quarterTokenUnits <= 0 || quarterTokenUnits > math.MaxInt64-(ASCIIBytesPerToken-1) {
		return 0, quarterTokenUnits == 0
	}
	return (quarterTokenUnits + ASCIIBytesPerToken - 1) / ASCIIBytesPerToken, true
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
