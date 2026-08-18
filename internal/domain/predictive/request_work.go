package predictive

import (
	"fmt"
	"math"
)

type InputEstimateConfidence uint8

const (
	InputEstimateConfidenceUnknown InputEstimateConfidence = iota
	InputEstimateConfidenceLexical
	InputEstimateConfidenceConservative
)

func (c InputEstimateConfidence) String() string {
	switch c {
	case InputEstimateConfidenceLexical:
		return "lexical"
	case InputEstimateConfidenceConservative:
		return "conservative"
	default:
		return "unknown"
	}
}

// RequestEstimate is the complete model-neutral output of request estimation.
// It contains no backend geometry, runtime state, request identity, or mode.
type RequestEstimate struct {
	SelectionInputTokens                    int64
	MaximumSequenceInputTokens              int64
	KVReservationInputTokens                int64
	MaximumSequenceKVReservationInputTokens int64
	DecodeHorizonTokens                     int64
	OutputLimitTokens                       int64
	OutputLimitKnown                        bool
	BasePromptCount                         int64
	DecodeSequences                         int64
	InputEstimateConfidence                 InputEstimateConfidence
}

func (e RequestEstimate) Validate() error {
	if e.SelectionInputTokens <= 0 || e.MaximumSequenceInputTokens <= 0 ||
		e.MaximumSequenceInputTokens > e.SelectionInputTokens || e.KVReservationInputTokens <= 0 ||
		e.KVReservationInputTokens < e.SelectionInputTokens ||
		e.MaximumSequenceKVReservationInputTokens < e.MaximumSequenceInputTokens ||
		e.MaximumSequenceKVReservationInputTokens > e.KVReservationInputTokens ||
		e.DecodeHorizonTokens < 0 || e.OutputLimitTokens < 0 ||
		(!e.OutputLimitKnown && e.OutputLimitTokens != 0) ||
		(e.OutputLimitKnown && e.DecodeHorizonTokens > e.OutputLimitTokens) ||
		e.BasePromptCount <= 0 ||
		e.SelectionInputTokens < e.BasePromptCount ||
		e.KVReservationInputTokens < e.BasePromptCount ||
		decodeShapeInvalid(e.BasePromptCount, e.DecodeSequences) ||
		e.InputEstimateConfidence > InputEstimateConfidenceConservative {
		return fmt.Errorf("request estimate is invalid")
	}
	inputCapacity, ok := requestWorkMultiply(
		e.MaximumSequenceInputTokens,
		e.BasePromptCount,
	)
	if !ok {
		return fmt.Errorf("request estimate input distribution overflows")
	}
	if e.SelectionInputTokens > inputCapacity {
		return fmt.Errorf("request estimate aggregate input exceeds sequence maximum")
	}
	kvCapacity, ok := requestWorkMultiply(
		e.MaximumSequenceKVReservationInputTokens,
		e.BasePromptCount,
	)
	if !ok {
		return fmt.Errorf("request estimate KV distribution overflows")
	}
	if e.KVReservationInputTokens > kvCapacity {
		return fmt.Errorf("request estimate aggregate KV exceeds sequence maximum")
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

type PrefillExecution uint8

const (
	PrefillExecutionIndependentSequences PrefillExecution = iota + 1
	PrefillExecutionPageAlignedPrecache
)

func (e PrefillExecution) String() string {
	switch e {
	case PrefillExecutionIndependentSequences:
		return "independent_sequences"
	case PrefillExecutionPageAlignedPrecache:
		return "page_aligned_precache"
	default:
		return "invalid"
	}
}

type InputKVSharing uint8

const (
	InputKVSharingIndependentSequences InputKVSharing = iota + 1
	InputKVSharingPageAlignedPrefix
)

func (s InputKVSharing) String() string {
	switch s {
	case InputKVSharingIndependentSequences:
		return "independent_sequences"
	case InputKVSharingPageAlignedPrefix:
		return "page_aligned_prefix"
	default:
		return "invalid"
	}
}

type FirstByteCoverage uint8

const (
	FirstByteCoverageOneSequence FirstByteCoverage = iota + 1
	FirstByteCoveragePageAlignedSinglePrompt
)

func (c FirstByteCoverage) String() string {
	switch c {
	case FirstByteCoverageOneSequence:
		return "one_sequence"
	case FirstByteCoveragePageAlignedSinglePrompt:
		return "page_aligned_single_prompt"
	default:
		return "invalid"
	}
}

// BackendExecutionProfile contains backend protocol properties only. It is
// immutable after startup and contains no model identity or learned values.
type BackendExecutionProfile struct {
	PrefillExecution  PrefillExecution
	InputKVSharing    InputKVSharing
	FirstByteCoverage FirstByteCoverage
}

func (p BackendExecutionProfile) Validate() error {
	if p.PrefillExecution != PrefillExecutionIndependentSequences &&
		p.PrefillExecution != PrefillExecutionPageAlignedPrecache {
		return fmt.Errorf("backend execution profile is invalid")
	}
	if p.InputKVSharing != InputKVSharingIndependentSequences &&
		p.InputKVSharing != InputKVSharingPageAlignedPrefix {
		return fmt.Errorf("backend execution profile is invalid")
	}
	if p.FirstByteCoverage != FirstByteCoverageOneSequence &&
		p.FirstByteCoverage != FirstByteCoveragePageAlignedSinglePrompt {
		return fmt.Errorf("backend execution profile is invalid")
	}
	return nil
}

// RequestWork is the immutable, block-aligned form consumed by admission. It
// is derived from one RequestEstimate and the Controller's immutable block
// size; clients never provide pre-rounded KV values.
type RequestWork struct {
	Estimate                             RequestEstimate
	PrefillInputTokens                   int64
	PrefillComputeTokens                 int64
	FirstBytePendingPrefillInputTokens   int64
	FirstBytePendingPrefillComputeTokens int64
	FirstBytePendingPrefillSequences     int64
	InputKVTokens                        int64
	FirstByteCoverableInputKVTokens      int64
	FirstBytePendingInputKVTokens        int64
	TotalKVTokens                        int64
	FutureKVTokens                       int64
}

func (w RequestWork) Validate() error {
	if err := w.Estimate.Validate(); err != nil {
		return err
	}
	if w.PrefillInputTokens <= 0 || w.PrefillComputeTokens <= 0 ||
		w.PrefillComputeTokens > w.PrefillInputTokens ||
		w.FirstBytePendingPrefillInputTokens < 0 ||
		w.FirstBytePendingPrefillInputTokens > w.PrefillInputTokens ||
		w.FirstBytePendingPrefillComputeTokens < 0 ||
		w.FirstBytePendingPrefillComputeTokens > w.FirstBytePendingPrefillInputTokens ||
		w.FirstBytePendingPrefillComputeTokens > w.PrefillComputeTokens ||
		w.FirstBytePendingPrefillSequences < 0 ||
		w.FirstBytePendingPrefillSequences >= w.Estimate.DecodeSequences ||
		(w.FirstBytePendingPrefillInputTokens == 0) != (w.FirstBytePendingPrefillSequences == 0) ||
		w.InputKVTokens <= 0 || w.FirstByteCoverableInputKVTokens <= 0 ||
		w.FirstBytePendingInputKVTokens < 0 || w.FutureKVTokens < 0 ||
		w.TotalKVTokens <= 0 {
		return fmt.Errorf("request work is invalid")
	}
	inputKV, ok := requestWorkAdd(
		w.FirstByteCoverableInputKVTokens,
		w.FirstBytePendingInputKVTokens,
	)
	if !ok || inputKV != w.InputKVTokens {
		return fmt.Errorf("request work input KV decomposition is invalid")
	}
	totalKV, ok := requestWorkAdd(w.InputKVTokens, w.FutureKVTokens)
	if !ok || totalKV != w.TotalKVTokens {
		return fmt.Errorf("request work total KV decomposition is invalid")
	}
	return nil
}

func BuildRequestWork(
	estimate RequestEstimate,
	profile BackendExecutionProfile,
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
	prefillInput, pendingPrefillInput, ok := requestWorkPrefill(
		estimate,
		profile,
		blockSize,
	)
	if !ok {
		return RequestWork{}, fmt.Errorf("request work Prefill input is invalid")
	}
	inputKV, coverableInputKV, pendingInputKV, ok := requestWorkInputKV(
		estimate,
		profile,
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
		futureKV, valid = requestWorkMultiply(fullSequenceFuture, estimate.DecodeSequences)
		if !valid {
			return RequestWork{}, fmt.Errorf("request work future KV is invalid")
		}
	}
	if inputKV > math.MaxInt64-futureKV {
		return RequestWork{}, fmt.Errorf("request work total KV is invalid")
	}
	totalKV := inputKV + futureKV
	work := RequestWork{
		Estimate:                             estimate,
		PrefillInputTokens:                   prefillInput,
		PrefillComputeTokens:                 prefillInput,
		FirstBytePendingPrefillInputTokens:   pendingPrefillInput,
		FirstBytePendingPrefillComputeTokens: pendingPrefillInput,
		FirstBytePendingPrefillSequences:     requestWorkPendingPrefillSequences(estimate, pendingPrefillInput),
		InputKVTokens:                        inputKV,
		FirstByteCoverableInputKVTokens:      coverableInputKV,
		FirstBytePendingInputKVTokens:        pendingInputKV,
		TotalKVTokens:                        totalKV,
		FutureKVTokens:                       futureKV,
	}
	if err := work.Validate(); err != nil {
		return RequestWork{}, err
	}
	return work, nil
}

func requestWorkPrefill(
	estimate RequestEstimate,
	profile BackendExecutionProfile,
	blockSize int64,
) (total, pendingAfterFirstByte int64, ok bool) {
	fanout := estimate.DecodeSequences / estimate.BasePromptCount
	switch profile.PrefillExecution {
	case PrefillExecutionIndependentSequences:
		total, ok = requestWorkMultiply(estimate.SelectionInputTokens, fanout)
		if !ok {
			return 0, 0, false
		}
		if estimate.DecodeSequences > 1 {
			if estimate.BasePromptCount == 1 {
				pendingAfterFirstByte = total - estimate.SelectionInputTokens
			} else {
				pendingAfterFirstByte = total - 1
			}
		}
		return total, pendingAfterFirstByte, true
	case PrefillExecutionPageAlignedPrecache:
		if fanout == 1 {
			total = estimate.SelectionInputTokens
			if estimate.DecodeSequences > 1 {
				return total, total - 1, true
			}
			return total, 0, true
		}
		tailCapacity, valid := requestWorkMultiply(estimate.BasePromptCount, blockSize-1)
		if !valid {
			return 0, 0, false
		}
		tailUpper := estimate.SelectionInputTokens
		if tailUpper > tailCapacity {
			tailUpper = tailCapacity
		}
		expandedTail, valid := requestWorkMultiply(tailUpper, fanout)
		if !valid || estimate.SelectionInputTokens > math.MaxInt64-expandedTail {
			return 0, 0, false
		}
		total = estimate.SelectionInputTokens + expandedTail
		if profile.FirstByteCoverage == FirstByteCoveragePageAlignedSinglePrompt &&
			estimate.BasePromptCount == 1 {
			pendingAfterFirstByte, valid = requestWorkMultiply(tailUpper, fanout-1)
			return total, pendingAfterFirstByte, valid
		}
		return total, total - 1, true
	default:
		return 0, 0, false
	}
}

func requestWorkInputKV(
	estimate RequestEstimate,
	profile BackendExecutionProfile,
	blockSize int64,
) (total, coverableAfterFirstByte, pendingAfterFirstByte int64, ok bool) {
	baseInput, valid := requestWorkRoundUpAcrossSequences(
		estimate.KVReservationInputTokens,
		estimate.MaximumSequenceKVReservationInputTokens,
		estimate.BasePromptCount,
		blockSize,
	)
	if !valid {
		return 0, 0, 0, false
	}
	fanout := estimate.DecodeSequences / estimate.BasePromptCount
	switch profile.InputKVSharing {
	case InputKVSharingIndependentSequences:
		replicatedInput, valid := requestWorkMultiply(estimate.KVReservationInputTokens, fanout)
		if !valid {
			return 0, 0, 0, false
		}
		total, valid = requestWorkRoundUpAcrossSequences(
			replicatedInput,
			estimate.MaximumSequenceKVReservationInputTokens,
			estimate.DecodeSequences,
			blockSize,
		)
		if !valid {
			return 0, 0, 0, false
		}
		if estimate.DecodeSequences == 1 || estimate.BasePromptCount == 1 {
			coverableAfterFirstByte = baseInput
		} else {
			coverableAfterFirstByte = blockSize
		}
	case InputKVSharingPageAlignedPrefix:
		extraTailBlocks, valid := requestWorkMultiply(estimate.BasePromptCount, fanout-1)
		if !valid {
			return 0, 0, 0, false
		}
		extraTailKV, valid := requestWorkMultiply(extraTailBlocks, blockSize)
		if !valid || baseInput > math.MaxInt64-extraTailKV {
			return 0, 0, 0, false
		}
		total = baseInput + extraTailKV
		if estimate.DecodeSequences == 1 ||
			(profile.FirstByteCoverage == FirstByteCoveragePageAlignedSinglePrompt &&
				estimate.BasePromptCount == 1) {
			coverableAfterFirstByte = baseInput
		} else {
			coverableAfterFirstByte = blockSize
		}
	default:
		return 0, 0, 0, false
	}
	if coverableAfterFirstByte <= 0 || coverableAfterFirstByte > total {
		return 0, 0, 0, false
	}
	return total, coverableAfterFirstByte, total - coverableAfterFirstByte, true
}

func requestWorkPendingPrefillSequences(estimate RequestEstimate, pendingTokens int64) int64 {
	if pendingTokens <= 0 {
		return 0
	}
	return estimate.DecodeSequences - 1
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

func requestWorkAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func requestWorkRoundUp(value, blockSize int64) (int64, bool) {
	if value <= 0 || blockSize <= 0 || value > math.MaxInt64-(blockSize-1) {
		return 0, false
	}
	return ((value + blockSize - 1) / blockSize) * blockSize, true
}
