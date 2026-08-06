package predictive

import (
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type RequestAwareManagerResult struct {
	Decision                RequestAwareDecision
	Reserved                bool
	DecisionManagerSequence uint64
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
		return requestAwareManagerFailure(RequestAwareReasonUnavailable, 0)
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.intakeOpen || policy == nil {
		return requestAwareManagerFailure(RequestAwareReasonUnavailable, m.eventSequence)
	}
	if m.manifestID == "" || cost.ManifestID == "" || cost.ManifestID != m.manifestID ||
		requestID == "" || selectionInputTokens <= 0 || !validRequestCost(cost) {
		return requestAwareManagerFailure(RequestAwareReasonInvalid, m.eventSequence)
	}
	if _, exists := m.reservations[requestID]; exists {
		return requestAwareManagerFailure(RequestAwareReasonDuplicate, m.eventSequence)
	}

	state, pendingPrefillTokens, pendingLong, pendingQuiescent := m.requestAwareStateLocked(policy)
	effectiveKV := state.ActiveKVUpper
	if state.PhysicalKVUpper > effectiveKV {
		effectiveKV = state.PhysicalKVUpper
	}
	requestReservedTokens := cost.KV.ActiveKVUpper
	if cost.KV.PhysicalKVUpper > requestReservedTokens {
		requestReservedTokens = cost.KV.PhysicalKVUpper
	}
	input.UsedTokens = effectiveKV
	input.ReservedTokens = 0
	input.EffectiveSequences = state.DecodeSequences - input.Waiting
	if input.EffectiveSequences < input.Running {
		input.EffectiveSequences = input.Running
	}
	input.RequestReservedTokens = requestReservedTokens
	input.SelectionInputTokens = selectionInputTokens
	input.EstimatedPrefillTokens = selectionInputTokens
	input.PendingPrefillSequences = state.PendingPrefillSequences
	input.PendingPrefillTokens = pendingPrefillTokens
	input.PendingLongPrefillSequences = pendingLong
	input.PendingQuiescentPrefillSequences = pendingQuiescent
	decision := policy.Evaluate(input)
	if decision.Action != RequestAwareAdmit || !reserve {
		return RequestAwareManagerResult{
			Decision:                decision,
			DecisionManagerSequence: m.eventSequence,
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
		Decision:                decision,
		Reserved:                true,
		DecisionManagerSequence: m.eventSequence,
	}
}

func (m *Manager) requestAwareStateLocked(policy *RequestAwarePolicy) (domain.VirtualState, int64, int, int) {
	state := m.base
	// Existing backend work has no attributable lexical estimate, so preserve
	// its observed safety upper. Local reservations replace only their own
	// upper with the request-aware interference estimate below.
	pendingPrefillTokens := state.Upper.UncachedPrefillTokens
	pendingLong := 0
	pendingQuiescent := 0
	for _, item := range m.reservations {
		addReservationToStateInterval(&state, &item)
		if item.PrefillComplete {
			continue
		}
		prefillInterferenceTokens := item.PrefillInterferenceTokens
		if prefillInterferenceTokens <= 0 {
			prefillInterferenceTokens = item.Cost.UncachedPrefillUpper
		}
		pendingPrefillTokens = addInt64Saturating(pendingPrefillTokens, prefillInterferenceTokens)
		switch policy.prefillClass(prefillInterferenceTokens) {
		case RequestAwarePrefillQuiescent:
			pendingQuiescent = addIntSaturating(pendingQuiescent, 1)
			pendingLong = addIntSaturating(pendingLong, 1)
		case RequestAwarePrefillExclusive:
			pendingLong = addIntSaturating(pendingLong, 1)
		}
	}
	return state.Upper, pendingPrefillTokens, pendingLong, pendingQuiescent
}

func requestAwareManagerFailure(reason RequestAwareReason, sequence uint64) RequestAwareManagerResult {
	return RequestAwareManagerResult{
		Decision: RequestAwareDecision{
			Action: RequestAwareHardProtect,
			Reason: reason,
		},
		DecisionManagerSequence: sequence,
	}
}
