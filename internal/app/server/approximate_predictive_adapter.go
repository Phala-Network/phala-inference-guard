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
	ReleaseResources(string) runtimepredictive.ResourceReleaseResult
	Terminate(string, runtimepredictive.TerminalCause) bool
	TerminateWithOutcome(string, runtimepredictive.TerminalCause, *runtimepredictive.SchedulerOutcome) bool
}

type predictiveUnreservedOutcomeCoordinator interface {
	ObserveUnreservedOutcome(runtimepredictive.SchedulerPrediction, runtimepredictive.TerminalCause, bool, runtimepredictive.SchedulerOutcome) bool
}

type predictiveShadowObservationCoordinator interface {
	predictiveUnreservedOutcomeCoordinator
	MarkLiveOutcomesInterfered() int
}

const (
	defaultMaximumShadowObservations = 256
	defaultMaximumDeferredOutcomes   = 256
)

type approximatePredictiveShadowConfig struct {
	Calibrator             *runtimepredictive.InputSizeCalibrator
	Coordinator            predictiveUpperBoundCoordinator
	Learner                predictiveLearningSnapshotter
	Upstream               predictiveUpstreamState
	Mode                   string
	ShadowObservationLimit int
	DeferredOutcomeLimit   int
	RouterBackpressureHold time.Duration
	RouterBackpressure     predictiveRouterBackpressurePolicy
	OnRouterBackpressure   func(predictiveRouterBackpressureEvent)
	OnRequestReject        func(predictiveRequestRejectEvent)
	ShadowPendingPrefills  *predictiveShadowPendingPrefillStore
	Now                    func() time.Time
}

type approximatePredictiveShadow struct {
	mu                  sync.Mutex
	learningOutcomes    sync.WaitGroup
	calibrator          *runtimepredictive.InputSizeCalibrator
	coordinator         predictiveUpperBoundCoordinator
	learner             predictiveLearningSnapshotter
	upstream            predictiveUpstreamState
	mode                string
	maximumObservations int
	maximumDeferred     int
	now                 func() time.Time
	closed              bool
	closeDone           chan struct{}
	closeErr            error
	attempts            predictiveAttemptSnapshot
	reservations        map[string]*approximatePredictiveReservation
	observations        map[string]*approximatePredictiveReservation
	deferredOutcomes    map[string]*approximatePredictiveReservation
	observationStats    predictiveShadowObservationSnapshot
	deferredStats       predictiveDeferredOutcomeSnapshot
	predictionDuration  durationHistogram
	tpsOutcomes         predictiveTPSOutcomeSnapshot
	routerBackpressure  predictiveRouterBackpressureState
	requestRejectLogs   predictiveRequestRejectLogState
	backpressurePolicy  predictiveRouterBackpressurePolicy
	backpressureHold    time.Duration
	onBackpressure      func(predictiveRouterBackpressureEvent)
	onRequestReject     func(predictiveRequestRejectEvent)
	shadowPrefills      *predictiveShadowPendingPrefillStore
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
	resourcesReleased           bool
	deferredOutcomeDropped      bool
	shadowPrefill               *predictiveShadowPrefillReservation
}

