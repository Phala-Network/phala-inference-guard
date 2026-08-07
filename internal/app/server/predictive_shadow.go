package server

import (
	"context"
	"fmt"
	"strconv"
	"sync"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveShadowInput struct {
	Cost kvadmission.Cost
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
}

func (d predictiveAdmissionDecision) rejectsForward() bool {
	return d.Outcome == predictiveAdmissionOutcomeRequestReject ||
		d.Outcome == predictiveAdmissionOutcomeLoadProtection ||
		d.Outcome == predictiveAdmissionOutcomeAvailabilityProtection
}

func (d predictiveAdmissionDecision) validEnforceResult() bool {
	if d.Outcome == predictiveAdmissionOutcomeForward {
		return d.Reservation != nil
	}
	return d.rejectsForward() && d.Reservation == nil
}

type predictiveAdmissionShadow interface {
	Decide(context.Context, string, predictiveShadowInput) predictiveAdmissionDecision
	Close() error
}

type predictiveAdmissionTelemetryProvider interface {
	PredictiveAdmissionTelemetry() predictiveAdmissionTelemetrySnapshot
}

type predictiveAdmissionTelemetrySnapshot struct {
	CapabilityProfile  runtimepredictive.BackendCapabilityProfile
	CapabilityReason   string
	Attempts           predictiveAttemptSnapshot
	Manager            runtimepredictive.Snapshot
	PredictionDuration *durationHistogram
	RouterBackpressure predictiveRouterBackpressureSnapshot
	Observer           predictiveObserverSnapshot
	RequestAware       requestAwareTelemetrySnapshot
}

type requestAwareTelemetrySnapshot struct {
	Action                                       runtimepredictive.RequestAwareAction
	Reason                                       runtimepredictive.RequestAwareReason
	PressureSource                               runtimepredictive.RequestAwarePressureSource
	Pressure                                     float64
	SelectionInputTokens                         int64
	ReservedTokens                               int64
	AllowanceTokens                              int64
	EffectiveKV                                  int64
	PostAdmitKV                                  int64
	RemainingKV                                  int64
	Running                                      int
	Waiting                                      int
	EffectiveSequences                           int
	AggregateTPSProxy                            float64
	MeanActiveTPSProxy                           float64
	ProjectedTPSProxy                            float64
	TPSForecastValid                             bool
	PrefillClass                                 runtimepredictive.RequestAwarePrefillClass
	EstimatedPrefillTokens                       int64
	PendingPrefillSequences                      int
	PendingPrefillTokens                         int64
	PostAdmitPendingPrefillTokens                int64
	PendingLongPrefillSequences                  int
	PendingQuiescentPrefillSequences             int
	LastDecisionPendingPrefillSequences          int
	LastDecisionPendingPrefillTokens             int64
	LastDecisionPostAdmitPendingPrefillTokens    int64
	LastDecisionPendingLongPrefillSequences      int
	LastDecisionPendingQuiescentPrefillSequences int
}

type serverDependencies struct {
	NewPredictiveShadow func(config) (predictiveAdmissionShadow, error)
}

type guardedPredictiveReservation struct {
	mu               sync.Mutex
	reservation      predictiveShadowReservation
	forwardAttempted bool
	forwarded        bool
	prefillComplete  bool
	terminated       bool
	onFailure        func(string)
}

func (s *proxyServer) decidePredictiveShadow(ctx context.Context, input predictiveShadowInput) (result predictiveAdmissionDecision) {
	if s == nil || s.predictiveShadow == nil {
		return predictiveAdmissionDecision{}
	}
	defer func() {
		if recover() != nil {
			s.predictiveShadowFailures.decide.Add(1)
			result = predictiveAdmissionDecision{Outcome: predictiveAdmissionOutcomeRequestReject, Reason: domainpredictive.ReasonPredictorProfileUnknown, Source: runtimepredictive.PredictionSourceUnavailable}
		}
	}()
	requestID := "http-" + strconv.FormatUint(s.nextPredictiveID.Add(1), 10)
	decision := s.predictiveShadow.Decide(ctx, requestID, input)
	if decision.Reservation != nil {
		decision.Reservation = &guardedPredictiveReservation{reservation: decision.Reservation, onFailure: func(phase string) {
			s.predictiveShadowFailures.add(phase)
		}}
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
	r.forwarded = guardedReservationCall("forward", r.onFailure, r.reservation.MarkForwarded)
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
	return guardedReservationCall("prefill", r.onFailure, r.reservation.MarkPrefillComplete)
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
	return guardedReservationCall("terminal", r.onFailure, func() bool { return r.reservation.Terminate(cause) })
}

func guardedReservationCall(phase string, onFailure func(string), call func() bool) (result bool) {
	defer func() {
		if recover() != nil {
			if onFailure != nil {
				onFailure(phase)
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
		defer func() {
			if recover() != nil {
				s.predictiveShadowFailures.close.Add(1)
				s.closeErr = fmt.Errorf("predictive admission close panicked")
			}
		}()
		if s.predictiveShadow != nil {
			s.closeErr = s.predictiveShadow.Close()
		}
	})
	return s.closeErr
}
