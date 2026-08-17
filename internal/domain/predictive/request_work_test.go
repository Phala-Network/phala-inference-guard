package predictive

import (
	"math"
	"strings"
	"testing"
)

func TestBuildRequestWorkCarriesOneCompleteEstimate(t *testing.T) {
	estimate := RequestEstimate{
		SelectionInputTokens:                    1_298,
		MaximumSequenceInputTokens:              1_298,
		KVReservationInputTokens:                2_501,
		MaximumSequenceKVReservationInputTokens: 2_501,
		DecodeHorizonTokens:                     256,
		BasePromptCount:                         1,
		DecodeSequences:                         1,
	}
	work, err := BuildRequestWork(estimate, basePromptWorkProfile(), 64)
	if err != nil {
		t.Fatal(err)
	}
	if work.Estimate != estimate || work.InputKVTokens != 2_560 ||
		work.TotalKVTokens != 2_816 || work.FutureKVTokens != 256 {
		t.Fatalf("request work=%+v", work)
	}
}

func TestBuildRequestWorkUsesMarginalFutureBlocks(t *testing.T) {
	estimate := RequestEstimate{
		SelectionInputTokens:                    500,
		MaximumSequenceInputTokens:              500,
		KVReservationInputTokens:                513,
		MaximumSequenceKVReservationInputTokens: 513,
		DecodeHorizonTokens:                     64,
		BasePromptCount:                         1,
		DecodeSequences:                         1,
	}
	work, err := BuildRequestWork(estimate, basePromptWorkProfile(), 64)
	if err != nil {
		t.Fatal(err)
	}
	if work.InputKVTokens != 576 || work.TotalKVTokens != 640 || work.FutureKVTokens != 64 {
		t.Fatalf("request work=%+v", work)
	}
}

func TestV01215ApproximateInputBlockPhaseCannotReduceFutureKV(t *testing.T) {
	estimate := RequestEstimate{
		SelectionInputTokens:                    456,
		MaximumSequenceInputTokens:              456,
		KVReservationInputTokens:                513,
		MaximumSequenceKVReservationInputTokens: 513,
		DecodeHorizonTokens:                     2,
		BasePromptCount:                         1,
		DecodeSequences:                         1,
	}
	work, err := BuildRequestWork(estimate, basePromptWorkProfile(), 64)
	if err != nil {
		t.Fatal(err)
	}
	if work.FutureKVTokens != 64 || work.TotalKVTokens != 640 {
		t.Fatalf("approximate input phase reduced future KV: %+v", work)
	}
}

func TestV01215SGLangApproximateUpperBoundRetainsPossibleChildTailPages(t *testing.T) {
	build := func(t *testing.T, input int64) RequestWork {
		t.Helper()
		work, err := BuildRequestWork(RequestEstimate{
			SelectionInputTokens:                    input,
			MaximumSequenceInputTokens:              input,
			KVReservationInputTokens:                input,
			MaximumSequenceKVReservationInputTokens: input,
			DecodeHorizonTokens:                     1,
			BasePromptCount:                         1,
			DecodeSequences:                         2,
		}, basePromptWorkProfile(), 64)
		if err != nil {
			t.Fatal(err)
		}
		return work
	}

	aligned := build(t, 64)
	unaligned := build(t, 63)
	if aligned.PrefillInputTokens != 190 || aligned.TotalKVTokens != 256 {
		t.Fatalf("approximate block-boundary upper bound lost a possible child tail: %+v", aligned)
	}
	if unaligned.PrefillInputTokens != 189 || unaligned.TotalKVTokens != 256 {
		t.Fatalf("unaligned child tail was not retained: %+v", unaligned)
	}
}

