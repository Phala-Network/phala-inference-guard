//go:build pig_native && cgo

package nativeffi

import (
	"context"
	"fmt"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type Analyzer struct{}

func Open(Config) (*Analyzer, error) {
	return nil, ErrUnavailable
}

func (a *Analyzer) Analyze(context.Context, runtimepredictive.RequestClass, []byte, runtimepredictive.RequestFeatures) (runtimepredictive.TokenBlockAnalysis, error) {
	return runtimepredictive.TokenBlockAnalysis{}, fmt.Errorf("%w: C ABI is not connected", ErrUnavailable)
}

func (a *Analyzer) Close() error {
	return nil
}
