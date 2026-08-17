package predictive

import (
	"fmt"
	"math"
)

// RequestEstimate is the complete model-neutral output of request estimation.
// It contains no backend geometry, runtime state, request identity, or mode.
type RequestEstimate struct {
	SelectionInputTokens                     int64
	MaximumSequenceInputTokens              int64
	KVReservationInputTokens                int64
	MaximumSequenceKVReservationInputTokens int64
	DecodeHorizonTokens                      int64
	BasePromptCount                          int64
	DecodeSequences                          int64
}

func (e RequestEstimate) Validate() error {
	if e.SelectionInputTokens <= 0 || e.MaximumSequenceInputTokens <= 0 ||
		e.MaximumSequenceInputTokens > e.SelectionInputTokens || e.KVReservationInputTokens <= 0 ||
		e.KVReservationInputTokens < e.SelectionInputTokens ||
		e.MaximumSequenceKVReservationInputTokens < e.MaximumSequenceInputTokens ||
		e.MaximumSequenceKVReservationInputTokens > e.KVReservationInputTokens ||
		e.DecodeHorizonTokens < 0 || e.BasePromptCount <= 0 ||
		decodeShapeInvalid(e.BasePromptCount, e.DecodeSequences) {
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

func decodeShapeInvalid(basePromptCount, decodeSequences int64) bool {
	return decodeSequences <= 0 || basePromptCount > decodeSequences ||
		decodeSequences%basePromptCount != 0
}

type InputAccounting uint8

const (
	InputAccountingBasePrompts InputAccounting = iota + 1
	InputAccountingDecodeSequences
)

func (a InputAccounting) String() string {
	switch a {
	case InputAccountingBasePrompts:
		return "base_prompts"
	case InputAccountingDecodeSequences:
		return "decode_sequences"
	default:
		return "invalid"
	}
}

type RequestWorkProfile struct {
	InputAccounting InputAccounting
}

func (p RequestWorkProfile) Validate() error {
	switch p.InputAccounting {
	case InputAccountingBasePrompts, InputAccountingDecodeSequences:
		return nil
	default:
		return fmt.Errorf("request work profile is invalid")
	}
}

// RequestWork is the immutable, block-aligned form consumed by admission. It
// is derived from one RequestEstimate and the Controller's immutable block
// size; clients never provide pre-rounded KV values.
type RequestWork struct {
	Estimate             RequestEstimate
	PrefillInputTokens   int64
	PrefillComputeTokens int64
	InputKVTokens        int64
	TotalKVTokens        int64
	FutureKVTokens       int64
}

func BuildRequestWork(
	estimate RequestEstimate,
	profile RequestWorkProfile,
	blockSize int64,
) (RequestWork, error) {
	if err := estimate.Validate(); err != nil {
		return RequestWork{}, err
	}
	if err := profile.Validate(); err != nil {
		return RequestWork{}, err
	}
	if blockSize <= 0 {
		return RequestWork{}, fmt.Errorf("request work block size is invalid")
	}
	replication := int64(1)
	inputSequences := estimate.BasePromptCount
	if profile.InputAccounting == InputAccountingDecodeSequences {
		replication = estimate.DecodeSequences / estimate.BasePromptCount
		inputSequences = estimate.DecodeSequences
	}
	prefillInput, ok := requestWorkMultiply(estimate.SelectionInputTokens, replication)
	if !ok {
		return RequestWork{}, fmt.Errorf("request work Prefill input is invalid")
	}
	inputEstimate, ok := requestWorkMultiply(estimate.KVReservationInputTokens, replication)
	if !ok {
		return RequestWork{}, fmt.Errorf("request work input KV is invalid")
	}
	inputKV, ok := requestWorkRoundUpAcrossSequences(
		inputEstimate,
		estimate.MaximumSequenceKVReservationInputTokens,
		inputSequences,
		blockSize,
	)
	if !ok {
		return RequestWork{}, fmt.Errorf("request work input KV is invalid")
	}
	futureKV := int64(0)
	if estimate.DecodeHorizonTokens > 0 {
		fullSequenceFuture, valid := requestWorkRoundUp(estimate.DecodeHorizonTokens, blockSize)
		if !valid {
			return RequestWork{}, fmt.Errorf("request work future KV is invalid")
		}
		if estimate.BasePromptCount == 1 &&
			profile.InputAccounting == InputAccountingBasePrompts {
			firstTotal, valid := requestWorkRoundUp(
				estimate.KVReservationInputTokens+estimate.DecodeHorizonTokens,
				blockSize,
			)
			baseInput, baseValid := requestWorkRoundUp(
				estimate.KVReservationInputTokens,
				blockSize,
			)
			if !valid || !baseValid || firstTotal < baseInput {
				return RequestWork{}, fmt.Errorf("request work future KV is invalid")
			}
			futureKV = firstTotal - baseInput
			additionalSequences := estimate.DecodeSequences - 1
			additionalFuture, valid := requestWorkMultiply(fullSequenceFuture, additionalSequences)
			if !valid || futureKV > math.MaxInt64-additionalFuture {
				return RequestWork{}, fmt.Errorf("request work future KV is invalid")
			}
			futureKV += additionalFuture
		} else {
			futureKV, valid = requestWorkMultiply(fullSequenceFuture, estimate.DecodeSequences)
			if !valid {
				return RequestWork{}, fmt.Errorf("request work future KV is invalid")
			}
		}
	}
	if inputKV > math.MaxInt64-futureKV {
		return RequestWork{}, fmt.Errorf("request work total KV is invalid")
	}
	totalKV := inputKV + futureKV
	return RequestWork{
		Estimate:             estimate,
		PrefillInputTokens:   prefillInput,
		PrefillComputeTokens: prefillInput,
		InputKVTokens:        inputKV,
		TotalKVTokens:        totalKV,
		FutureKVTokens:       futureKV,
	}, nil
}

func requestWorkRoundUpAcrossSequences(
	totalTokens,
	maximumSequenceTokens,
	sequences,
	blockSize int64,
) (int64, bool) {
	if totalTokens <= 0 || maximumSequenceTokens <= 0 ||
		maximumSequenceTokens > totalTokens || sequences <= 0 || blockSize <= 0 {
		return 0, false
	}
	aggregateRounded, ok := requestWorkRoundUp(totalTokens, blockSize)
	if !ok {
		return 0, false
	}
	aggregateBlocks := aggregateRounded / blockSize
	if aggregateBlocks > math.MaxInt64-(sequences-1) {
		return 0, false
	}
	blockUpper := aggregateBlocks + sequences - 1
	maximumRounded, ok := requestWorkRoundUp(maximumSequenceTokens, blockSize)
	if !ok {
		return 0, false
	}
	maximumBlocks := maximumRounded / blockSize
	perSequenceUpper, ok := requestWorkMultiply(maximumBlocks, sequences)
	if !ok {
		return 0, false
	}
	if blockUpper > perSequenceUpper {
		blockUpper = perSequenceUpper
	}
	if blockUpper > totalTokens {
		blockUpper = totalTokens
	}
	return requestWorkMultiply(blockUpper, blockSize)
}

func requestWorkMultiply(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || (left > 0 && right > math.MaxInt64/left) {
		return 0, false
	}
	return left * right, true
}

func requestWorkRoundUp(value, blockSize int64) (int64, bool) {
	if value <= 0 || blockSize <= 0 || value > math.MaxInt64-(blockSize-1) {
		return 0, false
	}
	return ((value + blockSize - 1) / blockSize) * blockSize, true
}
