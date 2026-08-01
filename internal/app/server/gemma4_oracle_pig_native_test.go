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

func TestGemma4RendererAndCountOnlyNativePathMatchProductionOracle(t *testing.T) {
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
	expectedCounts := map[string]int64{
		"chat_user_text":        14,
		"chat_system_user_text": 21,
		"chat_multi_turn_text":  30,
		"chat_text_parts":       21,
		"completion_text":       2,
	}
	if len(oracle.Cases) != len(expectedCounts) {
		t.Fatalf("Gemma4 oracle case count = %d, want %d", len(oracle.Cases), len(expectedCounts))
	}
	if actual := fileSHA256(t, tokenizerPath); actual != oracle.Oracle.TokenizerSHA256 {
		t.Fatalf("Gemma4 tokenizer SHA-256 = %s, want %s", actual, oracle.Oracle.TokenizerSHA256)
	}

	renderer, err := newGemma4TextRenderer(gemma4TextRendererConfig{
		ServedModel:          "google/gemma-4-31B-it",
		BOSToken:             "<bos>",
		DefaultDecodeHorizon: 128,
		MaximumDecodeHorizon: 262_144,
	})
	if err != nil {
		t.Fatalf("new Gemma4 renderer: %v", err)
	}
	counter, err := nativeffi.OpenCounter(nativeffi.CounterConfig{
		TokenizerPath:              tokenizerPath,
		ManifestID:                 "gemma4-production-text-v1",
		BackendEpoch:               "gemma4-production-oracle-epoch",
		CompletionAddSpecialTokens: true,
		ChatAddSpecialTokens:       false,
	})
	if err != nil {
		t.Fatalf("open production Gemma4 native counter: %v", err)
	}
	defer func() {
		if err := counter.Close(); err != nil {
			t.Errorf("close production Gemma4 native counter: %v", err)
		}
	}()

	seen := make(map[string]bool, len(expectedCounts))
	for _, test := range oracle.Cases {
		t.Run(test.Name, func(t *testing.T) {
			wantCount, exists := expectedCounts[test.Name]
			if !exists {
				t.Fatalf("unexpected oracle case %q", test.Name)
			}
			seen[test.Name] = true
			if test.TokenCount != wantCount {
				t.Fatalf("oracle token count = %d, pinned count = %d", test.TokenCount, wantCount)
			}
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
			wantAddSpecialTokens := rendered.Class == runtimepredictive.RequestClassCompletion
			if test.AddSpecialTokens != wantAddSpecialTokens {
				t.Fatalf("oracle special-token policy = %t, want %t for class %q", test.AddSpecialTokens, wantAddSpecialTokens, rendered.Class)
			}
			analysis, err := counter.Count(context.Background(), rendered.Class, rendered.Rendered)
			if err != nil {
				t.Fatalf("native count: %v", err)
			}
			if analysis.ExactInputTokens != wantCount {
				t.Fatalf("count-only native token count = %d, want %d", analysis.ExactInputTokens, wantCount)
			}
			if analysis.ManifestID != "gemma4-production-text-v1" || analysis.BackendEpoch != "gemma4-production-oracle-epoch" {
				t.Fatalf("count identity = %+v", analysis)
			}
		})
	}
	for name := range expectedCounts {
		if !seen[name] {
			t.Fatalf("missing pinned oracle case %q", name)
		}
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
