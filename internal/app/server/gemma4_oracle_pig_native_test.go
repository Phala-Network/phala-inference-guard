//go:build pig_native && cgo

package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive/nativeffi"
)

type gemma4NativeOracle struct {
	Cases  []gemma4NativeOracleCase `json:"cases"`
	Oracle struct {
		TokenizerSHA256 string `json:"tokenizer_sha256"`
	} `json:"oracle"`
}

type gemma4NativeOracleCase struct {
	Name             string `json:"name"`
	Path             string `json:"path"`
	CanonicalBody    string `json:"canonical_body"`
	Rendered         string `json:"rendered"`
	RenderedSHA256   string `json:"rendered_sha256"`
	AddSpecialTokens bool   `json:"add_special_tokens"`
	TokenCount       int64  `json:"token_count"`
}

func TestGemma4RendererAndNativeAnalyzerMatchProductionOracle(t *testing.T) {
	oraclePath := os.Getenv("PIG_GEMMA4_ORACLE_PATH")
	tokenizerPath := os.Getenv("PIG_GEMMA4_TOKENIZER_PATH")
	if oraclePath == "" || tokenizerPath == "" {
		t.Skip("production Gemma4 oracle assets are a remote-builder-only gate")
	}
	oraclePayload, err := os.ReadFile(oraclePath)
	if err != nil {
		t.Fatalf("read Gemma4 oracle: %v", err)
	}
	var oracle gemma4NativeOracle
	if err := json.Unmarshal(oraclePayload, &oracle); err != nil {
		t.Fatalf("decode Gemma4 oracle: %v", err)
	}
	if len(oracle.Cases) == 0 || oracle.Oracle.TokenizerSHA256 == "" {
		t.Fatalf("Gemma4 oracle is incomplete: %+v", oracle)
	}
	if actual := fileSHA256(t, tokenizerPath); actual != oracle.Oracle.TokenizerSHA256 {
		t.Fatalf("Gemma4 tokenizer SHA-256 = %s, want %s", actual, oracle.Oracle.TokenizerSHA256)
	}

	renderer, err := newGemma4TextRenderer(gemma4TextRendererConfig{
		BOSToken:             "<bos>",
		DefaultDecodeHorizon: 128,
		MaximumDecodeHorizon: 262_144,
	})
	if err != nil {
		t.Fatalf("new Gemma4 renderer: %v", err)
	}
	analyzer, err := nativeffi.Open(nativeffi.Config{
		TokenizerPath:              tokenizerPath,
		ManifestID:                 "gemma4-production-text-v1",
		BackendEpoch:               "gemma4-production-oracle-epoch",
		BlockSize:                  64,
		Key:                        []byte("0123456789abcdef0123456789abcdef"),
		CompletionAddSpecialTokens: true,
		ChatAddSpecialTokens:       false,
	})
	if err != nil {
		t.Fatalf("open production Gemma4 native analyzer: %v", err)
	}
	defer func() {
		if err := analyzer.Close(); err != nil {
			t.Errorf("close production Gemma4 native analyzer: %v", err)
		}
	}()

	for _, test := range oracle.Cases {
		t.Run(test.Name, func(t *testing.T) {
			rendered, err := renderer.Render(context.Background(), predictiveShadowInput{
				Path: test.Path,
				Body: []byte(test.CanonicalBody),
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if string(rendered.Rendered) != test.Rendered {
				t.Fatalf("rendered bytes differ: got %q want %q", rendered.Rendered, test.Rendered)
			}
			renderedDigest := sha256.Sum256(rendered.Rendered)
			if actual := hex.EncodeToString(renderedDigest[:]); actual != test.RenderedSHA256 {
				t.Fatalf("rendered SHA-256 = %s, want %s", actual, test.RenderedSHA256)
			}
			wantAddSpecialTokens := rendered.Class == runtimepredictive.RequestClassCompletion
			if test.AddSpecialTokens != wantAddSpecialTokens {
				t.Fatalf("oracle special-token policy = %t, want %t for class %q", test.AddSpecialTokens, wantAddSpecialTokens, rendered.Class)
			}
			analysis, err := analyzer.Analyze(context.Background(), rendered.Class, rendered.Rendered, rendered.Features)
			if err != nil {
				t.Fatalf("native analyze: %v", err)
			}
			if analysis.ExactInputTokens != test.TokenCount {
				t.Fatalf("native token count = %d, want %d", analysis.ExactInputTokens, test.TokenCount)
			}
			wantFullBlocks := int(test.TokenCount / 64)
			wantPartialTokens := test.TokenCount % 64
			if len(analysis.FullBlockDigests) != wantFullBlocks || analysis.PartialBlockTokens != wantPartialTokens {
				t.Fatalf("native block shape = full:%d partial:%d, want full:%d partial:%d", len(analysis.FullBlockDigests), analysis.PartialBlockTokens, wantFullBlocks, wantPartialTokens)
			}
		})
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		t.Fatalf("hash %s: %v", path, err)
	}
	return hex.EncodeToString(digest.Sum(nil))
}
