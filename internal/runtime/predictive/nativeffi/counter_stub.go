//go:build !pig_native || !cgo

package nativeffi

import (
	"context"
	"fmt"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type Counter struct{}

func OpenCounter(CounterConfig) (*Counter, error) {
	return nil, fmt.Errorf("%w: count-only tokenizer is not linked", ErrUnavailable)
}

func (c *Counter) Count(context.Context, runtimepredictive.RequestClass, []byte, runtimepredictive.RequestFeatures) (runtimepredictive.TokenCountAnalysis, error) {
	return runtimepredictive.TokenCountAnalysis{}, fmt.Errorf("%w: count-only tokenizer is not linked", ErrUnavailable)
}

func (c *Counter) Close() error {
	return nil
}
