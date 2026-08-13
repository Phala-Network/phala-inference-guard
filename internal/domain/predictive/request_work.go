package predictive

import (
	"fmt"
	"math"
)

// RequestEstimate is the complete model-neutral output of request estimation.
// It contains no backend geometry, runtime state, request identity, or mode.
type RequestEstimate struct {
	SelectionInputTokens     int64
	KVReservationInputTokens int64
	DecodeHorizonTokens      int64
}

func (e RequestEstimate) Validate() error {
	if e.SelectionInputTokens <= 0 || e.KVReservationInputTokens <= 0 ||
		e.KVReservationInputTokens < e.SelectionInputTokens || e.DecodeHorizonTokens < 0 {
		return fmt.Errorf("request estimate is invalid")
	}
	if e.KVReservationInputTokens > math.MaxInt64-e.DecodeHorizonTokens {
		return fmt.Errorf("request estimate context overflows")
	}
	return nil
}

// RequestWork is the immutable, block-aligned form consumed by admission. It
// is derived from one RequestEstimate and the Controller's immutable block
// size; clients never provide pre-rounded KV values.
type RequestWork struct {
	Estimate       RequestEstimate
	InputKVTokens  int64
	TotalKVTokens  int64
	FutureKVTokens int64
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
	totalKV, ok := requestWorkRoundUp(
		estimate.KVReservationInputTokens+estimate.DecodeHorizonTokens,
		blockSize,
	)
	if !ok || totalKV < inputKV {
		return RequestWork{}, fmt.Errorf("request work total KV is invalid")
	}
	return RequestWork{
		Estimate:       estimate,
		InputKVTokens:  inputKV,
		TotalKVTokens:  totalKV,
		FutureKVTokens: totalKV - inputKV,
	}, nil
}

func requestWorkRoundUp(value, blockSize int64) (int64, bool) {
	if value <= 0 || blockSize <= 0 || value > math.MaxInt64-(blockSize-1) {
		return 0, false
	}
	return ((value + blockSize - 1) / blockSize) * blockSize, true
}
