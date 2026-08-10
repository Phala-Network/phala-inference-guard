package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type requestAwareSnapshotProvider interface {
	RequestAwareInput(time.Time) runtimepredictive.RequestAwareInput
}

type requestAwarePredictiveAdapterConfig struct {
	Manager             *runtimepredictive.Manager
	Policy              *runtimepredictive.RequestAwarePolicy
	CapabilityProfile   runtimepredictive.BackendCapabilityProfile
	CapabilityReason    string
	Snapshot            requestAwareSnapshotProvider
	ManifestID          string
	BlockSize           int64
	Mode                string
	Now                 func() time.Time
	DecisionLogInterval time.Duration
	OnDecision          func(requestAwareDecisionLogEvent)
}

type requestAwarePredictiveAdapter struct {
	mu                      sync.Mutex
	manager                 *runtimepredictive.Manager
	policy                  *runtimepredictive.RequestAwarePolicy
	capabilityProfile       runtimepredictive.BackendCapabilityProfile
	capabilityReason        string
	snapshot                requestAwareSnapshotProvider
	manifestID              string
	blockSize               int64
	mode                    string
	now                     func() time.Time
	closed                  bool
	attempts                predictiveAttemptSnapshot
	lastRequestAware        requestAwareTelemetrySnapshot
	routerBackpressure      predictiveRouterBackpressureSnapshot
	routerActivationCounter uint64
	decisionLogInterval     time.Duration
	decisionLogs            requestAwareDecisionLogState
	onDecision              func(requestAwareDecisionLogEvent)
	predictionDuration      durationHistogram
}

type requestAwarePredictiveReservation struct {
	owner     *requestAwarePredictiveAdapter
	requestID string
}

