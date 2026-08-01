package server

import (
	"context"
	"errors"
	"fmt"
	"math"
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
	tpsOutcomes        predictiveTPSOutcomeSnapshot
}

type predictiveTPSTargetSource uint8

const (
	predictiveTPSTargetNone predictiveTPSTargetSource = iota
	predictiveTPSTargetBackend
	predictiveTPSTargetLocal
)

type realPredictiveReservation struct {
	owner                       *realPredictiveShadow
	requestID                   string
	identity                    runtimepredictive.ModelIdentity
	decodeHorizonUpper          int64
	semanticTTFT                time.Duration
	semanticTTFTValid           bool
	completion                  predictiveCompletionObservation
	completionObserved          bool
	completionStructurallyValid bool
	forwarded                   bool
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
		owner:              s,
		requestID:          requestID,
		identity:           result.Prediction.Identity,
		decodeHorizonUpper: rendered.DecodeHorizonUpper,
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
	tpsOutcomes := s.tpsOutcomes
	s.mu.Unlock()
	telemetry := predictiveAdmissionTelemetrySnapshot{
		Attempts:           attempts,
		PredictionDuration: &s.predictionDuration,
		RendererDuration:   &s.rendererDuration,
		TokenizerDuration:  &s.tokenizerDuration,
		TPSOutcomes:        tpsOutcomes,
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
	if r.owner.closed || !r.forwarded || r.semanticTTFTValid {
		return false
	}
	if _, exists := r.owner.reservations[r.requestID]; !exists {
		return false
	}
	r.semanticTTFT = ttft
	r.semanticTTFTValid = true
	return true
}

func (r *realPredictiveReservation) ObserveCompletion(observation predictiveCompletionObservation) bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.owner.closed || !r.forwarded || r.completionObserved {
		return false
	}
	if _, exists := r.owner.reservations[r.requestID]; !exists {
		return false
	}
	r.completionObserved = true
	r.completion = observation
	r.completionStructurallyValid = validPredictiveCompletionObservation(observation, r.decodeHorizonUpper)
	return r.completionStructurallyValid
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
	if cause == runtimepredictive.TerminalCompleted {
		completed := runtimepredictive.SchedulerOutcome{
			Identity:   r.identity,
			ObservedAt: r.owner.now(),
			Attributed: true,
		}
		hasTarget := false
		if r.semanticTTFTValid {
			completed.TTFT = r.semanticTTFT
			completed.TTFTValid = true
			hasTarget = true
		}
		tps, tpot, source, tpsValid := r.qualifiedTPS()
		if tpsValid {
			completed.UserTPS = tps
			completed.UserTPSValid = true
			completed.TPOT = tpot
			completed.TPOTValid = true
			hasTarget = true
		}
		if r.forwarded {
			r.recordTPSOutcomeLocked(source, tpsValid)
		}
		if hasTarget {
			outcome = &completed
		}
	}
	if cause != runtimepredictive.TerminalCompleted && r.forwarded {
		r.owner.tpsOutcomes.Rejected++
	}
	if outcome == nil && r.forwarded {
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

func (r *realPredictiveReservation) qualifiedTPS() (float64, time.Duration, predictiveTPSTargetSource, bool) {
	if r == nil || !r.completionObserved || !r.completionStructurallyValid {
		return 0, 0, predictiveTPSTargetNone, false
	}
	intervals := r.completion.CompletionTokens - 1
	decodeDuration := time.Duration(0)
	source := predictiveTPSTargetNone
	if r.completion.BackendMeanITL > 0 {
		decodeDuration = multiplyPositiveDuration(r.completion.BackendMeanITL, intervals)
		source = predictiveTPSTargetBackend
	} else if r.completion.BackendGenerationTime > 0 {
		decodeDuration = r.completion.BackendGenerationTime
		source = predictiveTPSTargetBackend
	} else if r.semanticTTFTValid && r.completion.ElapsedSinceRequest > r.semanticTTFT {
		decodeDuration = r.completion.ElapsedSinceRequest - r.semanticTTFT
		source = predictiveTPSTargetLocal
	}
	if decodeDuration <= 0 {
		return 0, 0, predictiveTPSTargetNone, false
	}
	tps := float64(intervals) / decodeDuration.Seconds()
	if tps <= 0 || math.IsNaN(tps) || math.IsInf(tps, 0) {
		return 0, 0, predictiveTPSTargetNone, false
	}
	tapot := dividePositiveDuration(decodeDuration, intervals)
	if tapot <= 0 {
		return 0, 0, predictiveTPSTargetNone, false
	}
	return tps, tapot, source, true
}

func (r *realPredictiveReservation) recordTPSOutcomeLocked(source predictiveTPSTargetSource, valid bool) {
	if r == nil || r.owner == nil {
		return
	}
	if valid {
		switch source {
		case predictiveTPSTargetBackend:
			r.owner.tpsOutcomes.Backend++
		case predictiveTPSTargetLocal:
			r.owner.tpsOutcomes.Local++
		default:
			r.owner.tpsOutcomes.Rejected++
		}
		return
	}
	if r.completionObserved && !r.completionStructurallyValid {
		r.owner.tpsOutcomes.Rejected++
		return
	}
	r.owner.tpsOutcomes.Missing++
}

func validPredictiveCompletionObservation(observation predictiveCompletionObservation, decodeHorizonUpper int64) bool {
	if observation.CompletionTokens <= 1 || decodeHorizonUpper <= 0 || observation.CompletionTokens > decodeHorizonUpper || observation.ElapsedSinceRequest <= 0 || observation.BackendMeanITL < 0 || observation.BackendGenerationTime < 0 {
		return false
	}
	if observation.BackendMeanITL == 0 || observation.BackendGenerationTime == 0 {
		return true
	}
	expected := multiplyPositiveDuration(observation.BackendMeanITL, observation.CompletionTokens-1)
	if expected <= 0 {
		return false
	}
	difference := expected - observation.BackendGenerationTime
	if difference < 0 {
		difference = -difference
	}
	tolerance := expected / 10
	if tolerance < 2*time.Millisecond {
		tolerance = 2 * time.Millisecond
	}
	return difference <= tolerance
}

func multiplyPositiveDuration(value time.Duration, count int64) time.Duration {
	if value <= 0 || count <= 0 || count > math.MaxInt64/int64(value) {
		return 0
	}
	return value * time.Duration(count)
}

func dividePositiveDuration(value time.Duration, divisor int64) time.Duration {
	if value <= 0 || divisor <= 0 {
		return 0
	}
	result := value / time.Duration(divisor)
	if result <= 0 {
		return time.Nanosecond
	}
	return result
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
