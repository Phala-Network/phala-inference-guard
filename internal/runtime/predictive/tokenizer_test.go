package predictive

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestTokenizerRuntimeWarmsAndReturnsExactEngineIDs(t *testing.T) {
	manifest := completeTokenizerManifest("profile-1")
	manifest.SpecialTokenPolicy = domain.SpecialTokenPolicyAdd
	engine := &fakeTokenizerEngine{
		manifest: manifest,
		ids:      []int64{2, 100, 101, 1},
	}
	runtime, err := NewTokenizerRuntime(context.Background(), TokenizerProfile{
		Manifest:          manifest,
		SupportedClasses:  []RequestClass{RequestClassCompletion},
		MaximumConcurrent: 2,
	}, engine)
	if err != nil {
		t.Fatalf("new tokenizer runtime failed: %v", err)
	}
	if engine.warmCalls != 1 {
		t.Fatalf("warm calls = %d, want 1", engine.warmCalls)
	}

	result, err := runtime.Tokenize(context.Background(), TokenizeInput{
		Class:         RequestClassCompletion,
		RenderedInput: "hello",
	})
	if err != nil {
		t.Fatalf("tokenize failed: %v", err)
	}
	if result.ManifestID != manifest.ProfileID || result.ExactInputTokens != 4 || result.FullBlocks != 1 {
		t.Fatalf("tokenization result = %+v", result)
	}
	want := []int64{2, 100, 101, 1}
	if len(result.TokenIDs) != len(want) {
		t.Fatalf("token ids = %v, want %v", result.TokenIDs, want)
	}
	for index := range want {
		if result.TokenIDs[index] != want[index] {
			t.Fatalf("token id %d = %d, want %d", index, result.TokenIDs[index], want[index])
		}
	}
	if result.RenderedInputSHA256 == "" || result.RenderedInputSHA256 == "hello" {
		t.Fatalf("rendered fingerprint = %q, want non-plaintext digest", result.RenderedInputSHA256)
	}
	result.TokenIDs[0] = 999
	if engine.ids[0] != 2 {
		t.Fatal("token result aliases engine-owned token ids")
	}
	if !engine.LastAddSpecialTokens() {
		t.Fatal("engine did not receive the immutable profile special-token policy")
	}
}

func TestTokenizerRuntimeRejectsManifestMismatchBeforeWarm(t *testing.T) {
	expected := completeTokenizerManifest("profile-1")
	actual := expected
	actual.TemplateSHA256 = "different-template"
	engine := &fakeTokenizerEngine{manifest: actual}
	if _, err := NewTokenizerRuntime(context.Background(), TokenizerProfile{
		Manifest:          expected,
		SupportedClasses:  []RequestClass{RequestClassCompletion},
		MaximumConcurrent: 1,
	}, engine); !errors.Is(err, ErrTokenizerManifestMismatch) {
		t.Fatalf("manifest error = %v, want %v", err, ErrTokenizerManifestMismatch)
	}
	if engine.warmCalls != 0 {
		t.Fatalf("warm calls = %d, want 0 after manifest mismatch", engine.warmCalls)
	}
}

func TestTokenizerRuntimeRejectsUnsupportedClassWithoutEngineCall(t *testing.T) {
	manifest := completeTokenizerManifest("profile-1")
	engine := &fakeTokenizerEngine{manifest: manifest, ids: []int64{1}}
	runtime := mustTokenizerRuntime(t, TokenizerProfile{
		Manifest:          manifest,
		SupportedClasses:  []RequestClass{RequestClassCompletion},
		MaximumConcurrent: 1,
	}, engine)
	if _, err := runtime.Tokenize(context.Background(), TokenizeInput{
		Class:         RequestClassChat,
		RenderedInput: "chat input",
	}); !errors.Is(err, ErrUnsupportedRequestClass) {
		t.Fatalf("unsupported error = %v, want %v", err, ErrUnsupportedRequestClass)
	}
	if engine.encodeCalls != 0 {
		t.Fatalf("encode calls = %d, want 0", engine.encodeCalls)
	}
}

