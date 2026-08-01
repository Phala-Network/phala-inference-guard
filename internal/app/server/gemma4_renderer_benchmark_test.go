package server

import (
	"context"
	"strings"
	"testing"
)

var gemma4RendererBenchmarkBytes int

func BenchmarkGemma4TextRendererLongChat(b *testing.B) {
	renderer, err := newGemma4TextRenderer(gemma4TextRendererConfig{
		ServedModel:          "gemma-4",
		BOSToken:             "<bos>",
		DefaultDecodeHorizon: 256,
		MaximumDecodeHorizon: 131_072,
	})
	if err != nil {
		b.Fatalf("new renderer: %v", err)
	}
	body := []byte(`{"model":"gemma-4","messages":[{"role":"user","content":"` + strings.Repeat("predictive admission token ", 8_192) + `"}],"max_tokens":256}`)
	input := predictiveShadowInput{Path: "/v1/chat/completions", Body: body}
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		rendered, renderErr := renderer.Render(context.Background(), input)
		if renderErr != nil {
			b.Fatalf("render: %v", renderErr)
		}
		gemma4RendererBenchmarkBytes = len(rendered.Rendered)
		clear(rendered.Rendered)
	}
}
