package server

import (
	"io"
	"sync"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveResponseBodyReadCloser struct {
	io.ReadCloser
	firstOnce    sync.Once
	completeOnce sync.Once
	onFirst      func()
	onComplete   func()
}

func (r *predictiveResponseBodyReadCloser) Read(buffer []byte) (int, error) {
	read, err := r.ReadCloser.Read(buffer)
	if read > 0 && r.onFirst != nil {
		r.firstOnce.Do(r.onFirst)
	}
	if err == io.EOF && r.onComplete != nil {
		r.completeOnce.Do(r.onComplete)
	}
	return read, err
}

func observePredictiveResponseBody(body io.ReadCloser, onFirst, onComplete func()) io.ReadCloser {
	if body == nil || (onFirst == nil && onComplete == nil) {
		return body
	}
	return &predictiveResponseBodyReadCloser{
		ReadCloser: body,
		onFirst:    onFirst,
		onComplete: onComplete,
	}
}

type predictiveAvailabilityProvider interface {
	Available() bool
}

func predictiveCoordinatorAvailable(provider predictiveAvailabilityProvider) (available bool) {
	if provider == nil {
		return true
	}
	defer func() {
		if recover() != nil {
			available = false
		}
	}()
	return provider.Available()
}

type predictiveProtectionScope string

const (
	predictiveProtectionScopeRequest      predictiveProtectionScope = "request"
	predictiveProtectionScopeLoad         predictiveProtectionScope = "load"
	predictiveProtectionScopeAvailability predictiveProtectionScope = "availability"
)

type predictiveAttemptSnapshot struct {
	Attempts         uint64
	Fits             uint64
	Risks            uint64
	Unknown          uint64
	LastReason       domainpredictive.Reason
	LastSource       runtimepredictive.PredictionSource
	LastRejectReason domainpredictive.Reason
	LastRejectSource runtimepredictive.PredictionSource
	LastRejectScope  predictiveProtectionScope
	LastRejectAt     time.Time
}
