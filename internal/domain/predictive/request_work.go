package predictive

import (
	"fmt"
	"math"
)

// RequestEstimate is the complete model-neutral output of request estimation.
// It contains no backend geometry, runtime state, request identity, or mode.
type RequestEstimate struct {
	SelectionInputTokens       int64
	MaximumSequenceInputTokens int64
	KVReservationInputTokens   int64
	DecodeHorizonTokens        int64
	BasePromptCount            int64
	DecodeSequences            int64
}

func (e RequestEstimate) Validate() error {
	if e.SelectionInputTokens <= 0 || e.MaximumSequenceInputTokens <= 0 ||
		e.MaximumSequenceInputTokens > e.SelectionInputTokens || e.KVReservationInputTokens <= 0 ||
		e.KVReservationInputTokens < e.SelectionInputTokens || e.DecodeHorizonTokens < 0 || e.DecodeSequences <= 0 {
		return fmt.Errorf("request estimate is invalid")
	}
	if e.MaximumSequenceInputTokens > math.MaxInt64-e.DecodeHorizonTokens {
		return fmt.Errorf("request estimate per-sequence context overflows")
	}
	if e.DecodeHorizonTokens > 0 && e.DecodeSequences > math.MaxInt64/e.DecodeHorizonTokens {
		return fmt.Errorf("request estimate Decode demand overflows")
	}
	aggregateDecode := e.DecodeHorizonTokens * e.DecodeSequences
	if e.KVReservationInputTokens > math.MaxInt64-aggregateDecode {
		return fmt.Errorf("request estimate aggregate KV overflows")
	}
	return nil
}

// RequestWork is the immutable, block-aligned form consumed by admission. It
// is derived from one RequestEstimate and the Controller's immutable block
// size; clients never provide pre-rounded KV values.
type RequestWork struct {
	Estimate             RequestEstimate
	PrefillComputeTokens int64
	InputKVTokens        int64
	TotalKVTokens        int64
	FutureKVTokens       int64
}

func BuildRequestWork(estimate RequestEstimate, blockSize int64) (RequestWork, error) {
	if err := estimate.Validate(); err != nil {
		return RequestWork{}, err
	}
	if blockSize <= 0 {
		return RequestWork{}, fmt.Errorf("request work block size is invalid")
	}
	inputKV, ok := requestWorkRoundUp(estimate.KVReservationInputTokens, blockSize)
	if !ok {
		return RequestWork{}, fmt.Errorf("request work input KV is invalid")
	}
	futureKV := int64(0)
	if estimate.DecodeHorizonTokens > 0 {
		firstTotal, valid := requestWorkRoundUp(
			estimate.KVReservationInputTokens+estimate.DecodeHorizonTokens,
			blockSize,
		)
		if !valid || firstTotal < inputKV {
			return RequestWork{}, fmt.Errorf("request work future KV is invalid")
		}
		futureKV = firstTotal - inputKV
		if estimate.DecodeSequences > 1 {
			additionalFuture, valid := requestWorkRoundUp(estimate.DecodeHorizonTokens, blockSize)
			additionalSequences := estimate.DecodeSequences - 1
			if !valid || additionalSequences > math.MaxInt64/additionalFuture ||
				futureKV > math.MaxInt64-additionalSequences*additionalFuture {
				return RequestWork{}, fmt.Errorf("request work future KV is invalid")
			}
			futureKV += additionalSequences * additionalFuture
		}
	}
	if inputKV > math.MaxInt64-futureKV {
		return RequestWork{}, fmt.Errorf("request work total KV is invalid")
	}
	totalKV := inputKV + futureKV
	return RequestWork{
		Estimate:             estimate,
		PrefillComputeTokens: estimate.SelectionInputTokens,
		InputKVTokens:        inputKV,
		TotalKVTokens:        totalKV,
		FutureKVTokens:       futureKV,
	}, nil
}

func requestWorkRoundUp(value, blockSize int64) (int64, bool) {
	if value <= 0 || blockSize <= 0 || value > math.MaxInt64-(blockSize-1) {
		return 0, false
	}
	return ((value + blockSize - 1) / blockSize) * blockSize, true
}