func newRequestAwarePredictiveAdapter(config requestAwarePredictiveAdapterConfig) (*requestAwarePredictiveAdapter, error) {
	mode := strings.ToLower(strings.TrimSpace(config.Mode))
	if config.Manager == nil || config.Policy == nil || config.Snapshot == nil ||
		strings.TrimSpace(config.ManifestID) == "" || config.BlockSize <= 0 ||
		(mode != "shadow" && mode != "enforce") ||
		!config.Manager.HasRequestAwareObservation() ||
		!config.Policy.MatchesCapability(config.CapabilityProfile) ||
		config.BlockSize != config.CapabilityProfile.KVBlockSize {
		return nil, fmt.Errorf("request-aware predictive adapter configuration is invalid")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.DecisionLogInterval <= 0 {
		config.DecisionLogInterval = defaultRequestAwareDecisionLogInterval
	}
	return &requestAwarePredictiveAdapter{
		manager:             config.Manager,
		policy:              config.Policy,
		capabilityProfile:   config.CapabilityProfile,
		capabilityReason:    config.CapabilityReason,
		snapshot:            config.Snapshot,
		manifestID:          strings.TrimSpace(config.ManifestID),
		blockSize:           config.BlockSize,
		mode:                mode,
		now:                 config.Now,
		decisionLogInterval: config.DecisionLogInterval,
		onDecision:          config.OnDecision,
		predictionDuration:  newPredictiveDurationHistogram(),
	}, nil
}

func (a *requestAwarePredictiveAdapter) Decide(ctx context.Context, requestID string, input predictiveShadowInput) predictiveAdmissionDecision {
	started := time.Now()
	defer func() {
		if a != nil {
			a.predictionDuration.Observe(time.Since(started))
		}
	}()
	if a == nil {
		return requestAwareAdapterFailure(
			predictiveAdmissionOutcomeRequestReject,
			domainpredictive.ReasonPredictorProfileUnknown,
			runtimepredictive.PredictionSourceUnavailable,
		)
	}
	now := a.now()
	if requestID == "" || ctx.Err() != nil {
		decision := requestAwareAdapterFailure(
			predictiveAdmissionOutcomeRequestReject,
			domainpredictive.ReasonPredictorProfileUnknown,
			runtimepredictive.PredictionSourceUnavailable,
		)
		a.recordDecision(now, runtimepredictive.RequestAwareManagerResult{
			Decision: runtimepredictive.RequestAwareDecision{
				Action: runtimepredictive.RequestAwareHardProtect,
				Reason: runtimepredictive.RequestAwareReasonInvalid,
			},
		}, 0, 0, runtimepredictive.RequestAwareInput{}, decision)
		return decision
	}
	a.mu.Lock()
	closed := a.closed
	a.mu.Unlock()
	if closed {
		decision := requestAwareAdapterFailure(
			predictiveAdmissionOutcomeAvailabilityProtection,
			domainpredictive.ReasonPredictorProfileUnknown,
			runtimepredictive.PredictionSourceUnavailable,
		)
		a.recordDecision(now, runtimepredictive.RequestAwareManagerResult{
			Decision: runtimepredictive.RequestAwareDecision{
				Action: runtimepredictive.RequestAwareHardProtect,
				Reason: runtimepredictive.RequestAwareReasonUnavailable,
			},
		}, 0, 0, runtimepredictive.RequestAwareInput{}, decision)
		return decision
	}
	selectionInputTokens, cost, valid := requestAwareAdapterCost(a.manifestID, a.blockSize, input)
	if !valid {
		decision := requestAwareAdapterFailure(
			predictiveAdmissionOutcomeRequestReject,
			domainpredictive.ReasonRequestSizeUnknown,
			runtimepredictive.PredictionSourceDeterministic,
		)
		a.recordDecision(now, runtimepredictive.RequestAwareManagerResult{
			Decision: runtimepredictive.RequestAwareDecision{
				Action: runtimepredictive.RequestAwareHardProtect,
				Reason: runtimepredictive.RequestAwareReasonInvalid,
			},
		}, 0, 0, runtimepredictive.RequestAwareInput{}, decision)
		return decision
	}
	var result runtimepredictive.RequestAwareManagerResult
	if a.mode == "shadow" {
		result = a.manager.DecideCurrentRequestAware(now, requestID, cost, selectionInputTokens, a.policy)
	} else {
		result = a.manager.DecideCurrentRequestAwareAndReserve(now, requestID, cost, selectionInputTokens, a.policy)
	}
	a.mu.Lock()
	closed = a.closed
	a.mu.Unlock()
	if closed && result.Reserved {
		a.manager.Terminate(requestID, runtimepredictive.TerminalExpired)
		decision := requestAwareAdapterFailure(
			predictiveAdmissionOutcomeAvailabilityProtection,
			domainpredictive.ReasonPredictorProfileUnknown,
			runtimepredictive.PredictionSourceUnavailable,
		)
		a.recordDecision(now, runtimepredictive.RequestAwareManagerResult{
			Decision: runtimepredictive.RequestAwareDecision{
				Action: runtimepredictive.RequestAwareHardProtect,
				Reason: runtimepredictive.RequestAwareReasonUnavailable,
			},
		}, selectionInputTokens, requestAwareReservedTokens(cost), result.Observation, decision)
		return decision
	}
	var decision predictiveAdmissionDecision
	if result.Decision.Action == runtimepredictive.RequestAwareAdmit {
		if a.mode == "shadow" {
			decision = predictiveAdmissionDecision{
				Outcome: predictiveAdmissionOutcomeForward,
				Reason:  domainpredictive.ReasonFit,
				Source:  runtimepredictive.PredictionSourceDeterministic,
			}
			a.recordDecision(now, result, selectionInputTokens, requestAwareReservedTokens(cost), result.Observation, decision)
			return decision
		}
		if !result.Reserved {
			decision = requestAwareAdapterFailure(
				predictiveAdmissionOutcomeAvailabilityProtection,
				domainpredictive.ReasonPredictorProfileUnknown,
				runtimepredictive.PredictionSourceUnavailable,
			)
			a.recordDecision(now, runtimepredictive.RequestAwareManagerResult{
				Decision: runtimepredictive.RequestAwareDecision{
					Action: runtimepredictive.RequestAwareHardProtect,
					Reason: runtimepredictive.RequestAwareReasonUnavailable,
				},
			}, selectionInputTokens, requestAwareReservedTokens(cost), result.Observation, decision)
			return decision
		}
		decision = predictiveAdmissionDecision{
			Outcome: predictiveAdmissionOutcomeForward,
			Reservation: &requestAwarePredictiveReservation{
				owner:     a,
				requestID: requestID,
			},
			Reason: domainpredictive.ReasonFit,
			Source: runtimepredictive.PredictionSourceDeterministic,
		}
		a.recordDecision(now, result, selectionInputTokens, requestAwareReservedTokens(cost), result.Observation, decision)
		return decision
	}
	decision = requestAwareAdapterProtectedDecision(result.Decision)
	a.recordDecision(now, result, selectionInputTokens, requestAwareReservedTokens(cost), result.Observation, decision)
	return decision
}

func (a *requestAwarePredictiveAdapter) Close() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return nil
	}
	a.closed = true
	a.mu.Unlock()
	a.manager.InvalidateEpoch()
	var closeErr error
	if closer, ok := a.snapshot.(interface{ Close() error }); ok {
		closeErr = closer.Close()
	}
	return closeErr
}

