package server

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveUpperBoundCoordinator interface {
	DecideUpperBoundAndReserve(time.Time, runtimepredictive.UpperBoundAdmissionProposal) runtimepredictive.CountAdmissionResult
	MarkForwarded(string) bool
	MarkPrefillComplete(string) bool
	Terminate(string, runtimepredictive.TerminalCause) bool
	TerminateWithOutcome(string, runtimepredictive.TerminalCause, *runtimepredictive.SchedulerOutcome) bool
}

type predictiveShadowObservationCoordinator interface {
	ObserveUnreservedOutcome(runtimepredictive.SchedulerPrediction, runtimepredictive.TerminalCause, bool, runtimepredictive.SchedulerOutcome) bool
	MarkLiveOutcomesInterfered() int
}

const defaultMaximumShadowObservations = 256

type approximatePredictiveShadowConfig struct {
	Calibrator             *runtimepredictive.InputSizeCalibrator
	Coordinator            predictiveUpperBoundCoordinator
	Learner                predictiveLearningSnapshotter
	Upstream               predictiveUpstreamState
	Mode                   string
	ShadowObservationLimit int
	Now                    func() time.Time
}

type approximatePredictiveShadow struct {
	mu                  sync.Mutex
	calibrator          *runtimepredictive.InputSizeCalibrator
	coordinator         predictiveUpperBoundCoordinator
	learner             predictiveLearningSnapshotter
	upstream            predictiveUpstreamState
	mode                string
	maximumObservations int
	now                 func() time.Time
	closed              bool
	attempts            predictiveAttemptSnapshot
	reservations        map[string]struct{}
	observations        map[string]*approximatePredictiveReservation
	observationStats    predictiveShadowObservationSnapshot
	predictionDuration  durationHistogram
	tpsOutcomes         predictiveTPSOutcomeSnapshot
}

type approximatePredictiveReservation struct {
	owner                       *approximatePredictiveShadow
	requestID                   string
	identity                    runtimepredictive.ModelIdentity
	prediction                  runtimepredictive.SchedulerPrediction
	sizeEstimate                runtimepredictive.InputSizeEstimate
	decodeHorizonUpper          int64
	observationOnly             bool
	outcomeInterfered           bool
	prefillComplete             bool
	semanticTTFT                time.Duration
	semanticTTFTValid           bool
	completion                  predictiveCompletionObservation
	completionObserved          bool
	completionStructurallyValid bool
	forwarded                   bool
}

