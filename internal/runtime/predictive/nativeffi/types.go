package nativeffi

import "errors"

var ErrUnavailable = errors.New("native predictive tokenizer is unavailable")

type Config struct {
	TokenizerPath              string
	ManifestID                string
	BackendEpoch              string
	BlockSize                 int
	Key                       []byte
	CompletionAddSpecialTokens bool
	ChatAddSpecialTokens       bool
}
