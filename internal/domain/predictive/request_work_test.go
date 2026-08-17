package predictive

import (
	"math"
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
	if work.InputKVTokens != 192 || work.FutureKVTokens != 256+3*320 ||
		work.TotalKVTokens != 192+256+3*320 || work.Estimate != estimate {
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
	independent, err := BuildRequestWork(estimate, RequestWorkProfile{
		InputAccounting: InputAccountingDecodeSequences,
	}, 64)
	if err != nil {
		t.Fatal(err)
	}
	if shared.PrefillInputTokens != 100 || shared.InputKVTokens != 128 ||
		independent.PrefillInputTokens != 200 || independent.InputKVTokens != 256 {
		t.Fatalf("input fanout shared=%+v independent=%+v", shared, independent)
	}
}

func TestBuildRequestWorkRejectsInvalidAndOverflowingInputs(t *testing.T) {
	tests := []struct {
		estimate  RequestEstimate
		blockSize int64
	}{
		{},
		{estimate: RequestEstimate{SelectionInputTokens: 2, MaximumSequenceInputTokens: 2, KVReservationInputTokens: 1, DecodeSequences: 1}, blockSize: 64},
		{estimate: RequestEstimate{SelectionInputTokens: 1, MaximumSequenceInputTokens: 1, KVReservationInputTokens: 1, DecodeHorizonTokens: -1, DecodeSequences: 1}, blockSize: 64},
		{estimate: RequestEstimate{SelectionInputTokens: 1, MaximumSequenceInputTokens: 1, KVReservationInputTokens: math.MaxInt64, DecodeHorizonTokens: 1, DecodeSequences: 1}, blockSize: 64},
		{estimate: RequestEstimate{SelectionInputTokens: 1, MaximumSequenceInputTokens: 1, KVReservationInputTokens: math.MaxInt64 - 62, DecodeSequences: 1}, blockSize: 64},
		{estimate: RequestEstimate{SelectionInputTokens: 1, MaximumSequenceInputTokens: 1, KVReservationInputTokens: 1, DecodeSequences: 1}},
		{estimate: RequestEstimate{SelectionInputTokens: 1, MaximumSequenceInputTokens: 1, KVReservationInputTokens: 1, DecodeHorizonTokens: math.MaxInt64, DecodeSequences: 2}, blockSize: 64},
	}
	for index, test := range tests {
		if work, err := BuildRequestWork(test.estimate, basePromptWorkProfile(), test.blockSize); err == nil {
			t.Fatalf("case %d accepted work=%+v", index, work)
		}
	}
}

func basePromptWorkProfile() RequestWorkProfile {
	return RequestWorkProfile{InputAccounting: InputAccountingBasePrompts}
}