func (a *requestAwarePredictiveAdapter) PredictiveAdmissionTelemetry() predictiveAdmissionTelemetrySnapshot {
	if a == nil {
		return predictiveAdmissionTelemetrySnapshot{}
	}
	now := a.now()
	_, observer, _ := requestAwareTelemetryObservation(a.snapshot, now)
	router := a.inspectRouterBackpressure(now)
	a.mu.Lock()
	attempts := a.attempts
	profile := a.capabilityProfile
	capabilityReason := a.capabilityReason
	lastRequestAware := a.lastRequestAware
	a.routerBackpressure = a.transitionRouterBackpressureLocked(now, router)
	router = a.routerBackpressure
	a.mu.Unlock()
	currentPending := a.manager.CurrentRequestAwarePending(a.policy)
	lastRequestAware.PendingPrefillSequences = currentPending.PrefillSequences
	lastRequestAware.PendingPrefillTokens = currentPending.PrefillTokens
	lastRequestAware.PostAdmitPendingPrefillTokens = currentPending.PrefillTokens
	lastRequestAware.PendingLongPrefillSequences = currentPending.LongPrefillSequences
	lastRequestAware.PendingQuiescentPrefillSequences = currentPending.QuiescentPrefillSequences
	return predictiveAdmissionTelemetrySnapshot{
		CapabilityProfile:  profile,
		CapabilityReason:   capabilityReason,
		Attempts:           attempts,
		Manager:            a.manager.Snapshot(),
		PredictionDuration: &a.predictionDuration,
		RouterBackpressure: router,
		Observer:           observer,
		RequestAware:       lastRequestAware,
	}
}

