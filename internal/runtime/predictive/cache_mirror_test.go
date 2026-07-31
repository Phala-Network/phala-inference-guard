package predictive

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestCacheMirrorUsesOnlyActiveFullBlocksAsCertainHits(t *testing.T) {
	mirror := newTestCacheMirror(t, 8, 4)
	tokens := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	cold, err := mirror.BeginRequest("first", tokens)
	if err != nil {
		t.Fatalf("begin cold request failed: %v", err)
	}
	assertHitInterval(t, cold, domain.CacheHitInterval{})
	if snapshot := mirror.Snapshot(); snapshot.PendingBlocks != 2 || snapshot.ActiveBlocks != 0 {
		t.Fatalf("cold snapshot = %+v, want two pending full blocks", snapshot)
	}

	pending, err := mirror.Estimate(tokens)
	if err != nil {
		t.Fatalf("estimate pending request failed: %v", err)
	}
	assertHitInterval(t, pending, domain.CacheHitInterval{})
	if !mirror.MarkPrefillComplete("first") {
		t.Fatal("prefill completion was not applied")
	}

	hot, err := mirror.BeginRequest("second", tokens)
	if err != nil {
		t.Fatalf("begin hot request failed: %v", err)
	}
	assertHitInterval(t, hot, domain.CacheHitInterval{
		Certain:  8,
		Lower:    8,
		Expected: 8,
		Upper:    8,
	})
	if !mirror.CompleteRequest("first") {
		t.Fatal("first completion was not applied")
	}
	afterActiveRelease, err := mirror.Estimate(tokens)
	if err != nil {
		t.Fatalf("estimate after active release failed: %v", err)
	}
	assertHitInterval(t, afterActiveRelease, domain.CacheHitInterval{})
}

func TestCacheMirrorTreatsCompletedBlocksAsExpectedNotCertain(t *testing.T) {
	mirror := newTestCacheMirror(t, 8, 4)
	tokens := []int64{1, 2, 3, 4, 5, 6, 7, 8}

	if _, err := mirror.BeginRequest("first", tokens); err != nil {
		t.Fatalf("begin failed: %v", err)
	}
	if !mirror.MarkPrefillComplete("first") || !mirror.CompleteRequest("first") {
		t.Fatal("request lifecycle was not applied")
	}

	hit, err := mirror.Estimate(tokens)
	if err != nil {
		t.Fatalf("estimate failed: %v", err)
	}
	assertHitInterval(t, hit, domain.CacheHitInterval{
		Expected: 8,
		Upper:    8,
	})
	if snapshot := mirror.Snapshot(); snapshot.ProbableBlocks != 2 || snapshot.ActiveBlocks != 0 {
		t.Fatalf("completed snapshot = %+v, want two probable blocks", snapshot)
	}
}

func TestCacheMirrorBlockKeysAreChainedAfterTokenDifference(t *testing.T) {
	mirror := newTestCacheMirror(t, 16, 4)
	base := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	if _, err := mirror.BeginRequest("base", base); err != nil {
		t.Fatalf("begin base failed: %v", err)
	}
	if !mirror.MarkPrefillComplete("base") {
		t.Fatal("base prefill completion was not applied")
	}

	firstBlockChanged := []int64{1, 2, 99, 4, 5, 6, 7, 8}
	firstHit, err := mirror.Estimate(firstBlockChanged)
	if err != nil {
		t.Fatalf("first-block estimate failed: %v", err)
	}
	assertHitInterval(t, firstHit, domain.CacheHitInterval{})

	secondBlockChanged := []int64{1, 2, 3, 4, 5, 6, 99, 8}
	secondHit, err := mirror.Estimate(secondBlockChanged)
	if err != nil {
		t.Fatalf("second-block estimate failed: %v", err)
	}
	assertHitInterval(t, secondHit, domain.CacheHitInterval{
		Certain:  4,
		Lower:    4,
		Expected: 4,
		Upper:    4,
	})
}

