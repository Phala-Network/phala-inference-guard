//go:build pig_native && cgo

package nativeffi

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestNativeCounterReturnsOnlyExactTokenCountAndClosesIdempotently(t *testing.T) {
	counter, err := OpenCounter(CounterConfig{
		TokenizerPath: filepath.Join("..", "..", "..", "..", "native", "tokenizer", "fixtures", "ffi-wordlevel-tokenizer.json"),
		ManifestID:    "go-count-test-manifest",
		BackendEpoch:  "go-count-test-epoch",
	})
	if err != nil {
		t.Fatalf("open native counter: %v", err)
	}

	analysis, err := counter.Count(
		context.Background(),
		runtimepredictive.RequestClassCompletion,
		[]byte("hello world"),
		runtimepredictive.RequestFeatures{},
	)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if analysis != (runtimepredictive.TokenCountAnalysis{
		ManifestID:       "go-count-test-manifest",
		BackendEpoch:     "go-count-test-epoch",
		ExactInputTokens: 2,
	}) {
		t.Fatalf("count analysis = %+v", analysis)
	}
	if err := counter.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := counter.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := counter.Count(context.Background(), runtimepredictive.RequestClassCompletion, []byte("hello"), runtimepredictive.RequestFeatures{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("count after close error = %v, want unavailable", err)
	}
}
