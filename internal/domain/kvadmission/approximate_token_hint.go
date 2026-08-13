package kvadmission

import (
	"math"
	"math/bits"
)

const (
	approximateASCIIBytesPerToken   = 4
	approximateSpaceBytesPerToken   = 32
	approximateDenseMinimumLength   = 256
	approximateDenseMinimumDistinct = 12
)

// approximateJSONStringTokens scans one raw JSON string value and returns a
// deliberately rough model-neutral lexical-size estimate. Feature extraction
// already locates every string boundary after bounded JSON validation, so
// counting complete string values avoids sampling blind spots without another
// body copy, tokenizer asset, vocabulary lookup, template execution, RPC, or
// FFI.
func approximateJSONStringTokens(raw []byte) (int64, bool) {
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
		if value < 0x80 && isApproximateASCIIDigit(value) {
			if !addApproximateRoundedRun(
				&quarterTokenUnits,
				asciiWordRunBytes,
				approximateASCIIBytesPerToken,
			) ||
				!addApproximateQuarterTokenUnits(
					&quarterTokenUnits,
					approximateASCIIBytesPerToken,
				) {
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
		if value < 0x80 && isApproximateASCIIWord(value) {
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
		if !addApproximateRoundedRun(
			&quarterTokenUnits,
			asciiWordRunBytes,
			approximateASCIIBytesPerToken,
		) {
			return 0, false
		}
		asciiWordRunBytes = 0
		switch {
		case value < 0x80 && isApproximateDenseASCII(value):
			if !addApproximateQuarterTokenUnits(&quarterTokenUnits, approximateASCIIBytesPerToken) {
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
			if !addApproximateQuarterTokenUnits(&quarterTokenUnits, approximateASCIIBytesPerToken) {
				return 0, false
			}
		case value&0xc0 != 0x80:
			if !addApproximateQuarterTokenUnits(&quarterTokenUnits, approximateASCIIBytesPerToken) {
				return 0, false
			}
		}
	}
	if !addApproximateRoundedRun(
		&quarterTokenUnits,
		asciiWordRunBytes,
		approximateASCIIBytesPerToken,
	) || !addApproximateRoundedRun(
		&quarterTokenUnits,
		stringSpaceBytes,
		approximateSpaceBytesPerToken,
	) {
		return 0, false
	}

	distinct := bits.OnesCount64(seenLow) + bits.OnesCount64(seenHigh)
	possibleTransitions := len(raw) - 1
	minimumDenseTransitions := possibleTransitions - possibleTransitions/4
	denseUnbrokenASCII := len(raw) >= approximateDenseMinimumLength &&
		denseASCIIBytes == len(raw) && distinct >= approximateDenseMinimumDistinct &&
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
	if quarterTokenUnits <= 0 || quarterTokenUnits > math.MaxInt64-(approximateASCIIBytesPerToken-1) {
		return 0, quarterTokenUnits == 0
	}
	return (quarterTokenUnits + approximateASCIIBytesPerToken - 1) /
		approximateASCIIBytesPerToken, true
}

// addApproximateRoundedRun settles one complete lexical category rather than
// pooling bytes across boundaries. That preserves the cheap bytes-per-token
// approximation while ensuring many independent one-byte words cannot collapse
// into one token merely because whitespace separated them.
func addApproximateRoundedRun(total *int64, runBytes, bytesPerToken int64) bool {
	if runBytes == 0 {
		return true
	}
	if runBytes < 0 || bytesPerToken <= 0 ||
		runBytes > math.MaxInt64-(bytesPerToken-1) {
		return false
	}
	tokens := (runBytes + bytesPerToken - 1) / bytesPerToken
	if tokens > math.MaxInt64/approximateASCIIBytesPerToken {
		return false
	}
	return addApproximateQuarterTokenUnits(
		total,
		tokens*approximateASCIIBytesPerToken,
	)
}

func isApproximateASCIIWord(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value == '_'
}

func isApproximateASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isApproximateDenseASCII(value byte) bool {
	return value == '+' || value == '/' || value == '-' || value == '='
}

func addApproximateQuarterTokenUnits(total *int64, increment int64) bool {
	if total == nil || increment < 0 || *total > math.MaxInt64-increment {
		return false
	}
	*total += increment
	return true
}

func addApproximateInputTokens(total *int64, increment int64) bool {
	if total == nil || increment < 0 || *total > math.MaxInt64-increment {
		return false
	}
	*total += increment
	return true
}