type predictiveShadowPrefillReservation struct {
	observation runtimepredictive.PendingPrefillObservation
	handle      *predictiveShadowPendingPrefillHandle
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
	if _, ok := config.Coordinator.(predictiveUnreservedOutcomeCoordinator); !ok {
		return nil, fmt.Errorf("predictive unreserved outcome coordinator is required")
	}
	maximumDeferred := config.DeferredOutcomeLimit
	if maximumDeferred == 0 {
		maximumDeferred = defaultMaximumDeferredOutcomes
	}
	if maximumDeferred < 0 {
		return nil, fmt.Errorf("predictive deferred outcome bound must be non-negative")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if mode == "shadow" && config.ShadowPendingPrefills == nil {
		config.ShadowPendingPrefills = newPredictiveShadowPendingPrefillStore(maximumObservations)
	}
	config.RouterBackpressureHold = normalizePredictiveRouterBackpressureHold(config.RouterBackpressureHold)
	return &approximatePredictiveShadow{
		calibrator:          config.Calibrator,
		coordinator:         config.Coordinator,
		learner:             config.Learner,
		upstream:            config.Upstream,
		mode:                mode,
		maximumObservations: maximumObservations,
		maximumDeferred:     maximumDeferred,
		now:                 config.Now,
		closeDone:           make(chan struct{}),
		reservations:        make(map[string]*approximatePredictiveReservation),
		observations:        make(map[string]*approximatePredictiveReservation),
		deferredOutcomes:    make(map[string]*approximatePredictiveReservation),
		predictionDuration:  newPredictiveDurationHistogram(),
		backpressurePolicy:  config.RouterBackpressure,
		backpressureHold:    config.RouterBackpressureHold,
		onBackpressure:      config.OnRouterBackpressure,
		onRequestReject:     config.OnRequestReject,
		shadowPrefills:      config.ShadowPendingPrefills,
	}, nil
}

func (s *approximatePredictiveShadow) Decide(ctx context.Context, requestID string, input predictiveShadowInput) predictiveAdmissionDecision {
	if s == nil || requestID == "" {
		return predictiveAdmissionDecision{
			Outcome: predictiveAdmissionOutcomeRequestReject,
			Reason:  domainpredictive.ReasonPredictorProfileUnknown,
			Source:  runtimepredictive.PredictionSourceUnavailable,
		}
	}
	started := time.Now()
	defer func() { s.predictionDuration.Observe(time.Since(started)) }()
	if err := ctx.Err(); err != nil {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return s.rejectionDecision(predictiveProtectionScopeRequest, domainpredictive.ReasonPredictorProfileUnknown, "", 0)
	}
	if !input.Cost.Supported {
		s.recordUnknown(domainpredictive.ReasonRequestSizeUnknown)
		return s.rejectionDecision(predictiveProtectionScopeRequest, domainpredictive.ReasonRequestSizeUnknown, "", 0)
	}
	class, ok := approximateRequestClass(input.Path)
	if !ok {
		s.recordUnknown(domainpredictive.ReasonRequestSizeUnknown)
		return s.rejectionDecision(predictiveProtectionScopeRequest, domainpredictive.ReasonRequestSizeUnknown, "", 0)
	}

	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		closedAt := s.now()
		s.recordAvailabilityUnknown(closedAt)
		return s.rejectionDecision(predictiveProtectionScopeAvailability, domainpredictive.ReasonPredictorProfileUnknown, runtimepredictive.PredictionSourceUnavailable, 0)
	}
	decisionTime := s.now()
	if s.availabilityUnavailable(decisionTime) {
		s.recordAvailabilityUnknown(decisionTime)
		return s.rejectionDecision(predictiveProtectionScopeAvailability, domainpredictive.ReasonPredictorProfileUnknown, runtimepredictive.PredictionSourceUnavailable, 0)
	}
	s.setAvailabilityProtection(decisionTime, false)
	size := s.calibrator.Estimate(decisionTime, class, input.Cost.EstimatedInputLow, input.Cost.EstimatedInputHigh)
	if !size.Known {
		s.recordUnknown(domainpredictive.ReasonRequestSizeUnknown)
		return s.rejectionDecision(predictiveProtectionScopeRequest, domainpredictive.ReasonRequestSizeUnknown, "", 0)
	}
	decodeUpper, ok := approximateDecodeUpper(input)
	if !ok {
		s.recordUnknown(domainpredictive.ReasonRequestSizeUnknown)
		return s.rejectionDecision(predictiveProtectionScopeRequest, domainpredictive.ReasonRequestSizeUnknown, "", 0)
	}
	if err := ctx.Err(); err != nil {
		s.recordUnknown(domainpredictive.ReasonPredictorProfileUnknown)
		return s.rejectionDecision(predictiveProtectionScopeRequest, domainpredictive.ReasonPredictorProfileUnknown, "", 0)
	}
	secondHealthCheck := s.now()
	if s.availabilityUnavailable(secondHealthCheck) {
		s.recordAvailabilityUnknown(secondHealthCheck)
		return s.rejectionDecision(predictiveProtectionScopeAvailability, domainpredictive.ReasonPredictorProfileUnknown, runtimepredictive.PredictionSourceUnavailable, 0)
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
		closedAt := s.now()
		s.recordAvailabilityUnknown(closedAt)
		return s.rejectionDecision(predictiveProtectionScopeAvailability, domainpredictive.ReasonPredictorProfileUnknown, runtimepredictive.PredictionSourceUnavailable, 0)
	}
	backpressureEvent, requestRejectEvent, scope := s.recordResultLocked(result, decisionTime)
	if !result.Reserved {
		if s.mode != "shadow" || !validShadowObservationResult(result) {
			s.mu.Unlock()
			s.emitRouterBackpressure(backpressureEvent)
			s.emitRequestReject(requestRejectEvent)
			return s.rejectionDecision(scope, result.Decision.Reason, result.Prediction.Source, result.Prediction.Samples)
		}
		if len(s.observations) >= s.maximumObservations {
			s.observationStats.Dropped++
			s.mu.Unlock()
			s.emitRouterBackpressure(backpressureEvent)
			s.emitRequestReject(requestRejectEvent)
			return predictiveAdmissionDecision{Outcome: predictiveAdmissionOutcomeForward}
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
		if scope == predictiveProtectionScopeLoad {
			if pending, valid := runtimepredictive.PendingPrefillObservationForResult(result); valid {
				observation.shadowPrefill = &predictiveShadowPrefillReservation{observation: pending}
			}
		}
		s.observations[requestID] = observation
		s.observationStats.Created++
		s.mu.Unlock()
		s.emitRouterBackpressure(backpressureEvent)
		s.emitRequestReject(requestRejectEvent)
		return predictiveAdmissionDecision{Outcome: predictiveAdmissionOutcomeForward, Reservation: observation}
	}
	s.markShadowObservationsInterferedLocked("")
	reservation := &approximatePredictiveReservation{
		owner:              s,
		requestID:          requestID,
		identity:           result.Prediction.Identity,
		prediction:         result.Prediction,
		sizeEstimate:       size,
		decodeHorizonUpper: decodeUpper,
	}
	s.reservations[requestID] = reservation
	s.mu.Unlock()
	s.emitRouterBackpressure(backpressureEvent)
	s.emitRequestReject(requestRejectEvent)
	return predictiveAdmissionDecision{Outcome: predictiveAdmissionOutcomeForward, Reservation: reservation}
}

func (s *approximatePredictiveShadow) DecideAndReserve(ctx context.Context, requestID string, input predictiveShadowInput) predictiveShadowReservation {
	return s.Decide(ctx, requestID, input).Reservation
}

func (s *approximatePredictiveShadow) PredictiveAdmissionTelemetry() predictiveAdmissionTelemetrySnapshot {
	if s == nil {
		return predictiveAdmissionTelemetrySnapshot{}
	}
	now := s.now()
	availabilityUnavailable := s.availabilityUnavailable(now)
	s.mu.Lock()
	availabilityEvent := s.routerBackpressure.SetAvailability(now, s.closed || availabilityUnavailable)
	attempts := s.attempts
	tpsOutcomes := s.tpsOutcomes
	observationStats := s.observationStats
	observationStats.Active = len(s.observations)
	deferredStats := s.deferredStats
	deferredStats.Active = len(s.deferredOutcomes)
	routerBackpressure := s.routerBackpressure.Snapshot(now)
	s.mu.Unlock()
	s.emitRouterBackpressure(availabilityEvent)
	telemetry := predictiveAdmissionTelemetrySnapshot{
		Attempts:           attempts,
		PredictionDuration: &s.predictionDuration,
		TPSOutcomes:        tpsOutcomes,
		ShadowObservations: observationStats,
		DeferredOutcomes:   deferredStats,
		RouterBackpressure: routerBackpressure,
	}
	if s.shadowPrefills != nil {
		telemetry.ShadowPendingPrefills = s.shadowPrefills.Snapshot()
	}
	if provider, ok := s.upstream.(predictiveExistingPrefillTelemetryProvider); ok {
		telemetry.ExistingPrefill = provider.ExistingPrefillTelemetry()
	}
	telemetry.InputSize = s.calibrator.Snapshot(now)
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
		closeDone := s.closeDone
		s.mu.Unlock()
		<-closeDone
		s.mu.Lock()
		closeErr := s.closeErr
		s.mu.Unlock()
		return closeErr
	}
	s.closed = true
	var availabilityEvent *predictiveRouterBackpressureEvent
	if s.mode == "enforce" {
		availabilityEvent = s.routerBackpressure.SetAvailability(s.now(), true)
	}
	failed := 0
	for requestID := range s.reservations {
		if !s.coordinator.Terminate(requestID, runtimepredictive.TerminalExpired) {
			failed++
		}
		delete(s.reservations, requestID)
	}
	s.observationStats.Terminated += uint64(len(s.observations))
	clear(s.observations)
	if s.shadowPrefills != nil {
		s.shadowPrefills.Clear()
	}
	s.deferredStats.Terminated += uint64(len(s.deferredOutcomes))
	s.deferredStats.Censored += uint64(len(s.deferredOutcomes))
	clear(s.deferredOutcomes)
	upstream := s.upstream
	s.mu.Unlock()
	s.emitRouterBackpressure(availabilityEvent)
	s.learningOutcomes.Wait()
	var result error
	if upstream != nil {
		result = upstream.Close()
	}
	if failed > 0 {
		if result != nil {
			result = fmt.Errorf("expire %d predictive reservations during close; close upstream: %v", failed, result)
		} else {
			result = fmt.Errorf("expire %d predictive reservations during close", failed)
		}
	}
	s.mu.Lock()
	s.closeErr = result
	close(s.closeDone)
	s.mu.Unlock()
	return result
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
		if r.shadowPrefill != nil && r.owner.shadowPrefills != nil {
			r.shadowPrefill.handle = r.owner.shadowPrefills.Begin(r.shadowPrefill.observation)
		}
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
	r.endShadowPrefillLocked()
	r.prefillComplete = true
	return true
}