func (a *requestAwarePredictiveAdapter) recordDecision(
	now time.Time,
	result runtimepredictive.RequestAwareManagerResult,
	selectionInputTokens int64,
	reservedTokens int64,
	input runtimepredictive.RequestAwareInput,
	decision predictiveAdmissionDecision,
) {
	if a == nil {
		return
	}
	last := requestAwareTelemetrySnapshot{
		Action:                              result.Decision.Action,
		Reason:                              result.Decision.Reason,
		PressureSource:                      result.Decision.PressureSource,
		Pressure:                            result.Decision.Pressure,
		SelectionInputTokens:                selectionInputTokens,
		ReservedTokens:                      reservedTokens,
		AllowanceTokens:                     result.Decision.AllowanceTokens,
		EffectiveKV:                         result.Decision.EffectiveKV,
		PostAdmitKV:                         result.Decision.PostAdmitKV,
		RemainingKV:                         result.Decision.RemainingKV,
		Running:                             input.Running,
		Waiting:                             input.Waiting,
		EffectiveSequences:                  result.Decision.EffectiveSequences,
		AggregateTPSProxy:                   input.AggregateTPSProxy,
		MeanActiveTPSProxy:                  input.MeanActiveTPSProxy,
		PrefillClass:                        result.Decision.PrefillClass,
		EstimatedPrefillTokens:              result.Decision.EstimatedPrefillTokens,
		PendingPrefillSequences:             result.Decision.PendingPrefillSequences,
		PendingPrefillTokens:                result.Decision.PendingPrefillTokens,
		PostAdmitPendingPrefillTokens:       result.Decision.PostAdmitPendingPrefillTokens,
		PendingLongPrefillSequences:         result.Decision.PendingLongPrefillSequences,
		PendingQuiescentPrefillSequences:    result.Decision.PendingQuiescentPrefillSequences,
		LastDecisionPendingPrefillSequences: result.Decision.PendingPrefillSequences,
		LastDecisionPendingPrefillTokens:    result.Decision.PendingPrefillTokens,
		LastDecisionPostAdmitPendingPrefillTokens:    result.Decision.PostAdmitPendingPrefillTokens,
		LastDecisionPendingLongPrefillSequences:      result.Decision.PendingLongPrefillSequences,
		LastDecisionPendingQuiescentPrefillSequences: result.Decision.PendingQuiescentPrefillSequences,
	}
	a.mu.Lock()
	a.attempts.Attempts++
	a.attempts.LastReason = decision.Reason
	a.attempts.LastSource = decision.Source
	if result.Decision.Action == runtimepredictive.RequestAwareAdmit && decision.Outcome == predictiveAdmissionOutcomeForward {
		a.attempts.Fits++
	} else if requestAwareUnknownReason(decision.Reason) {
		a.attempts.Unknown++
	} else {
		a.attempts.Risks++
	}
	if a.mode == "enforce" && decision.rejectsForward() {
		a.attempts.LastRejectReason = decision.Reason
		a.attempts.LastRejectSource = decision.Source
		a.attempts.LastRejectScope = requestAwareProtectionScope(decision.Outcome)
		a.attempts.LastRejectAt = now
		a.attempts.LastRejectManagerSequence = result.DecisionManagerSequence
		a.attempts.LastRejectManagerSequenceValid = result.DecisionManagerSequenceValid
	}
	a.lastRequestAware = last
	var logEvent *requestAwareDecisionLogEvent
	if a.onDecision != nil {
		logEvent = a.decisionLogs.Claim(now, a.decisionLogInterval, requestAwareDecisionLogEvent{
			Mode:                             a.mode,
			Enforced:                         a.mode == "enforce" && decision.rejectsForward(),
			Action:                           result.Decision.Action,
			Reason:                           result.Decision.Reason,
			HTTPReason:                       decision.Reason,
			Scope:                            requestAwareProtectionScope(decision.Outcome),
			PressureSource:                   result.Decision.PressureSource,
			Pressure:                         result.Decision.Pressure,
			SelectionInputTokens:             selectionInputTokens,
			ReservedTokens:                   reservedTokens,
			AllowanceTokens:                  result.Decision.AllowanceTokens,
			EffectiveKV:                      result.Decision.EffectiveKV,
			PostAdmitKV:                      result.Decision.PostAdmitKV,
			RemainingKV:                      result.Decision.RemainingKV,
			Running:                          input.Running,
			Waiting:                          input.Waiting,
			EffectiveSequences:               result.Decision.EffectiveSequences,
			AggregateTPSProxy:                input.AggregateTPSProxy,
			MeanActiveTPSProxy:               input.MeanActiveTPSProxy,
			PrefillClass:                     result.Decision.PrefillClass,
			EstimatedPrefillTokens:           result.Decision.EstimatedPrefillTokens,
			PendingPrefillSequences:          result.Decision.PendingPrefillSequences,
			PendingPrefillTokens:             result.Decision.PendingPrefillTokens,
			PostAdmitPendingPrefillTokens:    result.Decision.PostAdmitPendingPrefillTokens,
			PendingLongPrefillSequences:      result.Decision.PendingLongPrefillSequences,
			PendingQuiescentPrefillSequences: result.Decision.PendingQuiescentPrefillSequences,
		})
	}
	reporter := a.onDecision
	a.mu.Unlock()
	emitRequestAwareDecision(reporter, logEvent)
}