func TestTokenizerRuntimeRejectsUnsupportedFeatureSetWithoutEngineCall(t *testing.T) {
	manifest := completeTokenizerManifest("profile-1")
	manifest.Capabilities.Tools = false
	manifest.Capabilities.ToolChoice = false
	engine := &fakeTokenizerEngine{manifest: manifest, ids: []int64{1}}
	runtime := mustTokenizerRuntime(t, TokenizerProfile{
		Manifest:          manifest,
		SupportedClasses:  []RequestClass{RequestClassChat},
		MaximumConcurrent: 1,
	}, engine)
	if _, err := runtime.Tokenize(context.Background(), TokenizeInput{
		Class:         RequestClassChat,
		RenderedInput: "strictly rendered chat input",
		Features: RequestFeatures{
			Tools: true,
		},
	}); !errors.Is(err, ErrUnsupportedRequestFeatures) {
		t.Fatalf("unsupported feature error = %v, want %v", err, ErrUnsupportedRequestFeatures)
	}
	if engine.EncodeCalls() != 0 {
		t.Fatalf("encode calls = %d, want 0", engine.EncodeCalls())
	}
}

func TestTokenizerRuntimeBoundsConcurrencyAndHonorsCancellation(t *testing.T) {
	manifest := completeTokenizerManifest("profile-1")
	engine := &fakeTokenizerEngine{
		manifest: manifest,
		ids:      []int64{1},
		started:  make(chan struct{}),
		release:  make(chan struct{}),
	}
	runtime := mustTokenizerRuntime(t, TokenizerProfile{
		Manifest:          manifest,
		SupportedClasses:  []RequestClass{RequestClassCompletion},
		MaximumConcurrent: 1,
	}, engine)

	firstDone := make(chan error, 1)
	go func() {
		_, err := runtime.Tokenize(context.Background(), TokenizeInput{
			Class:         RequestClassCompletion,
			RenderedInput: "first",
		})
		firstDone <- err
	}()
	select {
	case <-engine.started:
	case <-time.After(time.Second):
		t.Fatal("first tokenization did not start")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runtime.Tokenize(cancelled, TokenizeInput{
		Class:         RequestClassCompletion,
		RenderedInput: "second",
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled error = %v, want context canceled", err)
	}
	if engine.EncodeCalls() != 1 {
		t.Fatalf("encode calls = %d, want only the first call", engine.EncodeCalls())
	}
	close(engine.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first tokenization failed: %v", err)
	}
}

func TestTokenizerRuntimeResetIsAtomicAndChangesProfileEpoch(t *testing.T) {
	firstManifest := completeTokenizerManifest("profile-1")
	firstEngine := &fakeTokenizerEngine{manifest: firstManifest, ids: []int64{1}}
	runtime := mustTokenizerRuntime(t, TokenizerProfile{
		Manifest:          firstManifest,
		SupportedClasses:  []RequestClass{RequestClassCompletion},
		MaximumConcurrent: 1,
	}, firstEngine)

	secondManifest := completeTokenizerManifest("profile-2")
	secondManifest.TokenizerRevision = "tokenizer-revision-2"
	secondEngine := &fakeTokenizerEngine{manifest: secondManifest, ids: []int64{2, 3}}
	if err := runtime.Reset(context.Background(), TokenizerProfile{
		Manifest:          secondManifest,
		SupportedClasses:  []RequestClass{RequestClassCompletion},
		MaximumConcurrent: 2,
	}, secondEngine); err != nil {
		t.Fatalf("reset failed: %v", err)
	}
	result, err := runtime.Tokenize(context.Background(), TokenizeInput{
		Class:         RequestClassCompletion,
		RenderedInput: "after reset",
	})
	if err != nil {
		t.Fatalf("tokenize after reset failed: %v", err)
	}
	if result.ManifestID != "profile-2" || result.ExactInputTokens != 2 || firstEngine.EncodeCalls() != 0 || secondEngine.EncodeCalls() != 1 {
		t.Fatalf("post-reset result = %+v, first/second calls = %d/%d", result, firstEngine.EncodeCalls(), secondEngine.EncodeCalls())
	}

	mismatch := secondManifest
	mismatch.BackendVersion = "different"
	badEngine := &fakeTokenizerEngine{manifest: mismatch, ids: []int64{9}}
	if err := runtime.Reset(context.Background(), TokenizerProfile{
		Manifest:          secondManifest,
		SupportedClasses:  []RequestClass{RequestClassCompletion},
		MaximumConcurrent: 1,
	}, badEngine); !errors.Is(err, ErrTokenizerManifestMismatch) {
		t.Fatalf("bad reset error = %v, want manifest mismatch", err)
	}
	if _, err := runtime.Tokenize(context.Background(), TokenizeInput{Class: RequestClassCompletion}); err != nil {
		t.Fatalf("old runtime must remain usable after failed reset: %v", err)
	}
	if secondEngine.EncodeCalls() != 2 || badEngine.EncodeCalls() != 0 {
		t.Fatalf("old/bad engine calls = %d/%d, want 2/0", secondEngine.EncodeCalls(), badEngine.EncodeCalls())
	}
}

func TestTokenizerRuntimeRejectsInvalidProfileAndTokenIDs(t *testing.T) {
	manifest := completeTokenizerManifest("profile-1")
	if _, err := NewTokenizerRuntime(context.Background(), TokenizerProfile{
		Manifest:         manifest,
		SupportedClasses: []RequestClass{RequestClassCompletion},
	}, &fakeTokenizerEngine{manifest: manifest}); err == nil {
		t.Fatal("zero maximum concurrency must fail")
	}

	engine := &fakeTokenizerEngine{manifest: manifest, ids: []int64{1, -1}}
	runtime := mustTokenizerRuntime(t, TokenizerProfile{
		Manifest:          manifest,
		SupportedClasses:  []RequestClass{RequestClassCompletion},
		MaximumConcurrent: 1,
	}, engine)
	if _, err := runtime.Tokenize(context.Background(), TokenizeInput{
		Class:         RequestClassCompletion,
		RenderedInput: "invalid ids",
	}); !errors.Is(err, ErrInvalidTokenizerOutput) {
		t.Fatalf("invalid output error = %v, want %v", err, ErrInvalidTokenizerOutput)
	}
}

type fakeTokenizerEngine struct {
	mu          sync.Mutex
	manifest    domain.TokenizerManifest
	ids         []int64
	warmErr     error
	encodeErr   error
	warmCalls   int
	encodeCalls int
	lastAddSpecialTokens bool
	started     chan struct{}
	startOnce   sync.Once
	release     chan struct{}
}

func (f *fakeTokenizerEngine) Manifest() domain.TokenizerManifest {
	return f.manifest
}

func (f *fakeTokenizerEngine) Warm(context.Context) error {
	f.mu.Lock()
	f.warmCalls++
	f.mu.Unlock()
	return f.warmErr
}

func (f *fakeTokenizerEngine) Encode(ctx context.Context, _ string, addSpecialTokens bool) ([]int64, error) {
	f.mu.Lock()
	f.encodeCalls++
	f.lastAddSpecialTokens = addSpecialTokens
	f.mu.Unlock()
	if f.started != nil {
		f.startOnce.Do(func() { close(f.started) })
	}
	if f.release != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.release:
		}
	}
	if f.encodeErr != nil {
		return nil, f.encodeErr
	}
	return append([]int64(nil), f.ids...), nil
}

