package server

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type predictiveShadowInput struct {
	Path             string
	Body             []byte
	Cost             kvadmission.Cost
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

type predictiveAdmissionOutcome string

const (
	predictiveAdmissionOutcomeForward                predictiveAdmissionOutcome = "forward"
	predictiveAdmissionOutcomeRequestReject          predictiveAdmissionOutcome = "request_reject"
	predictiveAdmissionOutcomeLoadProtection         predictiveAdmissionOutcome = "load_protection"
	predictiveAdmissionOutcomeAvailabilityProtection predictiveAdmissionOutcome = "availability_protection"
)

type predictiveAdmissionDecision struct {
	Outcome     predictiveAdmissionOutcome
	Reservation predictiveShadowReservation
	Reason      domainpredictive.Reason
	Source      runtimepredictive.PredictionSource
	Samples     int
}

func (d predictiveAdmissionDecision) rejectsForward() bool {
	switch d.Outcome {
	case predictiveAdmissionOutcomeRequestReject,
		predictiveAdmissionOutcomeLoadProtection,
		predictiveAdmissionOutcomeAvailabilityProtection:
		return true
	default:
		return false
	}
}

func (d predictiveAdmissionDecision) validEnforceResult() bool {
	switch d.Outcome {
	case predictiveAdmissionOutcomeForward:
		return d.Reservation != nil
	case predictiveAdmissionOutcomeRequestReject,
		predictiveAdmissionOutcomeLoadProtection,
		predictiveAdmissionOutcomeAvailabilityProtection:
		return d.Reservation == nil
	default:
		return false
	}
}

type predictiveAdmissionShadow interface {
	Decide(context.Context, string, predictiveShadowInput) predictiveAdmissionDecision
	Close() error
}

type predictiveAdmissionTelemetryProvider interface {
	PredictiveAdmissionTelemetry() predictiveAdmissionTelemetrySnapshot
}

type predictiveAdmissionTelemetrySnapshot struct {
	Attempts              predictiveAttemptSnapshot
	Manager               runtimepredictive.Snapshot
	Learning              runtimepredictive.LearnedSchedulerSnapshot
	InputSize             runtimepredictive.InputSizeCalibratorSnapshot
	PredictionDuration    *durationHistogram
	TPSOutcomes           predictiveTPSOutcomeSnapshot
	QualifiedUserTPS      telemetry.HistogramSample
	QualifiedTPOT         telemetry.HistogramSample
	ShadowObservations    predictiveShadowObservationSnapshot
	ShadowPendingPrefills predictiveShadowPendingPrefillSnapshot
	DeferredOutcomes      predictiveDeferredOutcomeSnapshot
	RouterBackpressure    predictiveRouterBackpressureSnapshot
	ExistingPrefill       predictiveExistingPrefillObservationSnapshot
	RequestAware          requestAwareTelemetrySnapshot
}

type requestAwareTelemetrySnapshot struct {
	Action                           runtimepredictive.RequestAwareAction
	Reason                           runtimepredictive.RequestAwareReason
	PressureSource                   runtimepredictive.RequestAwarePressureSource
	Pressure                         float64
	SelectionInputTokens             int64
	ReservedTokens                   int64
	AllowanceTokens                  int64
	EffectiveKV                      int64
	PostAdmitKV                      int64
	RemainingKV                      int64
	Running                          int
	Waiting                          int
	EffectiveSequences               int
	AggregateTPSProxy                float64
	MeanActiveTPSProxy               float64
	ProjectedTPSProxy                float64
	TPSForecastValid                 bool
	PrefillClass                     runtimepredictive.RequestAwarePrefillClass
	EstimatedPrefillTokens           int64
	PendingPrefillSequences          int
	PendingPrefillTokens             int64
	PostAdmitPendingPrefillTokens    int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
}

type predictiveExistingPrefillObservationSnapshot struct {
	Accepted                 uint64
	Rejected                 uint64
	Censored                 uint64
	Deduplicated             uint64
	LastExistingUserTPS      float64
	LastExistingUserTPSValid bool
	LastExploratory          bool
}

type predictiveTPSOutcomeSnapshot struct {
	Backend       uint64
	Local         uint64
	LocalCensored uint64
	Missing       uint64
	Rejected      uint64
}

type predictiveShadowObservationSnapshot struct {
	Active     int
	Created    uint64
	Terminated uint64
	Qualified  uint64
	Censored   uint64
	Dropped    uint64
}

type predictiveDeferredOutcomeSnapshot struct {
	Active     int
	Released   uint64
	Terminated uint64
	Qualified  uint64
	Censored   uint64
	Dropped    uint64
}

type predictiveSemanticTTFTObserver interface {
	ObserveSemanticTTFT(time.Duration) bool
}

type predictiveCompletionObservation struct {
	PromptTokens          int64
	CompletionTokens      int64
	ObservedAt            time.Time
	ElapsedSinceRequest   time.Duration
	BackendMeanITL        time.Duration
	BackendGenerationTime time.Duration
}

type predictiveCompletionObserver interface {
	ObserveCompletion(predictiveCompletionObservation) bool
}

type predictiveResourceReleaser interface {
	ReleaseResources() bool
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
	forwardAttempted      bool
	forwarded             bool
	prefillComplete       bool
	semanticTTFTObserved  bool
	completionObserved    bool
	resourceReleaseTried  bool
	terminated            bool
	onForwardCallFailure  func()
	onSemanticCallFailure func()
	onCompletionFailure   func()
	onResourceCallFailure func()
	onTerminalCallFailure func()
}

func observePredictiveSemanticTTFT(reservation predictiveShadowReservation, ttft time.Duration) bool {
	observer, ok := reservation.(predictiveSemanticTTFTObserver)
	return ok && observer.ObserveSemanticTTFT(ttft)
}

func observePredictiveCompletion(reservation predictiveShadowReservation, observation predictiveCompletionObservation) bool {
	observer, ok := reservation.(predictiveCompletionObserver)
	return ok && observer.ObserveCompletion(observation)
}

func (s *proxyServer) decidePredictiveShadow(ctx context.Context, input predictiveShadowInput) (result predictiveAdmissionDecision) {
	if s == nil || s.predictiveShadow == nil {
		return predictiveAdmissionDecision{}
	}
	if input.Body != nil {
		defer clear(input.Body)
	}
	defer func() {
		if recover() != nil {
			s.predictiveShadowFailures.decide.Add(1)
			s.logPredictiveFailureReject("decision_panic")
			result = predictiveAdmissionDecision{
				Outcome: predictiveAdmissionOutcomeRequestReject,
				Reason:  domainpredictive.ReasonPredictorProfileUnknown,
				Source:  runtimepredictive.PredictionSourceUnavailable,
			}
		}
	}()
	requestID := "http-" + strconv.FormatUint(s.nextPredictiveID.Add(1), 10)
	decision := s.predictiveShadow.Decide(ctx, requestID, input)
	if decision.Reservation == nil {
		return decision
	}
	decision.Reservation = &guardedPredictiveReservation{
		reservation: decision.Reservation,
		onForwardCallFailure: func() {
			s.predictiveShadowFailures.forward.Add(1)
		},
		onSemanticCallFailure: func() {
			s.predictiveShadowFailures.semantic.Add(1)
		},
		onCompletionFailure: func() {
			s.predictiveShadowFailures.completion.Add(1)
		},
		onResourceCallFailure: func() {
			s.predictiveShadowFailures.resourceRelease.Add(1)
		},
		onTerminalCallFailure: func() {
			s.predictiveShadowFailures.terminal.Add(1)
		},
	}
	return decision
}

func (r *guardedPredictiveReservation) MarkForwarded() bool {
	if r == nil || r.reservation == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.forwardAttempted || r.terminated {
		return false
	}
	r.forwardAttempted = true
	r.forwarded = callPredictiveShadow(r.onForwardCallFailure, r.reservation.MarkForwarded)
	return r.forwarded
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
	if !r.forwarded || r.semanticTTFTObserved || r.terminated {
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

func (r *guardedPredictiveReservation) ObserveCompletion(observation predictiveCompletionObservation) bool {
	if r == nil || r.reservation == nil || observation.CompletionTokens <= 0 || observation.ElapsedSinceRequest <= 0 {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.forwarded || r.completionObserved || r.terminated {
		return false
	}
	observer, ok := r.reservation.(predictiveCompletionObserver)
	if !ok {
		return false
	}
	r.completionObserved = true
	return callPredictiveShadow(r.onCompletionFailure, func() bool {
		return observer.ObserveCompletion(observation)
	})
}

func (r *guardedPredictiveReservation) ReleaseResources() bool {
	if r == nil || r.reservation == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.forwarded || r.resourceReleaseTried || r.terminated {
		return false
	}
	releaser, ok := r.reservation.(predictiveResourceReleaser)
	if !ok {
		return false
	}
	r.resourceReleaseTried = true
	return callPredictiveShadow(r.onResourceCallFailure, releaser.ReleaseResources)
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
