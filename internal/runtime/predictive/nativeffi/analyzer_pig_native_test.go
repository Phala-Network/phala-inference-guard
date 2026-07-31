//go:build pig_native && cgo

package nativeffi

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestNativeAnalyzerReturnsOpaqueBlockAnalysisAndClosesIdempotently(t *testing.T) {
	analyzer, err := Open(Config{
		TokenizerPath: filepath.Join("..", "..", "..", "..", "native", "tokenizer", "fixtures", "ffi-wordlevel-tokenizer.json"),
		ManifestID:    "go-ffi-test-manifest",
		BackendEpoch:  "go-ffi-test-epoch",
		BlockSize:     2,
		Key:           []byte("0123456789abcdef0123456789abcdef"),
	})
	if err != nil {
		t.Fatalf("open native analyzer: %v", err)
	}

	analysis, err := analyzer.Analyze(
		context.Background(),
		runtimepredictive.RequestClassCompletion,
		[]byte("hello world"),
		runtimepredictive.RequestFeatures{},
	)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}
	if analysis.ManifestID != "go-ffi-test-manifest" || analysis.BackendEpoch != "go-ffi-test-epoch" || analysis.BlockSize != 2 || analysis.ExactInputTokens != 2 || len(analysis.FullBlockDigests) != 1 || analysis.FullBlockDigests[0] == (runtimepredictive.CacheBlockDigest{}) || analysis.PartialBlockTokens != 0 || analysis.PartialBlockDigest != (runtimepredictive.CacheBlockDigest{}) {
		t.Fatalf("analysis = %+v", analysis)
	}
	if err := analyzer.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := analyzer.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if _, err := analyzer.Analyze(context.Background(), runtimepredictive.RequestClassCompletion, []byte("hello"), runtimepredictive.RequestFeatures{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("analyze after close error = %v, want unavailable", err)
	}
}
