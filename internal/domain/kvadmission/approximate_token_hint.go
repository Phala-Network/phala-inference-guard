package kvadmission

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/lexical"
)

const approximateASCIIBytesPerToken = lexical.ASCIIBytesPerToken

func approximateJSONStringTokens(raw []byte) (int64, bool) {
	return lexical.EstimateJSONStringTokens(raw)
}

func approximateJSONStringTokensWithRisk(raw []byte) (int64, bool, bool) {
	return lexical.EstimateJSONStringTokensWithRisk(raw)
}

func addApproximateInputTokens(total *int64, increment int64) bool {
	if total == nil || increment < 0 || *total > math.MaxInt64-increment {
		return false
	}
	*total += increment
	return true
}
