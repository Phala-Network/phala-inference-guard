package server

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveShadowInput struct {
	Path             string
	Body             []byte
	RequestStartedAt time.Time
	OutputTokens     int
	HasOutputTokens  bool
	Streaming        bool
}

type predictiveShadowReservation interface {
	MarkForwarded() bool
	MarkPrefillComplete() bool
	Terminate(runtimepredictive.TerminalCause) bool
}

type predictiveAdmissionShadow interface {
	DecideAndReserve(context.Context, string, predictiveShadowInput) predictiveShadowReservation
	Close() error
}

type predictiveAdmissionTelemetryProvider interface {
	PredictiveAdmissionTelemetry() predictiveAdmissionTelemetrySnapshot
}

type predictiveAdmissionTelemetrySnapshot struct {
	Attempts           predictiveAttemptSnapshot
	Manager            runtimepredictive.Snapshot
	Learning           runtimepredictive.LearnedSchedulerSnapshot
	PredictionDuration *durationHistogram
	RendererDuration   *durationHistogram
	TokenizerDuration  *durationHistogram
}

type predictiveSemanticTTFTObserver interface {
	ObserveSemanticTTFT(time.Duration) bool
}

type serverDependencies struct {
	NewPredictiveShadow func(config) (predictiveAdmissionShadow, error)
}

func predictiveAdmissionEnabled(mode string) bool {
	return mode == "shadow" || mode == "enforce"
}

type guardedPredictiveReservation struct {
	mu                    sync.Mutex
	reservation           predictiveShadowReservation
	forwarded             bool
	prefillComplete       bool
	semanticTTFTObserved  bool
	terminated            bool
	onForwardCallFailure  func()
	onSemanticCallFailure func()
	onTerminalCallFailure func()
}

func observePredictiveSemanticTTFT(reservation predictiveShadowReservation, ttft time.Duration) bool {
	observer, ok := reservation.(predictiveSemanticTTFTObserver)
	return ok && observer.ObserveSemanticTTFT(ttft)
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
		onForwardCallFailure: func() {
			s.predictiveShadowFailures.forward.Add(1)
		},
		onSemanticCallFailure: func() {
			s.predictiveShadowFailures.semantic.Add(1)
		},
		onTerminalCallFailure: func() {
			s.predictiveShadowFailures.terminal.Add(1)
		},
	}
}

func (r *guardedPredictiveReservation) MarkForwarded() bool {
	if r == nil || r.reservation == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.forwarded || r.terminated {
		return false
	}
	r.forwarded = true
	return callPredictiveShadow(r.onForwardCallFailure, r.reservation.MarkForwarded)
}

func (r *guardedPredictiveReservation) MarkPrefillComplete() bool {
	if r == nil || r.reservation == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.forwarded || r.prefillComplete || r.terminated {
		return false
	}
	r.prefillComplete = true
	return callPredictiveShadow(r.onSemanticCallFailure, r.reservation.MarkPrefillComplete)
}

func (r *guardedPredictiveReservation) ObserveSemanticTTFT(ttft time.Duration) bool {
	if r == nil || r.reservation == nil || ttft <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.semanticTTFTObserved || r.terminated {
		return false
	}
	observer, ok := r.reservation.(predictiveSemanticTTFTObserver)
	if !ok {
		return false
	}
	r.semanticTTFTObserved = true
	return callPredictiveShadow(r.onSemanticCallFailure, func() bool {
		return observer.ObserveSemanticTTFT(ttft)
	})
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

func (s *proxyServer) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		if s.predictiveShadow != nil {
			s.closeErr = closePredictiveShadow(s)
		}
	})
	return s.closeErr
}

func closePredictiveShadow(s *proxyServer) (err error) {
	defer func() {
		if recover() != nil {
			s.predictiveShadowFailures.close.Add(1)
			err = fmt.Errorf("predictive shadow close panicked")
		}
	}()
	return s.predictiveShadow.Close()
}
