package server

import (
	"context"
	"errors"
	"fmt"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

var errPredictiveRendererUnavailable = errors.New("predictive renderer is unavailable")

type gemma4TextRendererConfig struct {
	BOSToken             string
	DefaultDecodeHorizon int64
	MaximumDecodeHorizon int64
}

type gemma4TextRenderer struct {
	config gemma4TextRendererConfig
}

func newGemma4TextRenderer(config gemma4TextRendererConfig) (*gemma4TextRenderer, error) {
	if config.BOSToken == "" {
		return nil, fmt.Errorf("Gemma4 renderer BOS token is required")
	}
	if config.DefaultDecodeHorizon <= 0 || config.MaximumDecodeHorizon < config.DefaultDecodeHorizon {
		return nil, fmt.Errorf("Gemma4 renderer decode horizon is invalid")
	}
	return &gemma4TextRenderer{config: config}, nil
}

func (r *gemma4TextRenderer) Render(context.Context, predictiveShadowInput) (predictiveRenderedRequest, error) {
	return predictiveRenderedRequest{
		Class: runtimepredictive.RequestClassChat,
	}, errPredictiveRendererUnavailable
}
