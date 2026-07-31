//go:build pig_native && cgo

package server

import (
	"context"
	"path/filepath"
	"testing"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive/nativeffi"
)

func TestRealPredictiveShadowRunsGemma4RendererNativeAnalyzerAndCacheAwareCoordinator(t *testing.T) {
	renderer, err := newGemma4TextRenderer(gemma4TextRendererConfig{
		BOSToken:             "<bos>",
		DefaultDecodeHorizon: 16,
		MaximumDecodeHorizon: 256,
	})
	if err != nil {
		t.Fatalf("new Gemma4 renderer: %v", err)
	}
	analyzer, err := nativeffi.Open(nativeffi.Config{
		TokenizerPath:              filepath.Join("..", "..", "..", "native", "tokenizer", "fixtures", "ffi-wordlevel-tokenizer.json"),
		ManifestID:                 "adapter-test-manifest",
		BackendEpoch:               "adapter-test-epoch",
		BlockSize:                  4,
		Key:                        []byte("0123456789abcdef0123456789abcdef"),
		CompletionAddSpecialTokens: true,
		ChatAddSpecialTokens:       false,
	})
	if err != nil {
		t.Fatalf("open native analyzer: %v", err)
	}
	defer func() {
		if err := analyzer.Close(); err != nil {
			t.Errorf("close native analyzer: %v", err)
		}
	}()

	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 40)
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Analyzer:    analyzer,
		Coordinator: coordinator,
	})
	if err != nil {
		t.Fatalf("new real predictive shadow: %v", err)
	}
	defer func() {
		if err := adapter.Close(); err != nil {
			t.Errorf("close real predictive shadow: %v", err)
		}
	}()

	input := predictiveShadowInput{
		Path: "/v1/chat/completions",
		Body: []byte(`{"model":"m","messages":[{"role":"user","content":"hello world"}],"max_tokens":8}`),
	}
	rendered, err := renderer.Render(context.Background(), input)
	if err != nil {
		t.Fatalf("render fixture request: %v", err)
	}
	analysis, err := analyzer.Analyze(context.Background(), rendered.Class, rendered.Rendered, rendered.Features)
	if err != nil {
		t.Fatalf("analyze rendered fixture request: %v", err)
	}
	if analysis.ExactInputTokens <= 0 || len(analysis.FullBlockDigests) == 0 {
		t.Fatalf("native analysis did not cover a full cache block: %+v", analysis)
	}

	first := adapter.DecideAndReserve(context.Background(), "native-first", input)
	if first == nil {
		t.Fatalf("first exact predictive request was not reserved: attempt=%+v", adapter.Snapshot())
	}
	firstSnapshot := coordinator.Snapshot()
	if firstSnapshot.Manager.Reservations != 1 || firstSnapshot.Cache.Requests != 1 || firstSnapshot.Cache.PendingBlocks != len(analysis.FullBlockDigests) {
		t.Fatalf("first native reservation snapshot = %+v", firstSnapshot)
	}
	if firstSnapshot.Manager.Virtual.Upper.UncachedPrefillTokens != analysis.ExactInputTokens {
		t.Fatalf("first uncached prefill = %d, want exact input %d", firstSnapshot.Manager.Virtual.Upper.UncachedPrefillTokens, analysis.ExactInputTokens)
	}
	if !first.MarkPrefillComplete() {
		t.Fatal("first native reservation did not enter decode")
	}

	second := adapter.DecideAndReserve(context.Background(), "native-second", input)
	if second == nil {
		t.Fatalf("second cache-aware predictive request was not reserved: attempt=%+v", adapter.Snapshot())
	}
	secondSnapshot := coordinator.Snapshot()
	if secondSnapshot.Manager.Reservations != 2 || secondSnapshot.Cache.Requests != 2 || secondSnapshot.Cache.ActiveBlocks != len(analysis.FullBlockDigests) {
		t.Fatalf("second native reservation snapshot = %+v", secondSnapshot)
	}
	if secondSnapshot.Manager.Virtual.Upper.UncachedPrefillTokens != analysis.PartialBlockTokens {
		t.Fatalf("cache-aware uncached prefill = %d, want partial block only %d", secondSnapshot.Manager.Virtual.Upper.UncachedPrefillTokens, analysis.PartialBlockTokens)
	}
	if secondSnapshot.Manager.Virtual.Upper.UncachedPrefillTokens >= analysis.ExactInputTokens {
		t.Fatalf("active full-block cache reuse did not reduce prefill: virtual=%+v analysis=%+v", secondSnapshot.Manager.Virtual.Upper, analysis)
	}

	if !second.Terminate(runtimepredictive.TerminalCompleted) || !first.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("native reservations did not terminate exactly once")
	}
	finalSnapshot := coordinator.Snapshot()
	if finalSnapshot.Manager.Reservations != 0 || finalSnapshot.Cache.Requests != 0 {
		t.Fatalf("native reservations leaked after completion: %+v", finalSnapshot)
	}

	protectiveCoordinator := newAdapterTestCoordinator(t)
	protectiveAdapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Analyzer:    analyzer,
		Coordinator: protectiveCoordinator,
	})
	if err != nil {
		t.Fatalf("new TPS-protective predictive shadow: %v", err)
	}
	defer func() {
		if err := protectiveAdapter.Close(); err != nil {
			t.Errorf("close TPS-protective predictive shadow: %v", err)
		}
	}()
	protectiveFirst := protectiveAdapter.DecideAndReserve(context.Background(), "protective-first", input)
	if protectiveFirst == nil || !protectiveFirst.MarkPrefillComplete() {
		t.Fatalf("protective first request did not establish an active cache prefix: attempt=%+v", protectiveAdapter.Snapshot())
	}
	if protectiveSecond := protectiveAdapter.DecideAndReserve(context.Background(), "protective-second", input); protectiveSecond != nil {
		protectiveSecond.Terminate(runtimepredictive.TerminalExpired)
		t.Fatal("cache reuse bypassed the post-join single-user TPS bound")
	}
	protectiveAttempt := protectiveAdapter.Snapshot()
	if protectiveAttempt.Risks != 1 || protectiveAttempt.LastReason != domainpredictive.ReasonNewTPSAtRisk || protectiveAttempt.LastSource != runtimepredictive.PredictionSourceStatic {
		t.Fatalf("TPS-protective cache-hit attempt = %+v", protectiveAttempt)
	}
	if protectiveSnapshot := protectiveCoordinator.Snapshot(); protectiveSnapshot.Manager.Reservations != 1 || protectiveSnapshot.Cache.Requests != 1 {
		t.Fatalf("TPS reject mutated committed cache or virtual reservations: %+v", protectiveSnapshot)
	}
	if !protectiveFirst.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("protective first reservation did not terminate")
	}
}
