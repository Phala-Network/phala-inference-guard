package server

import (
	"context"
	"strconv"
	"sync"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveShadowInput struct {
	Path            string
	Body            []byte
	OutputTokens    int
	HasOutputTokens bool
	Streaming       bool
}

type predictiveShadowReservation interface {
	MarkPrefillComplete() bool
	Terminate(runtimepredictive.TerminalCause) bool
}

type predictiveAdmissionShadow interface {
	DecideAndReserve(context.Context, string, predictiveShadowInput) predictiveShadowReservation
}

type serverDependencies struct {
	NewPredictiveShadow func(config) (predictiveAdmissionShadow, error)
}

type guardedPredictiveReservation struct {
	mu                    sync.Mutex
	reservation           predictiveShadowReservation
	prefillComplete       bool
	terminated            bool
	onSemanticCallFailure func()
	onTerminalCallFailure func()
}

func (s *proxyServer) decidePredictiveShadow(ctx context.Context, input predictiveShadowInput) (result predictiveShadowReservation) {
	if s == nil || s.predictiveShadow == nil || input.Body == nil {
		return nil
	}
	defer clear(input.Body)
	defer func() {
		if recover() != nil {
			s.predictiveShadowFailures.decide.Add(1)
			result = nil
		}
	}()
	requestID := "http-" + strconv.FormatUint(s.nextPredictiveID.Add(1), 10)
	reservation := s.predictiveShadow.DecideAndReserve(ctx, requestID, input)
	if reservation == nil {
		return nil
	}
	return &guardedPredictiveReservation{
		reservation: reservation,
		onSemanticCallFailure: func() {
			s.predictiveShadowFailures.semantic.Add(1)
		},
		onTerminalCallFailure: func() {
			s.predictiveShadowFailures.terminal.Add(1)
		},
	}
}

func (r *guardedPredictiveReservation) MarkPrefillComplete() bool {
	if r == nil || r.reservation == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.prefillComplete || r.terminated {
		return false
	}
	r.prefillComplete = true
	return callPredictiveShadow(r.onSemanticCallFailure, r.reservation.MarkPrefillComplete)
}

func (r *guardedPredictiveReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	if r == nil || r.reservation == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminated {
		return false
	}
	r.terminated = true
	return callPredictiveShadow(r.onTerminalCallFailure, func() bool {
		return r.reservation.Terminate(cause)
	})
}

func callPredictiveShadow(onFailure func(), call func() bool) (result bool) {
	defer func() {
		if recover() != nil {
			if onFailure != nil {
				onFailure()
			}
			result = false
		}
	}()
	return call()
}
