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
	Risks       uint64
	Unknown     uint64
	LastReason  domainpredictive.Reason
	LastSource  runtimepredictive.PredictionSource
	LastSamples int
}

type realPredictiveShadow struct {
	mu           sync.Mutex
	renderer     predictiveRequestRenderer
	analyzer     predictiveTokenAnalyzer
	coordinator  *runtimepredictive.Coordinator
	now          func() time.Time
	closed       bool
	attempts     predictiveAttemptSnapshot
	reservations map[string]struct{}
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
		renderer:     config.Renderer,
		analyzer:     config.Analyzer,
		coordinator:  config.Coordinator,
		now:          config.Now,
		reservations: make(map[string]struct{}),
	}, nil
}

func (s *realPredictiveShadow) DecideAndReserve(ctx context.Context, requestID string, input predictiveShadowInput) predictiveShadowReservation {
	if s == nil || requestID == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}

	rendered, err := s.renderer.Render(ctx, input)
	if rendered.Rendered != nil {
		defer clear(rendered.Rendered)
	}
	if err != nil {
		s.recordUnknown(domainpredictive.ReasonTokenizerProfileUnknown)
		return nil
	}
	if err := ctx.Err(); err != nil {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	analysis, err := s.analyzer.Analyze(ctx, rendered.Class, rendered.Rendered, rendered.Features)
	if err != nil {
		s.recordUnknown(domainpredictive.ReasonTokenizerProfileUnknown)
		return nil
	}
	if err := ctx.Err(); err != nil {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		s.recordUnknownLocked(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	result := s.coordinator.DecideAndReserve(s.now(), runtimepredictive.AdmissionProposal{
		RequestID:          requestID,
		Analysis:           analysis,
		DecodeHorizonUpper: rendered.DecodeHorizonUpper,
		Confidence:         rendered.Confidence,
	})
	s.recordResultLocked(result)
	if !result.Reserved {
		return nil
	}
	s.reservations[requestID] = struct{}{}
	return &realPredictiveReservation{owner: s, requestID: requestID}
}

func (s *realPredictiveShadow) ObserveOutcome(requestID string, outcome runtimepredictive.SchedulerOutcome) bool {
	if s == nil || s.coordinator == nil || requestID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	if _, exists := s.reservations[requestID]; !exists {
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
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for requestID := range s.reservations {
		if s.coordinator.Terminate(requestID, runtimepredictive.TerminalExpired) {
			delete(s.reservations, requestID)
		}
	}
	return nil
}

func (r *realPredictiveReservation) MarkPrefillComplete() bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.owner.closed {
		return false
	}
	if _, exists := r.owner.reservations[r.requestID]; !exists {
		return false
	}
	return r.owner.coordinator.MarkPrefillComplete(r.requestID)
}

func (r *realPredictiveReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if _, exists := r.owner.reservations[r.requestID]; !exists {
		return false
	}
	if !r.owner.coordinator.Terminate(r.requestID, cause) {
		return false
	}
	delete(r.owner.reservations, r.requestID)
	return true
}

func (s *realPredictiveShadow) recordUnknown(reason domainpredictive.Reason) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordUnknownLocked(reason)
}

func (s *realPredictiveShadow) recordUnknownLocked(reason domainpredictive.Reason) {
	s.attempts.Attempts++
	s.attempts.Unknown++
	s.attempts.LastReason = reason
	s.attempts.LastSource = ""
	s.attempts.LastSamples = 0
}

func (s *realPredictiveShadow) recordResultLocked(result runtimepredictive.AdmissionResult) {
	s.attempts.Attempts++
	s.attempts.LastReason = result.Decision.Reason
	s.attempts.LastSource = result.Prediction.Source
	s.attempts.LastSamples = result.Prediction.Samples
	if result.Reserved {
		s.attempts.Fits++
		return
	}
	switch result.Decision.Reason {
	case domainpredictive.ReasonTokenizerProfileUnknown,
		domainpredictive.ReasonCacheStateUnknown,
		domainpredictive.ReasonPredictorProfileUnknown:
		s.attempts.Unknown++
	default:
		s.attempts.Risks++
	}
}
