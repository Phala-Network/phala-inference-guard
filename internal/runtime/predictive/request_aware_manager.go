package predictive

import (
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type RequestAwareManagerResult struct {
	Decision                     RequestAwareDecision
	Reserved                     bool
	DecisionManagerSequence      uint64
	DecisionManagerSequenceValid bool
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

	if !m.intakeOpen || policy == nil {
		return requestAwareManagerFailure(RequestAwareReasonUnavailable, m.eventSequence, true)
	}
	if m.manifestID == "" || cost.ManifestID == "" || cost.ManifestID != m.manifestID ||
		requestID == "" || selectionInputTokens <= 0 || !validRequestCost(cost) {
		return requestAwareManagerFailure(RequestAwareReasonInvalid, m.eventSequence, true)
	}
	if _, exists := m.reservations[requestID]; exists {
		return requestAwareManagerFailure(RequestAwareReasonDuplicate, m.eventSequence, true)
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
	}
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
