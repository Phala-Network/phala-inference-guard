package predictive

import (
	"math"
	"testing"
)

func TestBuildRequestWorkCarriesOneCompleteEstimate(t *testing.T) {
	estimate := RequestEstimate{
		SelectionInputTokens:       1_298,
		MaximumSequenceInputTokens: 1_298,
		KVReservationInputTokens:   2_501,
		DecodeHorizonTokens:        256,
		DecodeSequences:            1,
	}
	work, err := BuildRequestWork(estimate, 64)
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
		SelectionInputTokens:       500,
		MaximumSequenceInputTokens: 500,
		KVReservationInputTokens:   513,
		DecodeHorizonTokens:        64,
		DecodeSequences:            1,
	}
	work, err := BuildRequestWork(estimate, 64)
	if err != nil {
		t.Fatal(err)
	}
	if work.InputKVTokens != 576 || work.TotalKVTokens != 640 || work.FutureKVTokens != 64 {
		t.Fatalf("request work=%+v", work)
	}
}

func TestV01215BuildRequestWorkChargesEveryDecodeSequence(t *testing.T) {
	estimate := RequestEstimate{
		SelectionInputTokens:       128,
		MaximumSequenceInputTokens: 128,
		KVReservationInputTokens:   130,
		DecodeHorizonTokens:        257,
		DecodeSequences:            4,
	}
	work, err := BuildRequestWork(estimate, 64)
	if err != nil {
		t.Fatal(err)
	}
	if work.InputKVTokens != 192 || work.FutureKVTokens != 256+3*320 ||
		work.TotalKVTokens != 192+256+3*320 || work.Estimate != estimate {
		t.Fatalf("multi-sequence request work=%+v", work)
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
		if work, err := BuildRequestWork(test.estimate, test.blockSize); err == nil {
			t.Fatalf("case %d accepted work=%+v", index, work)
		}
	}
}