func TestV01215BuildRequestWorkChargesEveryDecodeSequence(t *testing.T) {
	estimate := RequestEstimate{
		SelectionInputTokens:                    128,
		MaximumSequenceInputTokens:              128,
		KVReservationInputTokens:                130,
		MaximumSequenceKVReservationInputTokens: 130,
		DecodeHorizonTokens:                     257,
		BasePromptCount:                         1,
		DecodeSequences:                         4,
	}
	work, err := BuildRequestWork(estimate, basePromptWorkProfile(), 64)
	if err != nil {
		t.Fatal(err)
	}
	if work.InputKVTokens != 384 || work.FutureKVTokens != 4*320 ||
		work.TotalKVTokens != 384+4*320 || work.Estimate != estimate {
		t.Fatalf("multi-sequence request work=%+v", work)
	}
}

func TestV01215BuildRequestWorkRoundsInputKVPerBasePrompt(t *testing.T) {
	estimate := RequestEstimate{
		SelectionInputTokens:                    2,
		MaximumSequenceInputTokens:              1,
		KVReservationInputTokens:                2,
		MaximumSequenceKVReservationInputTokens: 1,
		BasePromptCount:                         2,
		DecodeSequences:                         2,
	}
	work, err := BuildRequestWork(estimate, basePromptWorkProfile(), 64)
	if err != nil {
		t.Fatal(err)
	}
	if work.InputKVTokens != 128 || work.TotalKVTokens != 128 {
		t.Fatalf("base-prompt input blocks were pooled across sequences: %+v", work)
	}
}

func TestV01215BuildRequestWorkRoundsDecodeKVPerBasePrompt(t *testing.T) {
	estimate := RequestEstimate{
		SelectionInputTokens:                    126,
		MaximumSequenceInputTokens:              63,
		KVReservationInputTokens:                126,
		MaximumSequenceKVReservationInputTokens: 63,
		DecodeHorizonTokens:                     2,
		BasePromptCount:                         2,
		DecodeSequences:                         2,
	}
	work, err := BuildRequestWork(estimate, basePromptWorkProfile(), 64)
	if err != nil {
		t.Fatal(err)
	}
	if work.InputKVTokens != 128 || work.FutureKVTokens != 128 || work.TotalKVTokens != 256 {
		t.Fatalf("base-prompt Decode tails were pooled across sequences: %+v", work)
	}
}

func TestV01215BuildRequestWorkProfilesBackendInputFanout(t *testing.T) {
	estimate := RequestEstimate{
		SelectionInputTokens:                    100,
		MaximumSequenceInputTokens:              100,
		KVReservationInputTokens:                100,
		MaximumSequenceKVReservationInputTokens: 100,
		BasePromptCount:                         1,
		DecodeSequences:                         2,
	}
	shared, err := BuildRequestWork(estimate, basePromptWorkProfile(), 64)
	if err != nil {
		t.Fatal(err)
	}
	independent, err := BuildRequestWork(estimate, BackendExecutionProfile{
		PrefillExecution:  PrefillExecutionIndependentSequences,
		InputKVSharing:    InputKVSharingIndependentSequences,
		FirstByteCoverage: FirstByteCoverageOneSequence,
	}, 64)
	if err != nil {
		t.Fatal(err)
	}
	if shared.PrefillInputTokens != 226 || shared.InputKVTokens != 192 ||
		independent.PrefillInputTokens != 200 || independent.InputKVTokens != 256 {
		t.Fatalf("input fanout shared=%+v independent=%+v", shared, independent)
	}
}