func TestCacheMirrorAcceptsOpaqueAnalyzedBlocksWithoutTokenIDs(t *testing.T) {
	mirror := newTestCacheMirror(t, 8, 4)
	analysis := testTokenBlockAnalysis(10, testBlockDigest(1), testBlockDigest(2))

	cold, err := mirror.BeginAnalyzedRequest("first", analysis)
	if err != nil {
		t.Fatalf("begin analyzed request failed: %v", err)
	}
	assertHitInterval(t, cold, domain.CacheHitInterval{})
	if !mirror.MarkPrefillComplete("first") {
		t.Fatal("analyzed request prefill completion was not applied")
	}

	hot, err := mirror.BeginAnalyzedRequest("second", analysis)
	if err != nil {
		t.Fatalf("begin second analyzed request failed: %v", err)
	}
	assertHitInterval(t, hot, domain.CacheHitInterval{
		Certain:  8,
		Lower:    8,
		Expected: 8,
		Upper:    8,
	})
	if snapshot := mirror.Snapshot(); snapshot.Entries != 2 || snapshot.Requests != 2 {
		t.Fatalf("analyzed snapshot = %+v, want two full blocks and two requests", snapshot)
	}
}

func TestCacheMirrorTreatsPrecomputedDigestsAsAnOrderedPrefixChain(t *testing.T) {
	mirror := newTestCacheMirror(t, 8, 4)
	base := testTokenBlockAnalysis(8, testBlockDigest(1), testBlockDigest(2))
	if _, err := mirror.BeginAnalyzedRequest("base", base); err != nil {
		t.Fatalf("begin base analysis failed: %v", err)
	}
	if !mirror.MarkPrefillComplete("base") {
		t.Fatal("base prefill completion was not applied")
	}

	secondChanged := testTokenBlockAnalysis(8, testBlockDigest(1), testBlockDigest(9))
	hit, err := mirror.EstimateAnalysis(secondChanged)
	if err != nil {
		t.Fatalf("estimate changed analysis failed: %v", err)
	}
	assertHitInterval(t, hit, domain.CacheHitInterval{
		Certain:  4,
		Lower:    4,
		Expected: 4,
		Upper:    4,
	})
}

