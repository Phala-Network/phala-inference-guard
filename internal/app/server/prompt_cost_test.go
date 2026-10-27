package server

import (
	"math"
	"testing"

	apprequest "github.com/Phala-Network/phala-inference-guard/internal/app/request"
)

func testPrompt(blocks ...uint64) apprequest.PromptFingerprint {
	return apprequest.PromptFingerprint{Blocks: blocks, Bytes: int64(len(blocks)) * apprequest.PromptBlockBytes}
}

func TestPromptCostPricesColdPromptInFull(t *testing.T) {
	estimator := newPromptCostEstimator(1 << 20)
	cost := estimator.Estimate(testPrompt(1, 2, 3, 4), 0)
	want := int64(4 * apprequest.PromptBlockBytes / defaultPromptBytesPerToken)
	if !cost.known || cost.uncachedTokens != want {
		t.Fatalf("cost=%+v want %d", cost, want)
	}
}

func TestPromptCostPricesContinuationAtItsSuffix(t *testing.T) {
	estimator := newPromptCostEstimator(1 << 20)
	estimator.Admitted(testPrompt(1, 2, 3))
	cost := estimator.Estimate(testPrompt(1, 2, 3, 4), 0)
	want := int64(apprequest.PromptBlockBytes / defaultPromptBytesPerToken)
	if cost.uncachedTokens != want {
		t.Fatalf("continuation cost=%d want %d", cost.uncachedTokens, want)
	}
	if cold := estimator.Estimate(testPrompt(9, 2, 3, 4), 0); cold.uncachedTokens != 4*want {
		t.Fatalf("a different first block must price the Prompt cold, got %d", cold.uncachedTokens)
	}
}

func TestPromptCostEvictsLeastRecentlyAdmitted(t *testing.T) {
	// Capacity of 3 blocks at 4 bytes per token.
	estimator := newPromptCostEstimator(3 * apprequest.PromptBlockBytes / 4)
	estimator.Admitted(testPrompt(1, 2))
	estimator.Admitted(testPrompt(7, 8))
	// Block 1 was evicted first, so the whole Prompt is cold again.
	if cost := estimator.Estimate(testPrompt(1, 2), 0); cost.uncachedTokens != 512 {
		t.Fatalf("least recently admitted prefix was not evicted: cost=%d", cost.uncachedTokens)
	}
	if cost := estimator.Estimate(testPrompt(7, 8), 0); cost.uncachedTokens != 0 {
		t.Fatalf("recent prefix was evicted: cost=%d", cost.uncachedTokens)
	}
}

func TestPromptCostLearnsTokenRatioAndCorrection(t *testing.T) {
	estimator := newPromptCostEstimator(1 << 20)
	prompt := testPrompt(1, 2, 3, 4) // 4 KiB
	for i := 0; i < 50; i++ {
		cost := estimator.Estimate(prompt, 0)
		// The backend says 2,048 Prompt tokens (2 bytes/token), half cached.
		estimator.Learn(prompt, cost, 2048, 1024)
	}
	snapshot := estimator.Snapshot()
	if math.Abs(snapshot.bytesPerToken-2) > 0.01 {
		t.Fatalf("bytes per token=%.3f want 2", snapshot.bytesPerToken)
	}
	cost := estimator.Estimate(prompt, 0)
	if math.Abs(float64(cost.uncachedTokens)-1024) > 30 {
		t.Fatalf("corrected estimate=%d want about 1024", cost.uncachedTokens)
	}
}

func TestPromptCostMeasuresWarmAdmissions(t *testing.T) {
	estimator := newPromptCostEstimator(1 << 20)
	prompt := testPrompt(1, 2)
	cost := estimator.Estimate(prompt, 0)
	estimator.Learn(prompt, cost, 20_000, 0)
	if warm := estimator.Snapshot().warm; warm.learnedRequests != 0 {
		t.Fatalf("a request not admitted as warm was measured as warm: %+v", warm)
	}
	estimator.Learn(prompt, cost.AdmittedAsWarm(8192), 20_000, 15_000)
	estimator.Learn(prompt, cost.AdmittedAsWarm(8192), 20_000, 2_000)
	warm := estimator.Snapshot().warm
	if warm.learnedRequests != 2 || warm.uncachedTokens != 23_000 || warm.overLimit != 1 {
		t.Fatalf("warm outcomes=%+v want 2 requests, 23000 tokens, 1 over the limit", warm)
	}
}

func TestPromptCostWithoutFingerprintIsUnknown(t *testing.T) {
	if cost := newPromptCostEstimator(0).Estimate(apprequest.PromptFingerprint{}, 0); cost.known {
		t.Fatal("empty fingerprint produced a cost")
	}
}

func TestPromptCostWithoutUsableCapacityDoesNotRetainPrefixes(t *testing.T) {
	for _, capacity := range []int64{-1, 0, 1} {
		estimator := newPromptCostEstimator(capacity)
		for block := uint64(1); block <= 100; block++ {
			estimator.Admitted(testPrompt(block))
		}
		if snapshot := estimator.Snapshot(); snapshot.indexBlocks != 0 {
			t.Fatalf("capacity=%d retained %d prefixes without a usable bound", capacity, snapshot.indexBlocks)
		}
		if cost := estimator.Estimate(testPrompt(100), 0); !cost.known || cost.uncachedTokens != 256 {
			t.Fatalf("capacity=%d must price an unretained Prompt cold: %+v", capacity, cost)
		}
	}
}