func (f *fakeTokenizerEngine) LastAddSpecialTokens() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastAddSpecialTokens
}

func (f *fakeTokenizerEngine) EncodeCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.encodeCalls
}

func completeTokenizerManifest(profileID string) domain.TokenizerManifest {
	return domain.TokenizerManifest{
		ProfileID:             profileID,
		ServedModel:           "google/gemma-4-31B-it",
		ModelRepository:       "RedHatAI/gemma-4-31B-it-FP8-dynamic",
		ModelRevision:         "model-revision",
		TokenizerRepository:   "RedHatAI/gemma-4-31B-it-FP8-dynamic",
		TokenizerRevision:     "tokenizer-revision",
		TokenizerSHA256:       "tokenizer-sha",
		TokenizerConfigSHA256: "tokenizer-config-sha",
		SpecialTokensSHA256:   "special-tokens-sha",
		TemplateSHA256:        "template-sha",
		TemplateRuntime:       "minijinja-vllm-profile",
		TemplateRuntimeVersion: "v1",
		SpecialTokenPolicy:    domain.SpecialTokenPolicyOmit,
		SpecialTokens: domain.SpecialTokenBindings{
			BOS: domain.TokenBinding{Value: "<bos>", ID: 2},
			EOS: domain.TokenBinding{Value: "<eos>", ID: 1},
			UNK: domain.TokenBinding{Value: "<unk>", ID: 3},
			PAD: domain.TokenBinding{Value: "<pad>", ID: 0},
		},
		Capabilities: domain.TokenizerCapabilities{
			Completions:     true,
			ChatCompletions: true,
			Tools:           true,
			ToolChoice:      true,
			ResponseFormat:  true,
			JSONSchema:      true,
			Reasoning:       true,
		},
		BackendKind:           "vllm",
		BackendVersion:        "0.25.1",
		BlockSize:             4,
		MultimodalProfile:     "text-only",
		PredictorVersion:      "v0.9.1-test",
	}
}

func mustTokenizerRuntime(t *testing.T, profile TokenizerProfile, engine TokenizerEngine) *TokenizerRuntime {
	t.Helper()
	runtime, err := NewTokenizerRuntime(context.Background(), profile, engine)
	if err != nil {
		t.Fatalf("new tokenizer runtime failed: %v", err)
	}
	return runtime
}
