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

func TestRealPredictiveShadowChargesRepeatedNativePrefixesAsFullColdCost(t *testing.T) {
	renderer, err := newGemma4TextRenderer(gemma4TextRendererConfig{
		BOSToken:             "<bos>",
		DefaultDecodeHorizon: 16,
		MaximumDecodeHorizon: 256,
	})
	if err != nil {
		t.Fatalf("new Gemma4 renderer: %v", err)
	}
	counter, err := nativeffi.OpenCounter(nativeffi.CounterConfig{
		TokenizerPath:              filepath.Join("..", "..", "..", "native", "tokenizer", "fixtures", "ffi-wordlevel-tokenizer.json"),
		ManifestID:                 "adapter-test-manifest",
		BackendEpoch:               "adapter-test-epoch",
		CompletionAddSpecialTokens: true,
		ChatAddSpecialTokens:       false,
	})
	if err != nil {
		t.Fatalf("open native counter: %v", err)
	}
	defer func() {
		if err := counter.Close(); err != nil {
			t.Errorf("close native counter: %v", err)
		}
	}()

	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 40)
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Counter:     counter,
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
	analysis, err := counter.Count(context.Background(), rendered.Class, rendered.Rendered, rendered.Features)
	if err != nil {
		t.Fatalf("count rendered fixture request: %v", err)
	}
	if analysis.ExactInputTokens <= 0 {
		t.Fatalf("native count did not return input tokens: %+v", analysis)
	}

	first := adapter.DecideAndReserve(context.Background(), "native-first", input)
	if first == nil {
		t.Fatalf("first exact predictive request was not reserved: attempt=%+v", adapter.Snapshot())
	}
	firstSnapshot := coordinator.Snapshot()
	if firstSnapshot.Manager.Reservations != 1 {
		t.Fatalf("first native reservation snapshot = %+v", firstSnapshot)
	}
	if firstSnapshot.Manager.Virtual.Upper.UncachedPrefillTokens != analysis.ExactInputTokens {
		t.Fatalf("first uncached prefill = %d, want exact input %d", firstSnapshot.Manager.Virtual.Upper.UncachedPrefillTokens, analysis.ExactInputTokens)
	}
	firstReservedKV := firstSnapshot.Manager.ReservedPhysicalKV
	if !first.MarkPrefillComplete() {
		t.Fatal("first native reservation did not enter decode")
	}

	second := adapter.DecideAndReserve(context.Background(), "native-second", input)
	if second == nil {
		t.Fatalf("second full-cold predictive request was not reserved: attempt=%+v", adapter.Snapshot())
	}
	secondSnapshot := coordinator.Snapshot()
	if secondSnapshot.Manager.Reservations != 2 {
		t.Fatalf("second native reservation snapshot = %+v", secondSnapshot)
	}
	if secondSnapshot.Manager.Virtual.Upper.UncachedPrefillTokens != analysis.ExactInputTokens {
		t.Fatalf("repeated-prefix uncached prefill = %d, want full exact input %d", secondSnapshot.Manager.Virtual.Upper.UncachedPrefillTokens, analysis.ExactInputTokens)
	}
	if secondSnapshot.Manager.ReservedPhysicalKV != firstReservedKV*2 {
		t.Fatalf("repeated-prefix reserved KV = %d, want twice first full-cold cost %d", secondSnapshot.Manager.ReservedPhysicalKV, firstReservedKV*2)
	}

	if !second.Terminate(runtimepredictive.TerminalCompleted) || !first.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("native reservations did not terminate exactly once")
	}
	finalSnapshot := coordinator.Snapshot()
	if finalSnapshot.Manager.Reservations != 0 {
		t.Fatalf("native reservations leaked after completion: %+v", finalSnapshot)
	}

	protectiveCoordinator := newAdapterTestCoordinator(t)
	protectiveAdapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Counter:     counter,
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
		t.Fatalf("protective first request did not enter decode: attempt=%+v", protectiveAdapter.Snapshot())
	}
	if protectiveSecond := protectiveAdapter.DecideAndReserve(context.Background(), "protective-second", input); protectiveSecond != nil {
		protectiveSecond.Terminate(runtimepredictive.TerminalExpired)
		t.Fatal("second request bypassed the post-join single-user TPS bound")
	}
	protectiveAttempt := protectiveAdapter.Snapshot()
	if protectiveAttempt.Risks != 1 || protectiveAttempt.LastReason != domainpredictive.ReasonExistingTPSAtRisk || protectiveAttempt.LastSource != runtimepredictive.PredictionSourceStatic {
		t.Fatalf("TPS-protective attempt = %+v", protectiveAttempt)
	}
	if protectiveSnapshot := protectiveCoordinator.Snapshot(); protectiveSnapshot.Manager.Reservations != 1 {
		t.Fatalf("TPS reject mutated committed virtual reservations: %+v", protectiveSnapshot)
	}
	if !protectiveFirst.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("protective first reservation did not terminate")
	}
}
