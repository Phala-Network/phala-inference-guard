package nativeffi

import "errors"

var ErrUnavailable = errors.New("native predictive tokenizer is unavailable")

type CounterConfig struct {
	TokenizerPath              string
	ManifestID                 string
	BackendEpoch               string
	CompletionAddSpecialTokens bool
	ChatAddSpecialTokens       bool
}