func TestCacheMirrorRejectsStaleOrMalformedAnalyzedBlocks(t *testing.T) {
	mirror := newTestCacheMirror(t, 8, 4)
	valid := testTokenBlockAnalysis(10, testBlockDigest(1), testBlockDigest(2))
	cases := []struct {
		name   string
		mutate func(*TokenBlockAnalysis)
	}{
		{name: "manifest", mutate: func(value *TokenBlockAnalysis) { value.ManifestID = "other" }},
		{name: "backend epoch", mutate: func(value *TokenBlockAnalysis) { value.BackendEpoch = "other" }},
		{name: "block size", mutate: func(value *TokenBlockAnalysis) { value.BlockSize = 8 }},
		{name: "exact count", mutate: func(value *TokenBlockAnalysis) { value.ExactInputTokens = 9 }},
		{name: "full digest", mutate: func(value *TokenBlockAnalysis) { value.FullBlockDigests[0] = CacheBlockDigest{} }},
		{name: "partial count", mutate: func(value *TokenBlockAnalysis) { value.PartialBlockTokens = 0 }},
		{name: "partial digest", mutate: func(value *TokenBlockAnalysis) { value.PartialBlockDigest = CacheBlockDigest{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			invalid := valid.Clone()
			tc.mutate(&invalid)
			if _, err := mirror.EstimateAnalysis(invalid); err == nil {
				t.Fatal("invalid analyzed blocks were accepted")
			}
		})
	}
}

func TestCacheMirrorEvictsOldestUnpinnedBlockAndStaysBounded(t *testing.T) {
	mirror := newTestCacheMirror(t, 2, 2)
	completeCachedRequest(t, mirror, "a", []int64{1, 2})
	completeCachedRequest(t, mirror, "b", []int64{3, 4})

	if _, err := mirror.Estimate([]int64{1, 2}); err != nil {
		t.Fatalf("touch a failed: %v", err)
	}
	if _, err := mirror.BeginRequest("c", []int64{5, 6}); err != nil {
		t.Fatalf("begin c failed: %v", err)
	}

	a, err := mirror.Estimate([]int64{1, 2})
	if err != nil {
		t.Fatalf("estimate a failed: %v", err)
	}
	assertHitInterval(t, a, domain.CacheHitInterval{Expected: 2, Upper: 2})
	b, err := mirror.Estimate([]int64{3, 4})
	if err != nil {
		t.Fatalf("estimate b failed: %v", err)
	}
	assertHitInterval(t, b, domain.CacheHitInterval{})
	if snapshot := mirror.Snapshot(); snapshot.Entries != 2 || snapshot.CapacityBlocks != 2 {
		t.Fatalf("bounded snapshot = %+v, want two entries", snapshot)
	}
}

func TestCacheMirrorNeverEvictsPinnedBlocks(t *testing.T) {
	mirror := newTestCacheMirror(t, 1, 2)
	if _, err := mirror.BeginRequest("pinned", []int64{1, 2}); err != nil {
		t.Fatalf("begin pinned failed: %v", err)
	}
	if _, err := mirror.BeginRequest("blocked", []int64{3, 4}); !errors.Is(err, ErrCacheMirrorCapacity) {
		t.Fatalf("capacity error = %v, want %v", err, ErrCacheMirrorCapacity)
	}
	if snapshot := mirror.Snapshot(); snapshot.Entries != 1 || snapshot.Requests != 1 || snapshot.PendingBlocks != 1 {
		t.Fatalf("pinned snapshot = %+v, want unchanged one-block mirror", snapshot)
	}
}

func TestCacheMirrorAnalyzedPreflightDoesNotEvictOrMutateSnapshot(t *testing.T) {
	mirror := newTestCacheMirror(t, 2, 2)
	completeCachedRequest(t, mirror, "a", []int64{1, 2})
	completeCachedRequest(t, mirror, "b", []int64{3, 4})
	before := mirror.Snapshot()
	analysis := TokenBlockAnalysis{
		ManifestID:       "test-profile",
		BackendEpoch:     "backend-1",
		BlockSize:        2,
		ExactInputTokens: 2,
		FullBlockDigests: []CacheBlockDigest{testBlockDigest(9)},
	}

	hits, err := mirror.PreflightAnalyzedRequest("candidate", analysis)
	if err != nil {
		t.Fatalf("preflight failed: %v", err)
	}
	assertHitInterval(t, hits, domain.CacheHitInterval{})
	if after := mirror.Snapshot(); after != before {
		t.Fatalf("preflight mutated cache state: before=%+v after=%+v", before, after)
	}
}

func TestCacheMirrorResetClearsIdentityAndResidencyEvidence(t *testing.T) {
	mirror := newTestCacheMirror(t, 4, 2)
	tokens := []int64{1, 2, 3, 4}
	if _, err := mirror.BeginRequest("active", tokens); err != nil {
		t.Fatalf("begin failed: %v", err)
	}
	if !mirror.MarkPrefillComplete("active") {
		t.Fatal("prefill completion was not applied")
	}
	if err := mirror.Reset(CacheMirrorEpoch{
		ManifestID:   "new-profile",
		BackendEpoch: "backend-2",
		BlockSize:    4,
	}); err != nil {
		t.Fatalf("reset failed: %v", err)
	}

	hit, err := mirror.Estimate(tokens)
	if err != nil {
		t.Fatalf("post-reset estimate failed: %v", err)
	}
	assertHitInterval(t, hit, domain.CacheHitInterval{})
	snapshot := mirror.Snapshot()
	if snapshot.Entries != 0 || snapshot.Requests != 0 || snapshot.ManifestID != "new-profile" || snapshot.BackendEpoch != "backend-2" {
		t.Fatalf("reset snapshot = %+v", snapshot)
	}
}

func TestCacheMirrorConcurrentSharedPrefixAccounting(t *testing.T) {
	mirror := newTestCacheMirror(t, 8, 4)
	tokens := []int64{1, 2, 3, 4, 5, 6, 7, 8}
	if _, err := mirror.BeginRequest("owner", tokens); err != nil {
		t.Fatalf("begin owner failed: %v", err)
	}
	if !mirror.MarkPrefillComplete("owner") {
		t.Fatal("owner prefill completion was not applied")
	}

	const followers = 32
	start := make(chan struct{})
	errorsByRequest := make(chan error, followers)
	var wg sync.WaitGroup
	for index := 0; index < followers; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			id := fmt.Sprintf("follower-%d", index)
			hit, err := mirror.BeginRequest(id, tokens)
			if err == nil && hit.Certain != 8 {
				err = fmt.Errorf("certain hits = %d, want 8", hit.Certain)
			}
			errorsByRequest <- err
		}(index)
	}
	close(start)
	wg.Wait()
	close(errorsByRequest)
	for err := range errorsByRequest {
		if err != nil {
			t.Fatal(err)
		}
	}
	snapshot := mirror.Snapshot()
	if snapshot.Entries != 2 || snapshot.Requests != followers+1 || snapshot.ActiveBlocks != 2 {
		t.Fatalf("concurrent snapshot = %+v", snapshot)
	}
}

