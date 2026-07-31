package predictive

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

const renderedInputFingerprintDomain = "pig-rendered-input-fingerprint-v1"

var (
	ErrTokenizerManifestMismatch  = errors.New("tokenizer manifest mismatch")
	ErrUnsupportedRequestClass    = errors.New("unsupported tokenizer request class")
	ErrUnsupportedRequestFeatures = errors.New("unsupported tokenizer request features")
	ErrInvalidTokenizerOutput     = errors.New("invalid tokenizer output")
)

type RequestClass string

const (
	RequestClassCompletion RequestClass = "completion"
	RequestClassChat       RequestClass = "chat_completion"
)

type TokenizeInput struct {
	Class         RequestClass
	RenderedInput string
	Features      RequestFeatures
}

type RequestFeatures struct {
	Tools          bool
	ToolChoice     bool
	ResponseFormat bool
	JSONSchema     bool
	Reasoning      bool
	Multimodal     bool
}

type TokenizationResult struct {
	ManifestID               string
	RenderedInputFingerprint string
	TokenIDs                 []int64
	ExactInputTokens         int64
	FullBlocks               int64
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
	mu             sync.RWMutex
	state          *tokenizerRuntimeState
	fingerprintKey [sha256.Size]byte
}

func NewTokenizerRuntime(ctx context.Context, profile TokenizerProfile, engine TokenizerEngine) (*TokenizerRuntime, error) {
	state, err := buildTokenizerRuntimeState(ctx, profile, engine)
	if err != nil {
		return nil, err
	}
	var fingerprintKey [sha256.Size]byte
	if _, err := rand.Read(fingerprintKey[:]); err != nil {
		return nil, fmt.Errorf("generate rendered-input fingerprint key: %w", err)
	}
	return &TokenizerRuntime{state: state, fingerprintKey: fingerprintKey}, nil
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
	if err := validateRequestFeatures(input.Class, input.Features, state.profile.Manifest.Capabilities); err != nil {
		return TokenizationResult{}, err
	}
	specialTokenPolicy, err := specialTokenPolicyForRequestClass(state.profile.Manifest.SpecialTokenPolicies, input.Class)
	if err != nil {
		return TokenizationResult{}, err
	}
	select {
	case state.semaphore <- struct{}{}:
		defer func() { <-state.semaphore }()
	case <-ctx.Done():
		return TokenizationResult{}, ctx.Err()
	}

	tokenIDs, err := state.engine.Encode(
		ctx,
		input.RenderedInput,
		specialTokenPolicy.AddSpecialTokens(),
	)
	if err != nil {
		return TokenizationResult{}, fmt.Errorf("tokenizer encode: %w", err)
	}
	for index, tokenID := range tokenIDs {
		if tokenID < 0 || tokenID > int64(^uint32(0)) {
			return TokenizationResult{}, fmt.Errorf("%w: token id %d at index %d", ErrInvalidTokenizerOutput, tokenID, index)
		}
	}
	ownedTokenIDs := append([]int64(nil), tokenIDs...)
	digest := hmac.New(sha256.New, r.fingerprintKey[:])
	_, _ = digest.Write([]byte(renderedInputFingerprintDomain))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write([]byte(input.RenderedInput))
	return TokenizationResult{
		ManifestID:               state.profile.Manifest.ProfileID,
		RenderedInputFingerprint: hex.EncodeToString(digest.Sum(nil)),
		TokenIDs:                 ownedTokenIDs,
		ExactInputTokens:         int64(len(ownedTokenIDs)),
		FullBlocks:               int64(len(ownedTokenIDs)) / state.profile.Manifest.BlockSize,
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
		if !manifestSupportsRequestClass(profile.Manifest.Capabilities, requestClass) {
			return nil, fmt.Errorf("%w: manifest does not enable %q", ErrUnsupportedRequestClass, requestClass)
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

func specialTokenPolicyForRequestClass(policies domain.SpecialTokenPolicies, requestClass RequestClass) (domain.SpecialTokenPolicy, error) {
	switch requestClass {
	case RequestClassCompletion:
		return policies.Completions, nil
	case RequestClassChat:
		return policies.ChatCompletions, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnsupportedRequestClass, requestClass)
	}
}

func manifestSupportsRequestClass(capabilities domain.TokenizerCapabilities, requestClass RequestClass) bool {
	switch requestClass {
	case RequestClassCompletion:
		return capabilities.Completions
	case RequestClassChat:
		return capabilities.ChatCompletions
	default:
		return false
	}
}

func validateRequestFeatures(class RequestClass, features RequestFeatures, capabilities domain.TokenizerCapabilities) error {
	if class != RequestClassChat && features != (RequestFeatures{}) {
		return fmt.Errorf("%w: request class %q does not support chat features", ErrUnsupportedRequestFeatures, class)
	}
	unsupported := ""
	switch {
	case features.ToolChoice && !features.Tools:
		unsupported = "tool_choice_without_tools"
	case features.JSONSchema && !features.ResponseFormat:
		unsupported = "json_schema_without_response_format"
	case features.Tools && !capabilities.Tools:
		unsupported = "tools"
	case features.ToolChoice && !capabilities.ToolChoice:
		unsupported = "tool_choice"
	case features.ResponseFormat && !capabilities.ResponseFormat:
		unsupported = "response_format"
	case features.JSONSchema && !capabilities.JSONSchema:
		unsupported = "json_schema"
	case features.Reasoning && !capabilities.Reasoning:
		unsupported = "reasoning"
	case features.Multimodal && !capabilities.Multimodal:
		unsupported = "multimodal"
	}
	if unsupported != "" {
		return fmt.Errorf("%w: %s", ErrUnsupportedRequestFeatures, unsupported)
	}
	return nil
}