func TestBuildRequestWorkRejectsInvalidAndOverflowingInputs(t *testing.T) {
	tests := []struct {
		name      string
		estimate  RequestEstimate
		profile   BackendExecutionProfile
		blockSize int64
		want      string
	}{
		{name: "zero estimate", profile: basePromptWorkProfile(), blockSize: 64, want: "request estimate is invalid"},
		{
			name: "reservation below selection",
			estimate: RequestEstimate{
				SelectionInputTokens: 2, MaximumSequenceInputTokens: 2,
				KVReservationInputTokens: 1, MaximumSequenceKVReservationInputTokens: 1,
				BasePromptCount: 1, DecodeSequences: 1,
			},
			profile: basePromptWorkProfile(), blockSize: 64, want: "request estimate is invalid",
		},
		{
			name: "negative Decode horizon",
			estimate: RequestEstimate{
				SelectionInputTokens: 1, MaximumSequenceInputTokens: 1,
				KVReservationInputTokens: 1, MaximumSequenceKVReservationInputTokens: 1,
				DecodeHorizonTokens: -1, BasePromptCount: 1, DecodeSequences: 1,
			},
			profile: basePromptWorkProfile(), blockSize: 64, want: "request estimate is invalid",
		},
		{
			name: "aggregate KV overflow",
			estimate: RequestEstimate{
				SelectionInputTokens: 1, MaximumSequenceInputTokens: 1,
				KVReservationInputTokens: math.MaxInt64, MaximumSequenceKVReservationInputTokens: math.MaxInt64,
				DecodeHorizonTokens: 1, BasePromptCount: 1, DecodeSequences: 1,
			},
			profile: basePromptWorkProfile(), blockSize: 64, want: "aggregate KV overflows",
		},
		{
			name: "input block rounding overflow",
			estimate: RequestEstimate{
				SelectionInputTokens: 1, MaximumSequenceInputTokens: 1,
				KVReservationInputTokens: math.MaxInt64 - 62, MaximumSequenceKVReservationInputTokens: math.MaxInt64 - 62,
				BasePromptCount: 1, DecodeSequences: 1,
			},
			profile: basePromptWorkProfile(), blockSize: 64, want: "input KV is invalid",
		},
		{
			name: "zero block size",
			estimate: RequestEstimate{
				SelectionInputTokens: 1, MaximumSequenceInputTokens: 1,
				KVReservationInputTokens: 1, MaximumSequenceKVReservationInputTokens: 1,
				BasePromptCount: 1, DecodeSequences: 1,
			},
			profile: basePromptWorkProfile(), want: "block size is invalid",
		},
		{
			name: "Decode demand overflow",
			estimate: RequestEstimate{
				SelectionInputTokens: 1, MaximumSequenceInputTokens: 1,
				KVReservationInputTokens: 1, MaximumSequenceKVReservationInputTokens: 1,
				DecodeHorizonTokens: math.MaxInt64, BasePromptCount: 1, DecodeSequences: 2,
			},
			profile: basePromptWorkProfile(), blockSize: 64, want: "per-sequence context overflows",
		},
		{
			name: "invalid work profile",
			estimate: RequestEstimate{
				SelectionInputTokens: 1, MaximumSequenceInputTokens: 1,
				KVReservationInputTokens: 1, MaximumSequenceKVReservationInputTokens: 1,
				BasePromptCount: 1, DecodeSequences: 1,
			},
			blockSize: 64, want: "backend execution profile is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			work, err := BuildRequestWork(test.estimate, test.profile, test.blockSize)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("work=%+v error=%v, want error containing %q", work, err, test.want)
			}
		})
	}
}

func TestV01215RequestEstimateRejectsImpossibleAggregateMaximumPairs(t *testing.T) {
	tests := []RequestEstimate{
		{
			SelectionInputTokens: 1_000, MaximumSequenceInputTokens: 100,
			KVReservationInputTokens: 1_000, MaximumSequenceKVReservationInputTokens: 500,
			BasePromptCount: 2, DecodeSequences: 2,
		},
		{
			SelectionInputTokens: 200, MaximumSequenceInputTokens: 100,
			KVReservationInputTokens: 1_000, MaximumSequenceKVReservationInputTokens: 100,
			BasePromptCount: 2, DecodeSequences: 2,
		},
	}
	for _, estimate := range tests {
		if work, err := BuildRequestWork(estimate, basePromptWorkProfile(), 64); err == nil {
			t.Fatalf("impossible aggregate/maximum pair accepted: estimate=%+v work=%+v", estimate, work)
		}
	}
}

func basePromptWorkProfile() BackendExecutionProfile {
	return BackendExecutionProfile{
		PrefillExecution:  PrefillExecutionPageAlignedPrecache,
		InputKVSharing:    InputKVSharingPageAlignedPrefix,
		FirstByteCoverage: FirstByteCoveragePageAlignedSinglePrompt,
	}
}