func (r *approximatePredictiveReservation) endShadowPrefillLocked() {
	if r == nil || r.shadowPrefill == nil || r.shadowPrefill.handle == nil {
		return
	}
	r.shadowPrefill.handle.End()
	r.shadowPrefill.handle = nil
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

func (r *approximatePredictiveReservation) ReleaseResources() bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	if r.owner.closed || r.observationOnly || !r.forwarded || r.resourcesReleased || !r.activeLocked() {
		return false
	}
	release := r.owner.coordinator.ReleaseResources(r.requestID)
	if !release.Released {
		return false
	}
	delete(r.owner.reservations, r.requestID)
	r.resourcesReleased = true
	r.outcomeInterfered = r.outcomeInterfered || release.OutcomeInterfered
	r.owner.deferredStats.Released++
	if len(r.owner.deferredOutcomes) >= r.owner.maximumDeferred {
		r.deferredOutcomeDropped = true
		r.owner.deferredStats.Dropped++
		return true
	}
	r.owner.deferredOutcomes[r.requestID] = r
	return true
}

func (r *approximatePredictiveReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	if r.owner.closed || !r.activeLocked() {
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
		r.endShadowPrefillLocked()
		delete(r.owner.observations, r.requestID)
		r.owner.observationStats.Terminated++
		if schedulerOutcome != nil && r.outcomeInterfered {
			schedulerOutcome.Censored = true
			r.owner.observationStats.Censored++
		}
		learn := schedulerOutcome != nil || sizeOutcome != nil
		if learn {
			r.owner.learningOutcomes.Add(1)
		}
		r.owner.mu.Unlock()
		if learn {
			defer r.owner.learningOutcomes.Done()
		}

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
	if r.resourcesReleased {
		_, retained := r.owner.deferredOutcomes[r.requestID]
		dropped := r.deferredOutcomeDropped
		if retained {
			delete(r.owner.deferredOutcomes, r.requestID)
			r.owner.deferredStats.Terminated++
			if cause != runtimepredictive.TerminalCompleted || r.outcomeInterfered {
				r.owner.deferredStats.Censored++
			}
		}
		r.deferredOutcomeDropped = false
		if schedulerOutcome != nil && r.outcomeInterfered {
			schedulerOutcome.Censored = true
		}
		learn := !dropped && (schedulerOutcome != nil || sizeOutcome != nil)
		if learn {
			r.owner.learningOutcomes.Add(1)
		}
		r.owner.mu.Unlock()

		if dropped {
			return true
		}
		if learn {
			defer r.owner.learningOutcomes.Done()
		}
		if schedulerOutcome != nil {
			observer := r.owner.coordinator.(predictiveUnreservedOutcomeCoordinator)
			if observer.ObserveUnreservedOutcome(r.prediction, cause, r.forwarded, *schedulerOutcome) {
				r.owner.mu.Lock()
				r.owner.deferredStats.Qualified++
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
	if r.resourcesReleased {
		if r.deferredOutcomeDropped {
			return true
		}
		return r.owner.deferredOutcomes[r.requestID] == r
	}
	return r.owner.reservations[r.requestID] == r
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
	var rejectEvent *predictiveRequestRejectEvent
	if s.mode == "enforce" {
		now := s.now()
		s.recordLastRejectLocked(now, reason, "", 0, predictiveProtectionScopeRequest)
		rejectEvent = s.requestRejectLogs.Observe(now, s.backpressureHold, runtimepredictive.CountAdmissionResult{
			Decision: domainpredictive.Decision{Reason: reason},
		})
	}
	s.mu.Unlock()
	s.emitRequestReject(rejectEvent)
}

func (s *approximatePredictiveShadow) recordAvailabilityUnknown(now time.Time) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.attempts.Attempts++
	s.attempts.Unknown++
	s.attempts.LastReason = domainpredictive.ReasonPredictorProfileUnknown
	s.attempts.LastSource = runtimepredictive.PredictionSourceUnavailable
	s.attempts.LastSamples = 0
	var event *predictiveRouterBackpressureEvent
	if s.mode == "enforce" {
		s.recordLastRejectLocked(now, domainpredictive.ReasonPredictorProfileUnknown, runtimepredictive.PredictionSourceUnavailable, 0, predictiveProtectionScopeAvailability)
		event = s.routerBackpressure.SetAvailability(now, true)
	}
	s.mu.Unlock()
	s.emitRouterBackpressure(event)
}

func (s *approximatePredictiveShadow) setAvailabilityProtection(now time.Time, unavailable bool) {
	if s == nil || s.mode != "enforce" {
		return
	}
	s.mu.Lock()
	event := s.routerBackpressure.SetAvailability(now, unavailable)
	s.mu.Unlock()
	s.emitRouterBackpressure(event)
}

func (s *approximatePredictiveShadow) availabilityUnavailable(now time.Time) bool {
	if s == nil {
		return true
	}
	if !predictiveUpstreamHealthy(s.upstream, now) {
		return true
	}
	provider, ok := s.coordinator.(predictiveAvailabilityProvider)
	return ok && !predictiveCoordinatorAvailable(provider)
}

func (s *approximatePredictiveShadow) recordResultLocked(result runtimepredictive.CountAdmissionResult, now time.Time) (*predictiveRouterBackpressureEvent, *predictiveRequestRejectEvent, predictiveProtectionScope) {
	s.attempts.Attempts++
	s.attempts.LastReason = result.Decision.Reason
	s.attempts.LastSource = result.Prediction.Source
	s.attempts.LastSamples = result.Prediction.Samples
	if result.Reserved {
		s.attempts.Fits++
		return nil, nil, ""
	}
	switch result.Decision.Reason {
	case domainpredictive.ReasonTokenizerProfileUnknown,
		domainpredictive.ReasonRequestSizeUnknown,
		domainpredictive.ReasonPredictorProfileUnknown:
		s.attempts.Unknown++
	default:
		s.attempts.Risks++
	}
	if s.mode != "enforce" {
		return nil, nil, predictiveProtectionScopeForResult(result, s.backpressurePolicy)
	}
	scope := predictiveProtectionScopeForResult(result, s.backpressurePolicy)
	s.recordLastRejectLocked(now, result.Decision.Reason, result.Prediction.Source, result.Prediction.Samples, scope)
	switch scope {
	case predictiveProtectionScopeLoad:
		return s.routerBackpressure.Observe(now, s.backpressureHold, result, s.backpressurePolicy), nil, scope
	case predictiveProtectionScopeAvailability:
		return s.routerBackpressure.SetAvailability(now, true), nil, scope
	default:
		return nil, s.requestRejectLogs.Observe(now, s.backpressureHold, result), scope
	}
}

func (s *approximatePredictiveShadow) rejectionDecision(scope predictiveProtectionScope, reason domainpredictive.Reason, source runtimepredictive.PredictionSource, samples int) predictiveAdmissionDecision {
	decision := predictiveAdmissionDecision{
		Outcome: predictiveAdmissionOutcomeForward,
		Reason:  reason,
		Source:  source,
		Samples: samples,
	}
	if s == nil || s.mode != "enforce" {
		return decision
	}
	switch scope {
	case predictiveProtectionScopeLoad:
		decision.Outcome = predictiveAdmissionOutcomeLoadProtection
	case predictiveProtectionScopeAvailability:
		decision.Outcome = predictiveAdmissionOutcomeAvailabilityProtection
	default:
		decision.Outcome = predictiveAdmissionOutcomeRequestReject
	}
	return decision
}

func (s *approximatePredictiveShadow) recordLastRejectLocked(now time.Time, reason domainpredictive.Reason, source runtimepredictive.PredictionSource, samples int, scope predictiveProtectionScope) {
	s.attempts.LastRejectReason = reason
	s.attempts.LastRejectSource = source
	s.attempts.LastRejectScope = scope
	s.attempts.LastRejectSamples = samples
	s.attempts.LastRejectAt = now
}

func (s *approximatePredictiveShadow) emitRouterBackpressure(event *predictiveRouterBackpressureEvent) {
	if event == nil || s == nil || s.onBackpressure == nil {
		return
	}
	s.onBackpressure(*event)
}

func (s *approximatePredictiveShadow) emitRequestReject(event *predictiveRequestRejectEvent) {
	if event == nil || s == nil || s.onRequestReject == nil {
		return
	}
	s.onRequestReject(*event)
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
