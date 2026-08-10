package predictive

import (
	"fmt"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type RequestAwareManagerResult struct {
	Decision                     RequestAwareDecision
	Reserved                     bool
	DecisionManagerSequence      uint64
	DecisionManagerSequenceValid bool
	Observation                  RequestAwareInput
}

type RequestAwareObservation struct {
	ObservedAt          time.Time
	MaximumAge          time.Duration
	IdentityValid       bool
	ObservationSequence uint64
	CapacityTokens      int64
	UsedTokens          int64
	Running             int
	Waiting             int
	AggregateTPSProxy   float64
	MeanActiveTPSProxy  float64
	TPSValid            bool
	GenerationObserved  bool
	PreemptionObserved  bool
}

type requestAwareStateSnapshot struct {
	Virtual                          domain.VirtualState
	PendingPrefillTokens             int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
	PendingUnknownPrefillSequences   int
	UnobservedActiveDecodeSequences  int
	CompletedDecodeCredits           int
	ObservationSequence              uint64
}

func (m *Manager) DecideRequestAwareAndReserve(
	now time.Time,
	requestID string,
	cost domain.RequestCost,
	selectionInputTokens int64,
	policy *RequestAwarePolicy,
	input RequestAwareInput,
) RequestAwareManagerResult {
	return m.decideRequestAware(now, requestID, cost, selectionInputTokens, policy, input, true)
}

func (m *Manager) DecideCurrentRequestAwareAndReserve(
	now time.Time,
	requestID string,
	cost domain.RequestCost,
	selectionInputTokens int64,
	policy *RequestAwarePolicy,
) RequestAwareManagerResult {
	return m.decideCurrentRequestAware(now, requestID, cost, selectionInputTokens, policy, true)
}

func (m *Manager) DecideCurrentRequestAware(
	now time.Time,
	requestID string,
	cost domain.RequestCost,
	selectionInputTokens int64,
	policy *RequestAwarePolicy,
) RequestAwareManagerResult {
	return m.decideCurrentRequestAware(now, requestID, cost, selectionInputTokens, policy, false)
}

func (m *Manager) DecideRequestAware(
	now time.Time,
	requestID string,
	cost domain.RequestCost,
	selectionInputTokens int64,
	policy *RequestAwarePolicy,
	input RequestAwareInput,
) RequestAwareManagerResult {
	return m.decideRequestAware(now, requestID, cost, selectionInputTokens, policy, input, false)
}

func (m *Manager) decideRequestAware(
	now time.Time,
	requestID string,
	cost domain.RequestCost,
	selectionInputTokens int64,
	policy *RequestAwarePolicy,
	input RequestAwareInput,
	reserve bool,
) RequestAwareManagerResult {
	if m == nil {
		return requestAwareManagerFailure(RequestAwareReasonUnavailable, 0, false)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.decideRequestAwareLocked(now, requestID, cost, selectionInputTokens, policy, input, reserve)
}

func (m *Manager) decideCurrentRequestAware(
	now time.Time,
	requestID string,
	cost domain.RequestCost,
	selectionInputTokens int64,
	policy *RequestAwarePolicy,
	reserve bool,
) RequestAwareManagerResult {
	if m == nil {
		return requestAwareManagerFailure(RequestAwareReasonUnavailable, 0, false)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	input := m.requestAwareInputLocked(now)
	return m.decideRequestAwareLocked(now, requestID, cost, selectionInputTokens, policy, input, reserve)
}

func (m *Manager) decideRequestAwareLocked(
	now time.Time,
	requestID string,
	cost domain.RequestCost,
	selectionInputTokens int64,
	policy *RequestAwarePolicy,
	input RequestAwareInput,
	reserve bool,
) RequestAwareManagerResult {

	if !m.intakeOpen || policy == nil {
		return requestAwareManagerFailureWithObservation(RequestAwareReasonUnavailable, m.eventSequence, true, input)
	}
	if m.manifestID == "" || cost.ManifestID == "" || cost.ManifestID != m.manifestID ||
		requestID == "" || selectionInputTokens <= 0 || !validRequestCost(cost) {
		return requestAwareManagerFailureWithObservation(RequestAwareReasonInvalid, m.eventSequence, true, input)
	}
	if _, exists := m.reservations[requestID]; exists {
		return requestAwareManagerFailureWithObservation(RequestAwareReasonDuplicate, m.eventSequence, true, input)
	}

	state := m.requestAwareStateLocked(policy)
	effectiveKV := state.Virtual.ActiveKVUpper
	if state.Virtual.PhysicalKVUpper > effectiveKV {
		effectiveKV = state.Virtual.PhysicalKVUpper
	}
	requestReservedTokens := cost.KV.ActiveKVUpper
	if cost.KV.PhysicalKVUpper > requestReservedTokens {
		requestReservedTokens = cost.KV.PhysicalKVUpper
	}
	input.UsedTokens = effectiveKV
	input.ReservedTokens = 0
	observedActiveDecodeSequences := input.Running
	if input.ObservationSequence == state.ObservationSequence {
		observedActiveDecodeSequences = subtractIntFloorZero(input.Running, state.CompletedDecodeCredits)
	}
	input.EffectiveSequences = addIntSaturating(observedActiveDecodeSequences, state.UnobservedActiveDecodeSequences)
	input.RequestReservedTokens = requestReservedTokens
	input.SelectionInputTokens = selectionInputTokens
	input.EstimatedPrefillTokens = selectionInputTokens
	input.PendingPrefillSequences = state.Virtual.PendingPrefillSequences
	input.PendingPrefillTokens = state.PendingPrefillTokens
	input.PendingLongPrefillSequences = state.PendingLongPrefillSequences
	input.PendingQuiescentPrefillSequences = state.PendingQuiescentPrefillSequences
	input.PendingUnknownPrefillSequences = state.PendingUnknownPrefillSequences
	decision := policy.Evaluate(input)
	if decision.Action != RequestAwareAdmit || !reserve {
		return RequestAwareManagerResult{
			Decision:                     decision,
			DecisionManagerSequence:      m.eventSequence,
			DecisionManagerSequenceValid: true,
			Observation:                  input,
		}
	}

	m.eventSequence++
	m.reservations[requestID] = reservation{
		ID:                        requestID,
		Created:                   now,
		Cost:                      cost,
		PrefillInterferenceTokens: selectionInputTokens,
		AdmittedSequence:          m.eventSequence,
		Assimilation:              assimilationUnabsorbed,
	}
	return RequestAwareManagerResult{
		Decision:                     decision,
		Reserved:                     true,
		DecisionManagerSequence:      m.eventSequence,
		DecisionManagerSequenceValid: true,
		Observation:                  input,
	}
}

func (m *Manager) InitializeRequestAwareObservation(observation RequestAwareObservation) error {
	if m == nil {
		return fmt.Errorf("predictive Manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.hasRequestAwareObservation {
		return fmt.Errorf("predictive request-aware observation is already initialized")
	}
	if !validRequestAwareObservation(observation) || observation.ObservationSequence != 0 ||
		observation.UsedTokens != m.base.Upper.PhysicalKVUpper ||
		m.base.Upper.PhysicalKVUpper != m.base.Upper.ActiveKVUpper ||
		observation.Running+observation.Waiting != m.base.Upper.DecodeSequences ||
		observation.Waiting != m.base.Upper.PendingPrefillSequences {
		return fmt.Errorf("predictive initial request-aware observation is inconsistent with Manager state")
	}
	m.requestAwareObservation = observation
	m.hasRequestAwareObservation = true
	return nil
}

func (m *Manager) HasRequestAwareObservation() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.hasRequestAwareObservation
}

func (m *Manager) requestAwareInputLocked(now time.Time) RequestAwareInput {
	if !m.hasRequestAwareObservation {
		return RequestAwareInput{}
	}
	observation := m.requestAwareObservation
	fresh := m.intakeOpen && observation.IdentityValid && !now.IsZero() &&
		!now.Before(observation.ObservedAt) && now.Sub(observation.ObservedAt) <= observation.MaximumAge
	input := RequestAwareInput{
		MetricsFresh:        fresh,
		IdentityValid:       m.intakeOpen && observation.IdentityValid,
		ObservationSequence: observation.ObservationSequence,
		CapacityTokens:      observation.CapacityTokens,
		UsedTokens:          observation.UsedTokens,
		Running:             observation.Running,
		Waiting:             observation.Waiting,
		AggregateTPSProxy:   observation.AggregateTPSProxy,
		MeanActiveTPSProxy:  observation.MeanActiveTPSProxy,
		TPSValid:            observation.TPSValid,
		PreemptionObserved:  observation.PreemptionObserved,
	}
	if !fresh {
		input.AggregateTPSProxy = 0
		input.MeanActiveTPSProxy = 0
		input.TPSValid = false
		input.PreemptionObserved = false
	}
	return input
}

func validRequestAwareObservation(observation RequestAwareObservation) bool {
	return !observation.ObservedAt.IsZero() && observation.MaximumAge > 0 &&
		observation.IdentityValid && observation.CapacityTokens > 0 &&
		observation.UsedTokens >= 0 && observation.UsedTokens <= observation.CapacityTokens &&
		observation.Running >= 0 && observation.Waiting >= 0 &&
		observation.Running <= int(^uint(0)>>1)-observation.Waiting &&
		requestAwareFinite(observation.AggregateTPSProxy) && observation.AggregateTPSProxy >= 0 &&
		requestAwareFinite(observation.MeanActiveTPSProxy) && observation.MeanActiveTPSProxy >= 0 &&
		(!observation.TPSValid || observation.AggregateTPSProxy > 0 && observation.MeanActiveTPSProxy > 0)
}

func validRequestAwareObservationForSample(observation RequestAwareObservation, sample SampleWindow) bool {
	return validRequestAwareObservation(observation) &&
		observation.ObservationSequence == sample.ObservationSequence &&
		observation.UsedTokens == sample.Observed.PhysicalKVUpper &&
		sample.Observed.PhysicalKVUpper == sample.Observed.ActiveKVUpper &&
		observation.Running+observation.Waiting == sample.Observed.DecodeSequences &&
		observation.Waiting == sample.Observed.PendingPrefillSequences
}

func (m *Manager) requestAwareStateLocked(policy *RequestAwarePolicy) requestAwareStateSnapshot {
	state := m.base
	snapshot := requestAwareStateSnapshot{
		PendingPrefillTokens:           state.Upper.UncachedPrefillTokens,
		PendingUnknownPrefillSequences: state.Upper.PendingPrefillSequences,
		CompletedDecodeCredits:         m.retired.CompletedDecodeSequences(),
		ObservationSequence:            m.observationSequence,
	}
	// Existing backend work has no attributable lexical estimate, so preserve
	// its observed safety upper. Local reservations replace only their own
	// upper with the request-aware interference estimate below.
	for _, item := range m.reservations {
		addReservationToStateInterval(&state, &item)
		if item.PrefillComplete {
			if item.Assimilation != assimilationAbsorbed {
				snapshot.UnobservedActiveDecodeSequences = addIntSaturating(
					snapshot.UnobservedActiveDecodeSequences,
					item.Cost.DecodeSequencesUpper,
				)
			}
			continue
		}
		prefillInterferenceTokens := item.PrefillInterferenceTokens
		if prefillInterferenceTokens <= 0 {
			prefillInterferenceTokens = item.Cost.UncachedPrefillUpper
			snapshot.PendingUnknownPrefillSequences = addIntSaturating(snapshot.PendingUnknownPrefillSequences, 1)
		}
		snapshot.PendingPrefillTokens = addInt64Saturating(snapshot.PendingPrefillTokens, prefillInterferenceTokens)
		switch policy.prefillClass(prefillInterferenceTokens) {
		case RequestAwarePrefillQuiescent:
			snapshot.PendingQuiescentPrefillSequences = addIntSaturating(snapshot.PendingQuiescentPrefillSequences, 1)
			snapshot.PendingLongPrefillSequences = addIntSaturating(snapshot.PendingLongPrefillSequences, 1)
		case RequestAwarePrefillWeighted, RequestAwarePrefillExclusive:
			snapshot.PendingLongPrefillSequences = addIntSaturating(snapshot.PendingLongPrefillSequences, 1)
		}
	}
	snapshot.Virtual = state.Upper
	return snapshot
}

func (m *Manager) CurrentRequestAwarePending(policy *RequestAwarePolicy) RequestAwarePendingSnapshot {
	if m == nil || policy == nil {
		return RequestAwarePendingSnapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.requestAwareStateLocked(policy)
	return RequestAwarePendingSnapshot{
		PrefillSequences:          state.Virtual.PendingPrefillSequences,
		PrefillTokens:             state.PendingPrefillTokens,
		LongPrefillSequences:      state.PendingLongPrefillSequences,
		QuiescentPrefillSequences: state.PendingQuiescentPrefillSequences,
		UnknownPrefillSequences:   state.PendingUnknownPrefillSequences,
	}
}

func requestAwareManagerFailure(reason RequestAwareReason, sequence uint64, sequenceValid bool) RequestAwareManagerResult {
	return RequestAwareManagerResult{
		Decision: RequestAwareDecision{
			Action: RequestAwareHardProtect,
			Reason: reason,
		},
		DecisionManagerSequence:      sequence,
		DecisionManagerSequenceValid: sequenceValid,
	}
}

func requestAwareManagerFailureWithObservation(
	reason RequestAwareReason,
	sequence uint64,
	sequenceValid bool,
	observation RequestAwareInput,
) RequestAwareManagerResult {
	result := requestAwareManagerFailure(reason, sequence, sequenceValid)
	result.Observation = observation
	return result
}