func (a *requestAwarePredictiveAdapter) inspectRouterBackpressure(
	now time.Time,
) predictiveRouterBackpressureSnapshot {
	a.mu.Lock()
	mode := a.mode
	closed := a.closed
	a.mu.Unlock()
	if mode != "enforce" {
		return predictiveRouterBackpressureSnapshot{}
	}
	if closed || a.manager == nil || a.policy == nil || a.snapshot == nil || a.blockSize <= 0 {
		return requestAwareHardRouterBackpressure(
			domainpredictive.ReasonPredictorProfileUnknown,
			runtimepredictive.PredictionSourceUnavailable,
			predictiveProtectionScopeAvailability,
		)
	}
	cost := requestAwareInspectCost(a.manifestID, a.blockSize)
	result := a.manager.DecideCurrentRequestAware(
		now,
		"\x00pig-request-aware-inspect",
		cost,
		a.blockSize,
		a.policy,
	)
	if result.Decision.Action == runtimepredictive.RequestAwareAdmit {
		if result.Decision.Pressure <= 0 {
			return predictiveRouterBackpressureSnapshot{}
		}
		return predictiveRouterBackpressureSnapshot{
			Active:          true,
			Scope:           predictiveProtectionScopeLoad,
			Reason:          domainpredictive.ReasonRequestSizeAtPressure,
			Source:          runtimepredictive.PredictionSourceDeterministic,
			MinimumRunning:  1,
			InspectCapacity: 1,
		}
	}
	protected := requestAwareAdapterProtectedDecision(result.Decision)
	scope := requestAwareProtectionScope(protected.Outcome)
	if result.Decision.Reason == runtimepredictive.RequestAwareReasonInvalid ||
		result.Decision.Reason == runtimepredictive.RequestAwareReasonUnavailable ||
		result.Decision.Reason == runtimepredictive.RequestAwareReasonStale {
		scope = predictiveProtectionScopeAvailability
	}
	return requestAwareHardRouterBackpressure(protected.Reason, protected.Source, scope)
}

func (a *requestAwarePredictiveAdapter) transitionRouterBackpressureLocked(
	now time.Time,
	desired predictiveRouterBackpressureSnapshot,
) predictiveRouterBackpressureSnapshot {
	desired.LatestRejectAt = a.attempts.LastRejectAt
	managerSequenceCurrent := !a.attempts.LastRejectManagerSequenceValid ||
		a.manager.EventSequence() == a.attempts.LastRejectManagerSequence
	if managerSequenceCurrent {
		if recent, ok := recentRequestAwareRejectProjection(now, a.attempts); ok &&
			(!desired.Active || desired.InspectCapacity > recent.InspectCapacity) {
			desired = recent
		}
	}
	if !desired.Active {
		desired.Activations = a.routerActivationCounter
		return desired
	}
	previous := a.routerBackpressure
	transition := !previous.Active || previous.InspectCapacity != desired.InspectCapacity ||
		previous.Scope != desired.Scope || previous.Reason != desired.Reason || previous.Source != desired.Source
	if transition {
		if a.routerActivationCounter < ^uint64(0) {
			a.routerActivationCounter++
		}
		desired.Activation = a.routerActivationCounter
		desired.ActivatedAt = now
	} else {
		desired.Activation = previous.Activation
		desired.ActivatedAt = previous.ActivatedAt
	}
	desired.Activations = a.routerActivationCounter
	return desired
}