func newApproximatePredictiveShadow(config approximatePredictiveShadowConfig) (*approximatePredictiveShadow, error) {
	if config.Calibrator == nil {
		return nil, fmt.Errorf("predictive input-size calibrator is required")
	}
	if config.Coordinator == nil {
		return nil, fmt.Errorf("predictive upper-bound coordinator is required")
	}
	mode := strings.ToLower(strings.TrimSpace(config.Mode))
	if mode != "shadow" && mode != "enforce" {
		return nil, fmt.Errorf("predictive adapter mode must be shadow or enforce")
	}
	maximumObservations := config.ShadowObservationLimit
	if maximumObservations == 0 {
		maximumObservations = defaultMaximumShadowObservations
	}
	if maximumObservations < 0 {
		return nil, fmt.Errorf("predictive shadow observation bound must be non-negative")
	}
	if mode == "shadow" {
		if _, ok := config.Coordinator.(predictiveShadowObservationCoordinator); !ok {
			return nil, fmt.Errorf("predictive shadow observation coordinator is required")
		}
		if maximumObservations == 0 {
			return nil, fmt.Errorf("predictive shadow observation bound must be positive")
		}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &approximatePredictiveShadow{
		calibrator:          config.Calibrator,
		coordinator:         config.Coordinator,
		learner:             config.Learner,
		upstream:            config.Upstream,
		mode:                mode,
		maximumObservations: maximumObservations,
		now:                 config.Now,
		reservations:        make(map[string]struct{}),
		observations:        make(map[string]*approximatePredictiveReservation),
		predictionDuration:  newDurationHistogram(),
	}, nil
}

func (s *approximatePredictiveShadow) DecideAndReserve(ctx context.Context, requestID string, input predictiveShadowInput) predictiveShadowReservation {
	if s == nil || requestID == "" {
		return nil
	}
	started := time.Now()
	defer func() { s.predictionDuration.Observe(time.Since(started)) }()
	if err := ctx.Err(); err != nil {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	if !input.Cost.Supported {
		s.recordUnknown(domainpredictive.ReasonRequestSizeUnknown)
		return nil
	}
	class, ok := approximateRequestClass(input.Path)
	if !ok {
		s.recordUnknown(domainpredictive.ReasonRequestSizeUnknown)
		return nil
	}

	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	decisionTime := s.now()
	if s.upstream != nil && !s.upstream.Healthy(decisionTime) {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	size := s.calibrator.Estimate(decisionTime, class, input.Cost.EstimatedInputLow, input.Cost.EstimatedInputHigh)
	if !size.Known {
		s.recordUnknown(domainpredictive.ReasonRequestSizeUnknown)
		return nil
	}
	decodeUpper, ok := approximateDecodeUpper(input)
	if !ok {
		s.recordUnknown(domainpredictive.ReasonRequestSizeUnknown)
		return nil
	}
	if err := ctx.Err(); err != nil {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	if s.upstream != nil && !s.upstream.Healthy(s.now()) {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return nil
	}
	admissionStarted := input.RequestStartedAt
	if admissionStarted.IsZero() {
		admissionStarted = started
	}
	result := s.coordinator.DecideUpperBoundAndReserve(decisionTime, runtimepredictive.UpperBoundAdmissionProposal{
		RequestID:                    requestID,
		InputTokensUpper:             size.InputTokensUpper,
		RawInputTokensHigh:           size.RawInputTokensHigh,
		DecodeHorizonUpper:           decodeUpper,
		AccruedLocalAdmissionLatency: time.Since(admissionStarted),
		Confidence:                   size.Confidence,
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
		if s.mode != "shadow" || !validShadowObservationResult(result) {
			s.mu.Unlock()
			return nil
		}
		if len(s.observations) >= s.maximumObservations {
			s.observationStats.Dropped++
			s.mu.Unlock()
			return nil
		}
		observation := &approximatePredictiveReservation{
			owner:              s,
			requestID:          requestID,
			identity:           result.Prediction.Identity,
			prediction:         result.Prediction,
			sizeEstimate:       size,
			decodeHorizonUpper: decodeUpper,
			observationOnly:    true,
		}
		s.observations[requestID] = observation
		s.observationStats.Created++
		s.mu.Unlock()
		return observation
	}
	s.markShadowObservationsInterferedLocked("")
	s.reservations[requestID] = struct{}{}
	s.mu.Unlock()
	return &approximatePredictiveReservation{
		owner:              s,
		requestID:          requestID,
		identity:           result.Prediction.Identity,
		prediction:         result.Prediction,
		sizeEstimate:       size,
		decodeHorizonUpper: decodeUpper,
	}
}

func (s *approximatePredictiveShadow) PredictiveAdmissionTelemetry() predictiveAdmissionTelemetrySnapshot {
	if s == nil {
		return predictiveAdmissionTelemetrySnapshot{}
	}
	s.mu.Lock()
	attempts := s.attempts
	tpsOutcomes := s.tpsOutcomes
	observationStats := s.observationStats
	observationStats.Active = len(s.observations)
	s.mu.Unlock()
	telemetry := predictiveAdmissionTelemetrySnapshot{
		Attempts:           attempts,
		PredictionDuration: &s.predictionDuration,
		TPSOutcomes:        tpsOutcomes,
		ShadowObservations: observationStats,
	}
	telemetry.InputSize = s.calibrator.Snapshot(s.now())
	if coordinator, ok := s.coordinator.(predictiveCoordinatorSnapshotter); ok {
		telemetry.Manager = coordinator.Snapshot().Manager
	}
	if s.learner != nil {
		telemetry.Learning = s.learner.Snapshot()
	}
	return telemetry
}

func (s *approximatePredictiveShadow) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	failed := 0
	for requestID := range s.reservations {
		if !s.coordinator.Terminate(requestID, runtimepredictive.TerminalExpired) {
			failed++
		}
		delete(s.reservations, requestID)
	}
	s.observationStats.Terminated += uint64(len(s.observations))
	clear(s.observations)
	upstream := s.upstream
	s.mu.Unlock()
	var closeErr error
	if upstream != nil {
		closeErr = upstream.Close()
	}
	if failed > 0 {
		if closeErr != nil {
			return fmt.Errorf("expire %d predictive reservations during close; close upstream: %v", failed, closeErr)
		}
		return fmt.Errorf("expire %d predictive reservations during close", failed)
	}
	return closeErr
}

func (r *approximatePredictiveReservation) MarkForwarded() bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.owner.closed || r.forwarded || !r.activeLocked() {
		return false
	}
	if r.observationOnly {
		r.owner.markShadowObservationsInterferedLocked(r.requestID)
		observer := r.owner.coordinator.(predictiveShadowObservationCoordinator)
		observer.MarkLiveOutcomesInterfered()
	} else if !r.owner.coordinator.MarkForwarded(r.requestID) {
		return false
	}
	r.forwarded = true
	return true
}

func (r *approximatePredictiveReservation) MarkPrefillComplete() bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.owner.closed || !r.forwarded || r.prefillComplete || !r.activeLocked() {
		return false
	}
	if !r.observationOnly && !r.owner.coordinator.MarkPrefillComplete(r.requestID) {
		return false
	}
	r.prefillComplete = true
	return true
}

func (r *approximatePredictiveReservation) ObserveSemanticTTFT(ttft time.Duration) bool {
	if r == nil || r.owner == nil || ttft <= 0 {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.owner.closed || !r.forwarded || r.semanticTTFTValid || !r.activeLocked() {
		return false
	}
	r.semanticTTFT = ttft
	r.semanticTTFTValid = true
	return true
}

func (r *approximatePredictiveReservation) ObserveCompletion(observation predictiveCompletionObservation) bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.owner.closed || !r.forwarded || r.completionObserved || !r.activeLocked() {
		return false
	}
	r.completionObserved = true
	r.completion = observation
	r.completionStructurallyValid = validPredictiveCompletionObservation(observation, r.decodeHorizonUpper)
	return observation.PromptTokens > 0 || r.completionStructurallyValid
}

func (r *approximatePredictiveReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	if !r.activeLocked() {
		r.owner.mu.Unlock()
		return false
	}
	now := r.owner.now()
	var schedulerOutcome *runtimepredictive.SchedulerOutcome
	var sizeOutcome *runtimepredictive.InputSizeOutcome
	if cause == runtimepredictive.TerminalCompleted {
		completed := runtimepredictive.SchedulerOutcome{
			Identity: r.identity, ObservedAt: now, Attributed: true,
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
			schedulerOutcome = &completed
		}
		if r.forwarded && r.completionObserved && r.completion.PromptTokens > 0 {
			sizeOutcome = &runtimepredictive.InputSizeOutcome{
				EstimatorVersion:   r.sizeEstimate.EstimatorVersion,
				Class:              r.sizeEstimate.Class,
				RawInputTokensHigh: r.sizeEstimate.RawInputTokensHigh,
				ActualPromptTokens: r.completion.PromptTokens,
				ObservedAt:         now,
				Attributed:         true,
			}
		}
	} else if r.forwarded {
		r.owner.tpsOutcomes.Rejected++
	}

	if r.observationOnly {
		delete(r.owner.observations, r.requestID)
		r.owner.observationStats.Terminated++
		if schedulerOutcome != nil && r.outcomeInterfered {
			schedulerOutcome.Censored = true
			r.owner.observationStats.Censored++
		}
		r.owner.mu.Unlock()

		if schedulerOutcome != nil {
			observer := r.owner.coordinator.(predictiveShadowObservationCoordinator)
			if observer.ObserveUnreservedOutcome(r.prediction, cause, r.forwarded, *schedulerOutcome) {
				r.owner.mu.Lock()
				r.owner.observationStats.Qualified++
				r.owner.mu.Unlock()
			}
		}
		if sizeOutcome != nil {
			_ = r.owner.calibrator.Observe(*sizeOutcome)
		}
		return true
	}

	terminated := false
	if schedulerOutcome != nil {
		terminated = r.owner.coordinator.TerminateWithOutcome(r.requestID, cause, schedulerOutcome)
	} else {
		terminated = r.owner.coordinator.Terminate(r.requestID, cause)
	}
	if !terminated {
		r.owner.mu.Unlock()
		return false
	}
	delete(r.owner.reservations, r.requestID)
	r.owner.mu.Unlock()

	if sizeOutcome != nil {
		_ = r.owner.calibrator.Observe(*sizeOutcome)
	}
	return true
}

func (r *approximatePredictiveReservation) activeLocked() bool {
	if r == nil || r.owner == nil {
		return false
	}
	if r.observationOnly {
		return r.owner.observations[r.requestID] == r
	}
	_, ok := r.owner.reservations[r.requestID]
	return ok
}

func (s *approximatePredictiveShadow) markShadowObservationsInterferedLocked(exceptRequestID string) {
	for requestID, observation := range s.observations {
		if requestID == exceptRequestID || observation == nil || observation.outcomeInterfered {
			continue
		}
		observation.outcomeInterfered = true
	}
}

func validShadowObservationResult(result runtimepredictive.CountAdmissionResult) bool {
	prediction := result.Prediction
	return !result.Reserved && result.Decision.Reason != domainpredictive.ReasonFit &&
		result.Cost.ManifestID != "" && result.Cost.BackendEpoch != "" &&
		prediction.Identity.Validate() == nil && prediction.Identity.BackendEpoch == result.Cost.BackendEpoch &&
		prediction.Source != "" && prediction.Confidence > 0 && prediction.Confidence <= 1 &&
		!math.IsNaN(prediction.Confidence) && !math.IsInf(prediction.Confidence, 0)
}

func (r *approximatePredictiveReservation) qualifiedTPS() (float64, time.Duration, predictiveTPSTargetSource, bool) {
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
	tpot := dividePositiveDuration(decodeDuration, intervals)
	if tpot <= 0 {
		return 0, 0, predictiveTPSTargetNone, false
	}
	return tps, tpot, source, true
}

func (r *approximatePredictiveReservation) recordTPSOutcomeLocked(source predictiveTPSTargetSource, valid bool) {
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

func (s *approximatePredictiveShadow) recordUnknown(reason domainpredictive.Reason) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.attempts.Attempts++
	s.attempts.Unknown++
	s.attempts.LastReason = reason
	s.attempts.LastSource = ""
	s.attempts.LastSamples = 0
	s.mu.Unlock()
}

func (s *approximatePredictiveShadow) recordResultLocked(result runtimepredictive.CountAdmissionResult) {
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
		domainpredictive.ReasonRequestSizeUnknown,
		domainpredictive.ReasonPredictorProfileUnknown:
		s.attempts.Unknown++
	default:
		s.attempts.Risks++
	}
}

func approximateRequestClass(path string) (runtimepredictive.RequestClass, bool) {
	trimmed := strings.TrimRight(path, "/")
	switch {
	case strings.HasSuffix(trimmed, "/chat/completions"):
		return runtimepredictive.RequestClassChat, true
	case strings.HasSuffix(trimmed, "/completions"):
		return runtimepredictive.RequestClassCompletion, true
	case strings.HasSuffix(trimmed, "/responses"):
		return runtimepredictive.RequestClassResponses, true
	default:
		return "", false
	}
}

func approximateDecodeUpper(input predictiveShadowInput) (int64, bool) {
	decode := input.Cost.BoundedDecodeTokens
	if input.Cost.HasMaxOutputTokens {
		if input.Cost.MaxOutputTokens < 0 {
			return 0, false
		}
		decode = int64(input.Cost.MaxOutputTokens)
	}
	return decode, decode >= 0
}
