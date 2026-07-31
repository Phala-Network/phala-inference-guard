package predictive

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

var (
	ErrTokenizerManifestMismatch = errors.New("tokenizer manifest mismatch")
	ErrUnsupportedRequestClass   = errors.New("unsupported tokenizer request class")
	ErrInvalidTokenizerOutput    = errors.New("invalid tokenizer output")
)

type RequestClass string

const (
	RequestClassCompletion RequestClass = "completion"
	RequestClassChat       RequestClass = "chat_completion"
)

type TokenizeInput struct {
	Class            RequestClass
	RenderedInput    string
	AddSpecialTokens bool
}

type TokenizationResult struct {
	ManifestID          string
	RenderedInputSHA256 string
	TokenIDs            []int64
	ExactInputTokens    int64
	FullBlocks          int64
}

type TokenizerProfile struct {
	Manifest          domain.TokenizerManifest
	SupportedClasses  []RequestClass
	MaximumConcurrent int
}

type TokenizerEngine interface {
	Manifest() domain.TokenizerManifest
	Warm(context.Context) error
	Encode(context.Context, string, bool) ([]int64, error)
}

type tokenizerRuntimeState struct {
	profile   TokenizerProfile
	supported map[RequestClass]struct{}
	engine    TokenizerEngine
	semaphore chan struct{}
}

type TokenizerRuntime struct {
	mu    sync.RWMutex
	state *tokenizerRuntimeState
}

func NewTokenizerRuntime(ctx context.Context, profile TokenizerProfile, engine TokenizerEngine) (*TokenizerRuntime, error) {
	state, err := buildTokenizerRuntimeState(ctx, profile, engine)
	if err != nil {
		return nil, err
	}
	return &TokenizerRuntime{state: state}, nil
}

func (r *TokenizerRuntime) Tokenize(ctx context.Context, input TokenizeInput) (TokenizationResult, error) {
	if r == nil {
		return TokenizationResult{}, fmt.Errorf("tokenizer runtime is nil")
	}
	r.mu.RLock()
	state := r.state
	r.mu.RUnlock()
	if state == nil {
		return TokenizationResult{}, fmt.Errorf("tokenizer runtime is not initialized")
	}
	if _, supported := state.supported[input.Class]; !supported {
		return TokenizationResult{}, fmt.Errorf("%w: %q", ErrUnsupportedRequestClass, input.Class)
	}
	select {
	case state.semaphore <- struct{}{}:
		defer func() { <-state.semaphore }()
	case <-ctx.Done():
		return TokenizationResult{}, ctx.Err()
	}

	tokenIDs, err := state.engine.Encode(ctx, input.RenderedInput, input.AddSpecialTokens)
	if err != nil {
		return TokenizationResult{}, fmt.Errorf("tokenizer encode: %w", err)
	}
	for index, tokenID := range tokenIDs {
		if tokenID < 0 {
			return TokenizationResult{}, fmt.Errorf("%w: token id %d at index %d", ErrInvalidTokenizerOutput, tokenID, index)
		}
	}
	ownedTokenIDs := append([]int64(nil), tokenIDs...)
	digest := sha256.Sum256([]byte(input.RenderedInput))
	return TokenizationResult{
		ManifestID:          state.profile.Manifest.ProfileID,
		RenderedInputSHA256: hex.EncodeToString(digest[:]),
		TokenIDs:            ownedTokenIDs,
		ExactInputTokens:    int64(len(ownedTokenIDs)),
		FullBlocks:          int64(len(ownedTokenIDs)) / state.profile.Manifest.BlockSize,
	}, nil
}

func (r *TokenizerRuntime) Reset(ctx context.Context, profile TokenizerProfile, engine TokenizerEngine) error {
	if r == nil {
		return fmt.Errorf("tokenizer runtime is nil")
	}
	state, err := buildTokenizerRuntimeState(ctx, profile, engine)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.state = state
	r.mu.Unlock()
	return nil
}

func buildTokenizerRuntimeState(ctx context.Context, profile TokenizerProfile, engine TokenizerEngine) (*tokenizerRuntimeState, error) {
	if err := profile.Manifest.Validate(); err != nil {
		return nil, err
	}
	if profile.MaximumConcurrent <= 0 {
		return nil, fmt.Errorf("tokenizer maximum concurrency must be positive")
	}
	if len(profile.SupportedClasses) == 0 {
		return nil, fmt.Errorf("tokenizer supported classes are required")
	}
	if engine == nil {
		return nil, fmt.Errorf("tokenizer engine is required")
	}
	if !profile.Manifest.Compatible(engine.Manifest()) {
		return nil, ErrTokenizerManifestMismatch
	}
	supported := make(map[RequestClass]struct{}, len(profile.SupportedClasses))
	for _, requestClass := range profile.SupportedClasses {
		if requestClass == "" {
			return nil, fmt.Errorf("tokenizer request class cannot be empty")
		}
		if _, exists := supported[requestClass]; exists {
			return nil, fmt.Errorf("duplicate tokenizer request class %q", requestClass)
		}
		supported[requestClass] = struct{}{}
	}
	if err := engine.Warm(ctx); err != nil {
		return nil, fmt.Errorf("warm tokenizer engine: %w", err)
	}
	if !profile.Manifest.Compatible(engine.Manifest()) {
		return nil, ErrTokenizerManifestMismatch
	}
	return &tokenizerRuntimeState{
		profile:   profile,
		supported: supported,
		engine:    engine,
		semaphore: make(chan struct{}, profile.MaximumConcurrent),
	}, nil
}