func requestAwareHardRouterBackpressure(
	reason domainpredictive.Reason,
	source runtimepredictive.PredictionSource,
	scope predictiveProtectionScope,
) predictiveRouterBackpressureSnapshot {
	return predictiveRouterBackpressureSnapshot{
		Active:          true,
		Scope:           scope,
		Reason:          reason,
		Source:          source,
		MinimumRunning:  1,
		InspectCapacity: 0,
	}
}

func requestAwareInspectCost(manifestID string, blockSize int64) domainpredictive.RequestCost {
	return domainpredictive.RequestCost{
		ManifestID:  manifestID,
		InputTokens: blockSize,
		KV: domainpredictive.KVIncrement{
			PhysicalKVUpper: blockSize,
			ActiveKVUpper:   blockSize,
		},
		UncachedPrefillUpper:     blockSize,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: blockSize,
		Confidence:               1,
	}
}

func requestAwareReservedTokens(cost domainpredictive.RequestCost) int64 {
	if cost.KV.PhysicalKVUpper > cost.KV.ActiveKVUpper {
		return cost.KV.PhysicalKVUpper
	}
	return cost.KV.ActiveKVUpper
}

func requestAwareUnknownReason(reason domainpredictive.Reason) bool {
	switch reason {
	case domainpredictive.ReasonRequestSizeUnknown,
		domainpredictive.ReasonPredictorProfileUnknown,
		domainpredictive.ReasonMetricsStale,
		domainpredictive.ReasonDuplicateRequest:
		return true
	default:
		return false
	}
}

func requestAwareProtectionScope(outcome predictiveAdmissionOutcome) predictiveProtectionScope {
	switch outcome {
	case predictiveAdmissionOutcomeLoadProtection:
		return predictiveProtectionScopeLoad
	case predictiveAdmissionOutcomeAvailabilityProtection:
		return predictiveProtectionScopeAvailability
	default:
		return predictiveProtectionScopeRequest
	}
}

func (r *requestAwarePredictiveReservation) MarkForwarded() bool {
	if r == nil || r.owner == nil {
		return false
	}
	r.owner.mu.Lock()
	defer r.owner.mu.Unlock()
	return !r.owner.closed && r.owner.manager != nil && r.owner.manager.MarkForwarded(r.requestID)
}

func (r *requestAwarePredictiveReservation) MarkPrefillComplete() bool {
	return r != nil && r.owner != nil && r.owner.manager != nil && r.owner.manager.MarkPrefillComplete(r.requestID)
}

func (r *requestAwarePredictiveReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	return r != nil && r.owner != nil && r.owner.manager != nil && r.owner.manager.Terminate(r.requestID, cause)
}

func requestAwareAdapterCost(manifestID string, blockSize int64, input predictiveShadowInput) (int64, domainpredictive.RequestCost, bool) {
	cost := input.Cost
	if !cost.Supported || manifestID == "" || blockSize <= 0 || cost.EstimatedInputHigh <= 0 || cost.BoundedDecodeTokens < 0 {
		return 0, domainpredictive.RequestCost{}, false
	}
	selectionInputTokens, known := cost.ApproximatePrefillTokenHint()
	if !known {
		selectionInputTokens = cost.EstimatedInputHigh
	}
	if selectionInputTokens <= 0 {
		return 0, domainpredictive.RequestCost{}, false
	}
	requestCost, err := domainpredictive.BuildRequestCost(domainpredictive.RequestCostInput{
		ManifestID:             manifestID,
		BlockSize:              blockSize,
		SelectionPrefillTokens: selectionInputTokens,
		SafetyInputTokens:      cost.EstimatedInputHigh,
		DecodeHorizonTokens:    cost.BoundedDecodeTokens,
		Confidence:             1,
	})
	if err != nil {
		return 0, domainpredictive.RequestCost{}, false
	}
	return selectionInputTokens, requestCost, true
}

func requestAwareAdapterSnapshot(provider requestAwareSnapshotProvider, now time.Time) (input runtimepredictive.RequestAwareInput, valid bool) {
	if provider == nil || now.IsZero() {
		return runtimepredictive.RequestAwareInput{}, false
	}
	defer func() {
		if recover() != nil {
			input = runtimepredictive.RequestAwareInput{}
			valid = false
		}
	}()
	return provider.RequestAwareInput(now), true
}

