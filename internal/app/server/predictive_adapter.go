package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveRenderedRequest struct {
	Class              runtimepredictive.RequestClass
	Rendered           []byte
	DecodeHorizonUpper int64
	Confidence         float64
}

type predictiveRequestRenderer interface {
	Render(context.Context, predictiveShadowInput) (predictiveRenderedRequest, error)
}

type predictiveTokenCounter interface {
	Count(context.Context, runtimepredictive.RequestClass, []byte) (runtimepredictive.TokenCountAnalysis, error)
	Close() error
}

type predictiveUpstreamState interface {
	Healthy(time.Time) bool
	Close() error
}

type predictiveAdmissionCoordinator interface {
	DecideAndReserve(time.Time, runtimepredictive.CountAdmissionProposal) runtimepredictive.CountAdmissionResult
	MarkForwarded(string) bool
	MarkPrefillComplete(string) bool
	Terminate(string, runtimepredictive.TerminalCause) bool
	TerminateWithOutcome(string, runtimepredictive.TerminalCause, *runtimepredictive.SchedulerOutcome) bool
}

type predictiveCoordinatorSnapshotter interface {
	Snapshot() runtimepredictive.CountCoordinatorSnapshot
}

type predictiveLearningSnapshotter interface {
	Snapshot() runtimepredictive.LearnedSchedulerSnapshot
}

type realPredictiveShadowConfig struct {
	Renderer    predictiveRequestRenderer
	Counter     predictiveTokenCounter
	Coordinator predictiveAdmissionCoordinator
	Learner     predictiveLearningSnapshotter
	Upstream    predictiveUpstreamState
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
	mu                 sync.Mutex
	renderer           predictiveRequestRenderer
	counter            predictiveTokenCounter
	coordinator        predictiveAdmissionCoordinator
	learner            predictiveLearningSnapshotter
	upstream           predictiveUpstreamState
	now                func() time.Time
	closed             bool
	attempts           predictiveAttemptSnapshot
	reservations       map[string]struct{}
	predictionDuration durationHistogram
	rendererDuration   durationHistogram
	tokenizerDuration  durationHistogram
}

type realPredictiveReservation struct {
	owner             *realPredictiveShadow
	requestID         string
	identity          runtimepredictive.ModelIdentity
	semanticTTFT      time.Duration
	semanticTTFTValid bool
	forwarded         bool
}

