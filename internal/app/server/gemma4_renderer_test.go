package server

import (
	"context"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestGemma4TextRendererMatchesPinnedProductionTemplateOracle(t *testing.T) {
	renderer, err := newGemma4TextRenderer(gemma4TextRendererConfig{
		BOSToken:             "<bos>",
		DefaultDecodeHorizon: 128,
		MaximumDecodeHorizon: 262_144,
	})
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}
	tests := []struct {
		name          string
		path          string
		body          string
		wantClass     runtimepredictive.RequestClass
		wantRendered  string
		wantDecode    int64
	}{
		{
			name:         "chat user text",
			path:         "/v1/chat/completions",
			body:         `{"model":"google/gemma-4-31B-it","messages":[{"role":"user","content":"hello"}],"max_tokens":16}`,
			wantClass:    runtimepredictive.RequestClassChat,
			wantRendered: "<bos><|turn>user\nhello<turn|>\n<|turn>model\n<|channel>thought\n<channel|>",
			wantDecode:   16,
		},
		{
			name:         "chat system user text",
			path:         "/v1/chat/completions",
			body:         `{"model":"google/gemma-4-31B-it","messages":[{"role":"system","content":"be concise"},{"role":"user","content":"hello"}],"max_completion_tokens":32}`,
			wantClass:    runtimepredictive.RequestClassChat,
			wantRendered: "<bos><|turn>system\nbe concise<turn|>\n<|turn>user\nhello<turn|>\n<|turn>model\n<|channel>thought\n<channel|>",
			wantDecode:   32,
		},
		{
			name:         "chat multi turn text",
			path:         "/v1/chat/completions",
			body:         `{"model":"google/gemma-4-31B-it","messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"three"}]}`,
			wantClass:    runtimepredictive.RequestClassChat,
			wantRendered: "<bos><|turn>user\none<turn|>\n<|turn>model\n<|channel>thought\n<channel|>two<turn|>\n<|turn>user\nthree<turn|>\n<|turn>model\n<|channel>thought\n<channel|>",
			wantDecode:   128,
		},
		{
			name:         "chat text parts",
			path:         "/v1/chat/completions",
			body:         `{"model":"google/gemma-4-31B-it","messages":[{"role":"developer","content":[{"type":"text","text":"rules"}]},{"role":"user","content":[{"type":"text","text":"hello"},{"type":"text","text":" world"}]}]}`,
			wantClass:    runtimepredictive.RequestClassChat,
			wantRendered: "<bos><|turn>system\nrules <turn|>\n<|turn>user\nhelloworld<turn|>\n<|turn>model\n<|channel>thought\n<channel|>",
			wantDecode:   128,
		},
		{
			name:         "completion text",
			path:         "/v1/completions",
			body:         `{"model":"google/gemma-4-31B-it","prompt":"hello world","max_tokens":8}`,
			wantClass:    runtimepredictive.RequestClassCompletion,
			wantRendered: "hello world",
			wantDecode:   8,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := renderer.Render(context.Background(), predictiveShadowInput{
				Path: test.path,
				Body: []byte(test.body),
			})
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			if result.Class != test.wantClass || string(result.Rendered) != test.wantRendered || result.DecodeHorizonUpper != test.wantDecode || result.Confidence != 1 || result.Features != (runtimepredictive.RequestFeatures{}) {
				t.Fatalf("rendered = %+v/%q", result, result.Rendered)
			}
		})
	}
}

func TestGemma4TextRendererRejectsLossyOrUnsupportedInputs(t *testing.T) {
	renderer, err := newGemma4TextRenderer(gemma4TextRendererConfig{
		BOSToken:             "<bos>",
		DefaultDecodeHorizon: 128,
		MaximumDecodeHorizon: 262_144,
	})
	if err != nil {
		t.Fatalf("new renderer: %v", err)
	}
	tests := []struct {
		name string
		path string
		body string
	}{
		{name: "duplicate root key", path: "/v1/chat/completions", body: `{"messages":[],"messages":[]}`},
		{name: "tools unsupported", path: "/v1/chat/completions", body: `{"messages":[],"tools":[{"type":"function"}]}`},
		{name: "multimodal unsupported", path: "/v1/chat/completions", body: `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"x"}}]}]}`},
		{name: "completion prompt array unsupported", path: "/v1/completions", body: `{"prompt":["a","b"]}`},
		{name: "conflicting output horizons", path: "/v1/chat/completions", body: `{"messages":[],"max_tokens":8,"max_completion_tokens":9}`},
		{name: "trailing JSON", path: "/v1/chat/completions", body: `{"messages":[]} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := renderer.Render(context.Background(), predictiveShadowInput{Path: test.path, Body: []byte(test.body)}); err == nil {
				t.Fatal("unsupported input rendered successfully")
			}
		})
	}
}
