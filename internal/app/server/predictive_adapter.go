package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveRenderedRequest struct {
	Class              runtimepredictive.RequestClass
	Rendered           []byte
	Features           runtimepredictive.RequestFeatures
	DecodeHorizonUpper int64
	Confidence         float64
}

type predictiveRequestRenderer interface {
	Render(context.Context, predictiveShadowInput) (predictiveRenderedRequest, error)
}

type predictiveTokenAnalyzer interface {
	Analyze(context.Context, runtimepredictive.RequestClass, []byte, runtimepredictive.RequestFeatures) (runtimepredictive.TokenBlockAnalysis, error)
}

type realPredictiveShadowConfig struct {
	Renderer    predictiveRequestRenderer
	Analyzer    predictiveTokenAnalyzer
	Coordinator *runtimepredictive.Coordinator
	Now         func() time.Time
}

type predictiveAttemptSnapshot struct {
	Attempts    uint64
	Fits        uint64
	Unknown     uint64
	LastReason  domainpredictive.Reason
	LastSource  runtimepredictive.PredictionSource
	LastSamples int
}

type realPredictiveShadow struct {
	mu          sync.Mutex
	renderer    predictiveRequestRenderer
	analyzer    predictiveTokenAnalyzer
	coordinator *runtimepredictive.Coordinator
	now         func() time.Time
	closed      bool
	attempts    predictiveAttemptSnapshot
}

type realPredictiveReservation struct {
	owner     *realPredictiveShadow
	requestID string
}

func newRealPredictiveShadow(config realPredictiveShadowConfig) (*realPredictiveShadow, error) {
	if config.Renderer == nil {
		return nil, fmt.Errorf("predictive request renderer is required")
	}
	if config.Analyzer == nil {
		return nil, fmt.Errorf("predictive token analyzer is required")
	}
	if config.Coordinator == nil {
		return nil, fmt.Errorf("predictive coordinator is required")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &realPredictiveShadow{
		renderer:    config.Renderer,
		analyzer:    config.Analyzer,
		coordinator: config.Coordinator,
		now:         config.Now,
	}, nil
}

func (s *realPredictiveShadow) DecideAndReserve(context.Context, string, predictiveShadowInput) predictiveShadowReservation {
	return nil
}

func (s *realPredictiveShadow) ObserveOutcome(requestID string, outcome runtimepredictive.SchedulerOutcome) bool {
	if s == nil || s.coordinator == nil {
		return false
	}
	return s.coordinator.ObserveOutcome(requestID, outcome)
}

func (s *realPredictiveShadow) Snapshot() predictiveAttemptSnapshot {
	if s == nil {
		return predictiveAttemptSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

func (s *realPredictiveShadow) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (r *realPredictiveReservation) MarkPrefillComplete() bool {
	if r == nil || r.owner == nil {
		return false
	}
	return r.owner.coordinator.MarkPrefillComplete(r.requestID)
}

func (r *realPredictiveReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	if r == nil || r.owner == nil {
		return false
	}
	return r.owner.coordinator.Terminate(r.requestID, cause)
}