func newRealPredictiveShadow(config realPredictiveShadowConfig) (*realPredictiveShadow, error) {
	if config.Renderer == nil {
		return nil, fmt.Errorf("predictive request renderer is required")
	}
	if config.Counter == nil {
		return nil, fmt.Errorf("predictive token counter is required")
	}
	if config.Coordinator == nil {
		return nil, fmt.Errorf("predictive coordinator is required")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &realPredictiveShadow{
		renderer:           config.Renderer,
		counter:            config.Counter,
		coordinator:        config.Coordinator,
		learner:            config.Learner,
		upstream:           config.Upstream,
		now:                config.Now,
		reservations:       make(map[string]struct{}),
		predictionDuration: newDurationHistogram(),
		rendererDuration:   newDurationHistogram(),
		tokenizerDuration:  newDurationHistogram(),
	}, nil
}

func (s *realPredictiveShadow) DecideAndReserve(ctx context.Context, requestID string, input predictiveShadowInput) predictiveShadowReservation {
	if s == nil || requestID == "" {
		return nil
	}
	predictionStarted := time.Now()
	defer func() { s.predictionDuration.Observe(time.Since(predictionStarted)) }()
	admissionStarted := input.RequestStartedAt
	if admissionStarted.IsZero() {
		admissionStarted = predictionStarted
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
	if s.upstream != nil && !s.upstream.Healthy(s.now()) {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}

	rendererStarted := time.Now()
	rendered, err := s.renderer.Render(ctx, input)
	s.rendererDuration.Observe(time.Since(rendererStarted))
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
	tokenizerStarted := time.Now()
	analysis, err := s.counter.Count(ctx, rendered.Class, rendered.Rendered)
	s.tokenizerDuration.Observe(time.Since(tokenizerStarted))
	if err != nil {
		s.recordUnknown(domainpredictive.ReasonTokenizerProfileUnknown)
		return nil
	}
	if err := ctx.Err(); err != nil {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	decisionTime := s.now()
	if s.upstream != nil && !s.upstream.Healthy(decisionTime) {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}

	result := s.coordinator.DecideAndReserve(decisionTime, runtimepredictive.CountAdmissionProposal{
		RequestID:                    requestID,
		Analysis:                     analysis,
		DecodeHorizonUpper:           rendered.DecodeHorizonUpper,
		AccruedLocalAdmissionLatency: time.Since(admissionStarted),
		Confidence:                   rendered.Confidence,
	})
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		if result.Reserved {
			s.coordinator.Terminate(requestID, runtimepredictive.TerminalExpired)
		}
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	s.recordResultLocked(result)
	if !result.Reserved {
		s.mu.Unlock()
		return nil
	}
	s.reservations[requestID] = struct{}{}
	s.mu.Unlock()
	return &realPredictiveReservation{
		owner:     s,
		requestID: requestID,
		identity:  result.Prediction.Identity,
	}
}

func (s *realPredictiveShadow) Snapshot() predictiveAttemptSnapshot {
	if s == nil {
		return predictiveAttemptSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attempts
}

func (s *realPredictiveShadow) PredictiveAdmissionTelemetry() predictiveAdmissionTelemetrySnapshot {
	if s == nil {
		return predictiveAdmissionTelemetrySnapshot{}
	}
	s.mu.Lock()
	attempts := s.attempts
	s.mu.Unlock()
	telemetry := predictiveAdmissionTelemetrySnapshot{
		Attempts:           attempts,
		PredictionDuration: &s.predictionDuration,
		RendererDuration:   &s.rendererDuration,
		TokenizerDuration:  &s.tokenizerDuration,
	}
	if coordinator, ok := s.coordinator.(predictiveCoordinatorSnapshotter); ok {
		telemetry.Manager = coordinator.Snapshot().Manager
	}
	if s.learner != nil {
		telemetry.Learning = s.learner.Snapshot()
	}
	return telemetry
}

func (s *realPredictiveShadow) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	for requestID := range s.reservations {
		if s.coordinator.Terminate(requestID, runtimepredictive.TerminalExpired) {
			delete(s.reservations, requestID)
		}
	}
	upstream := s.upstream
	counter := s.counter
	s.mu.Unlock()
	var closeErrors []error
	if upstream != nil {
		closeErrors = append(closeErrors, upstream.Close())
	}
	closeErrors = append(closeErrors, counter.Close())
	return errors.Join(closeErrors...)
}

func (r *realPredictiveReservation) MarkForwarded() bool {
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
	if !r.owner.coordinator.MarkForwarded(r.requestID) {
		return false
	}
	r.forwarded = true
	return true
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

func (r *realPredictiveReservation) ObserveSemanticTTFT(ttft time.Duration) bool {
	if r == nil || r.owner == nil || ttft <= 0 {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.owner.closed || r.semanticTTFTValid {
		return false
	}
	if _, exists := r.owner.reservations[r.requestID]; !exists {
		return false
	}
	r.semanticTTFT = ttft
	r.semanticTTFTValid = true
	return true
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
	var outcome *runtimepredictive.SchedulerOutcome
	if cause == runtimepredictive.TerminalCompleted && r.semanticTTFTValid {
		outcome = &runtimepredictive.SchedulerOutcome{
			Identity:   r.identity,
			ObservedAt: r.owner.now(),
			Attributed: true,
			TTFT:       r.semanticTTFT,
			TTFTValid:  true,
		}
	} else if r.forwarded {
		outcome = &runtimepredictive.SchedulerOutcome{
			Identity:   r.identity,
			ObservedAt: r.owner.now(),
			Attributed: true,
			Censored:   true,
		}
	}
	if !r.owner.coordinator.TerminateWithOutcome(r.requestID, cause, outcome) {
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

func (s *realPredictiveShadow) recordResultLocked(result runtimepredictive.CountAdmissionResult) {
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
		domainpredictive.ReasonPredictorProfileUnknown:
		s.attempts.Unknown++
	default:
		s.attempts.Risks++
	}
}