func TestCacheMirrorRejectsInvalidLifecycleInputs(t *testing.T) {
	if _, err := NewCacheMirror(CacheMirrorConfig{}); err == nil {
		t.Fatal("empty configuration must fail")
	}
	if _, err := NewCacheMirror(CacheMirrorConfig{
		CapacityBlocks: 1,
		BlockSize:      4,
		ManifestID:     "test-profile",
		BackendEpoch:   "backend-1",
		HashKey:        []byte("only-sixteen-key"),
	}); err == nil {
		t.Fatal("non-32-byte cache mirror key must fail")
	}
	mirror := newTestCacheMirror(t, 4, 2)
	if _, err := mirror.BeginRequest("", []int64{1, 2}); err == nil {
		t.Fatal("empty request id must fail")
	}
	if _, err := mirror.BeginRequest("negative", []int64{1, -1}); err == nil {
		t.Fatal("negative token id must fail")
	}
	if _, err := mirror.BeginRequest("duplicate", []int64{1, 2}); err != nil {
		t.Fatalf("first duplicate request failed: %v", err)
	}
	if _, err := mirror.BeginRequest("duplicate", []int64{1, 2}); err == nil {
		t.Fatal("duplicate active request id must fail")
	}
	if mirror.MarkPrefillComplete("missing") {
		t.Fatal("unknown prefill completion must not apply")
	}
	if mirror.CompleteRequest("missing") {
		t.Fatal("unknown completion must not apply")
	}
	if err := mirror.Reset(CacheMirrorEpoch{}); err == nil {
		t.Fatal("invalid reset epoch must fail")
	}
}

func newTestCacheMirror(t *testing.T, capacity, blockSize int) *CacheMirror {
	t.Helper()
	mirror, err := NewCacheMirror(CacheMirrorConfig{
		CapacityBlocks: capacity,
		BlockSize:      blockSize,
		ManifestID:     "test-profile",
		BackendEpoch:   "backend-1",
		HashKey:        []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("new cache mirror failed: %v", err)
	}
	return mirror
}

func completeCachedRequest(t *testing.T, mirror *CacheMirror, id string, tokens []int64) {
	t.Helper()
	if _, err := mirror.BeginRequest(id, tokens); err != nil {
		t.Fatalf("begin %s failed: %v", id, err)
	}
	if !mirror.MarkPrefillComplete(id) {
		t.Fatalf("prefill completion %s was not applied", id)
	}
	if !mirror.CompleteRequest(id) {
		t.Fatalf("completion %s was not applied", id)
	}
}

func assertHitInterval(t *testing.T, got, want domain.CacheHitInterval) {
	t.Helper()
	if got != want {
		t.Fatalf("cache hit interval = %+v, want %+v", got, want)
	}
}

func testTokenBlockAnalysis(inputTokens int64, digests ...CacheBlockDigest) TokenBlockAnalysis {
	analysis := TokenBlockAnalysis{
		ManifestID:         "test-profile",
		BackendEpoch:       "backend-1",
		BlockSize:          4,
		ExactInputTokens:   inputTokens,
		FullBlockDigests:   append([]CacheBlockDigest(nil), digests...),
		PartialBlockTokens: inputTokens % 4,
	}
	if analysis.PartialBlockTokens > 0 {
		analysis.PartialBlockDigest = testBlockDigest(255)
	}
	return analysis
}

func testBlockDigest(value byte) CacheBlockDigest {
	var digest CacheBlockDigest
	digest[0] = value
	return digest
}
