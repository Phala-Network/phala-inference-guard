package predictive

import (
	"math"
	"testing"
)

func TestBuildRequestCostPreservesRollingDecodeHorizon(t *testing.T) {
	cost, err := BuildRequestCost(RequestCostInput{
		ManifestID:             "manifest",
		BlockSize:              64,
		SelectionPrefillTokens: 1_298,
		SafetyInputTokens:      2_501,
		DecodeHorizonTokens:    256,
		Confidence:             1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cost.InputTokens != 2_501 || cost.UncachedPrefillUpper != 2_501 ||
		cost.DecodeHorizonUpper != 256 || cost.ActiveContextTokensUpper != 2_757 ||
		cost.FutureContextTokensUpper != 256 || cost.KV.PhysicalKVUpper != 2_816 ||
		cost.KV.ActiveKVUpper != 2_816 || cost.FutureKV.PhysicalKVUpper != 256 ||
		cost.FutureKV.ActiveKVUpper != 256 || cost.DecodeSequencesUpper != 1 {
		t.Fatalf("request cost=%+v", cost)
	}
}

func TestBuildRequestCostPromotesSelectionAboveSafetyUpper(t *testing.T) {
	cost, err := BuildRequestCost(RequestCostInput{
		ManifestID:             "manifest",
		BlockSize:              64,
		SelectionPrefillTokens: 513,
		SafetyInputTokens:      500,
		DecodeHorizonTokens:    0,
		Confidence:             0.5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cost.InputTokens != 513 || cost.KV.PhysicalKVUpper != 576 || cost.FutureKV.PhysicalKVUpper != 0 || cost.Confidence != 0.5 {
		t.Fatalf("promoted request cost=%+v", cost)
	}
}

func TestBuildRequestCostRejectsInvalidAndOverflowingInputs(t *testing.T) {
	tests := []RequestCostInput{
		{},
		{ManifestID: "manifest", BlockSize: 64, SelectionPrefillTokens: 1, SafetyInputTokens: 1, DecodeHorizonTokens: -1, Confidence: 1},
		{ManifestID: "manifest", BlockSize: 64, SelectionPrefillTokens: 1, SafetyInputTokens: math.MaxInt64, DecodeHorizonTokens: 1, Confidence: 1},
		{ManifestID: "manifest", BlockSize: 64, SelectionPrefillTokens: 1, SafetyInputTokens: 1, Confidence: math.NaN()},
	}
	for index, input := range tests {
		if cost, err := BuildRequestCost(input); err == nil {
			t.Fatalf("case %d accepted cost=%+v", index, cost)
		}
	}
}