func requestAwareTelemetryObservation(
	provider requestAwareSnapshotProvider,
	now time.Time,
) (input runtimepredictive.RequestAwareInput, observer predictiveObserverSnapshot, valid bool) {
	if provider == nil || now.IsZero() {
		return runtimepredictive.RequestAwareInput{}, predictiveObserverSnapshot{}, false
	}
	if source, ok := provider.(interface {
		Snapshot(time.Time) predictiveObserverSnapshot
	}); ok {
		defer func() {
			if recover() != nil {
				input = runtimepredictive.RequestAwareInput{}
				observer = predictiveObserverSnapshot{}
				valid = false
			}
		}()
		observer = source.Snapshot(now)
		return runtimepredictive.RequestAwareInput{
			MetricsFresh:        observer.MetricsFresh,
			IdentityValid:       observer.IdentityValid,
			ObservationSequence: observer.ObservationSequence,
			CapacityTokens:      observer.CapacityTokens,
			UsedTokens:          observer.UsedTokens,
			Running:             observer.Running,
			Waiting:             observer.Waiting,
			AggregateTPSProxy:   observer.AggregateTPS,
			MeanActiveTPSProxy:  observer.MeanActiveTPS,
			TPSValid:            observer.TPSValid,
			PreemptionObserved:  observer.PreemptionObserved,
		}, observer, true
	}
	input, valid = requestAwareAdapterSnapshot(provider, now)
	return input, predictiveObserverSnapshot{}, valid
}

func requestAwareAdapterProtectedDecision(decision runtimepredictive.RequestAwareDecision) predictiveAdmissionDecision {
	source := runtimepredictive.PredictionSourceDeterministic
	switch decision.Reason {
	case runtimepredictive.RequestAwareReasonDecodeInterference:
		return requestAwareAdapterFailure(predictiveAdmissionOutcomeRequestReject, domainpredictive.ReasonRequestSizeAtPressure, source)
	case runtimepredictive.RequestAwareReasonPrefillBudget,
		runtimepredictive.RequestAwareReasonPrefillConcurrency,
		runtimepredictive.RequestAwareReasonPrefillExclusive,
		runtimepredictive.RequestAwareReasonPrefillBusy:
		return requestAwareAdapterFailure(predictiveAdmissionOutcomeLoadProtection, domainpredictive.ReasonRequestSizeAtPressure, source)
	case runtimepredictive.RequestAwareReasonKV:
		return requestAwareAdapterFailure(predictiveAdmissionOutcomeLoadProtection, domainpredictive.ReasonKVOverBudget, source)
	case runtimepredictive.RequestAwareReasonPreemption:
		return requestAwareAdapterFailure(predictiveAdmissionOutcomeLoadProtection, domainpredictive.ReasonPreemptionObserved, source)
	case runtimepredictive.RequestAwareReasonStale, runtimepredictive.RequestAwareReasonUnavailable:
		return requestAwareAdapterFailure(predictiveAdmissionOutcomeAvailabilityProtection, domainpredictive.ReasonMetricsStale, runtimepredictive.PredictionSourceUnavailable)
	case runtimepredictive.RequestAwareReasonDuplicate:
		return requestAwareAdapterFailure(predictiveAdmissionOutcomeRequestReject, domainpredictive.ReasonDuplicateRequest, source)
	default:
		return requestAwareAdapterFailure(predictiveAdmissionOutcomeRequestReject, domainpredictive.ReasonPredictorProfileUnknown, source)
	}
}

func requestAwareAdapterFailure(outcome predictiveAdmissionOutcome, reason domainpredictive.Reason, source runtimepredictive.PredictionSource) predictiveAdmissionDecision {
	return predictiveAdmissionDecision{Outcome: outcome, Reason: reason, Source: source}
}
