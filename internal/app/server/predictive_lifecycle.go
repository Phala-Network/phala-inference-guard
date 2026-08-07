package server

import (
	"io"
	"sync"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveFirstBodyReadCloser struct {
	io.ReadCloser
	once    sync.Once
	onFirst func()
}

func (r *predictiveFirstBodyReadCloser) Read(buffer []byte) (int, error) {
	read, err := r.ReadCloser.Read(buffer)
	if read > 0 {
		r.once.Do(r.onFirst)
	}
	return read, err
}

func observePredictiveFirstBodyRead(body io.ReadCloser, onFirst func()) io.ReadCloser {
	if body == nil || onFirst == nil {
		return body
	}
	return &predictiveFirstBodyReadCloser{ReadCloser: body, onFirst: onFirst}
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
