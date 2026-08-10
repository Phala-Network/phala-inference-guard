package predictive

import (
	"fmt"
	"math"
)

type RequestCostInput struct {
	ManifestID             string
	BlockSize              int64
	SelectionPrefillTokens int64
	SafetyInputTokens      int64
	DecodeHorizonTokens    int64
	Confidence             float64
}

func BuildRequestCost(input RequestCostInput) (RequestCost, error) {
	if input.ManifestID == "" || input.BlockSize <= 0 || input.SelectionPrefillTokens <= 0 ||
		input.SafetyInputTokens <= 0 || input.DecodeHorizonTokens < 0 ||
		input.Confidence <= 0 || input.Confidence > 1 || math.IsNaN(input.Confidence) || math.IsInf(input.Confidence, 0) {
		return RequestCost{}, fmt.Errorf("request cost input is invalid")
	}
	safetyInputTokens := input.SafetyInputTokens
	if input.SelectionPrefillTokens > safetyInputTokens {
		safetyInputTokens = input.SelectionPrefillTokens
	}
	if safetyInputTokens > math.MaxInt64-input.DecodeHorizonTokens {
		return RequestCost{}, fmt.Errorf("request cost context overflows")
	}
	activeContext := safetyInputTokens + input.DecodeHorizonTokens
	inputKV, ok := requestCostRoundUp(safetyInputTokens, input.BlockSize)
	if !ok {
		return RequestCost{}, fmt.Errorf("request cost input KV is invalid")
	}
	totalKV, ok := requestCostRoundUp(activeContext, input.BlockSize)
	if !ok || totalKV < inputKV {
		return RequestCost{}, fmt.Errorf("request cost total KV is invalid")
	}
	futureKV := totalKV - inputKV
	return RequestCost{
		ManifestID:  input.ManifestID,
		InputTokens: safetyInputTokens,
		KV: KVIncrement{
			PhysicalKVUpper: totalKV,
			ActiveKVUpper:   totalKV,
		},
		FutureKV: KVIncrement{
			PhysicalKVUpper: futureKV,
			ActiveKVUpper:   futureKV,
		},
		UncachedPrefillUpper:     safetyInputTokens,
		DecodeHorizonUpper:       input.DecodeHorizonTokens,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: activeContext,
		FutureContextTokensUpper: input.DecodeHorizonTokens,
		Confidence:               input.Confidence,
	}, nil
}

func requestCostRoundUp(value, blockSize int64) (int64, bool) {
	if value <= 0 || blockSize <= 0 || value > math.MaxInt64-(blockSize-1) {
		return 0, false
	}
	return ((value + blockSize - 1) / blockSize) * blockSize, true
}
