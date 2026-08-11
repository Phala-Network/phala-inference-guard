package predictive

import (
	"fmt"
	"math"
	"sync"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type TerminalCause string

const (
	TerminalCompleted          TerminalCause = "completed"
	TerminalLocalQoSReject     TerminalCause = "local_qos_reject"
	TerminalClientCancelled    TerminalCause = "client_cancelled"
	TerminalClientDisconnected TerminalCause = "client_disconnected"
	TerminalUpstreamFailure    TerminalCause = "upstream_failure"
	TerminalTimeout            TerminalCause = "timeout"
	TerminalExpired            TerminalCause = "expired"
)

func (c TerminalCause) Validate() error {
	switch c {
	case TerminalCompleted, TerminalLocalQoSReject, TerminalClientCancelled,
		TerminalClientDisconnected, TerminalUpstreamFailure, TerminalTimeout, TerminalExpired:
		return nil
	default:
		return fmt.Errorf("predictive terminal cause %q is invalid", c)
	}
}

type reservation struct {
	ID                        string
	Created                   time.Time
	Cost                      domain.RequestCost
	PrefillInterferenceTokens int64
	Forwarded                 bool
	PrefillComplete           bool
	AdmittedSequence          uint64
	ForwardedSequence         uint64
	PrefillCompletedSequence  uint64
	InputCoveredByObservation bool
}

type SampleWindow struct {
	Observed                domain.VirtualState
	StartedSequence         uint64
	FinishedSequence        uint64
	ObservationSequence     uint64
	RequestAwareObservation *RequestAwareObservation
}

type Manager struct {
	mu                         sync.Mutex
	manifestID                 string
	intakeOpen                 bool
	base                       domain.VirtualStateInterval
	reservations               map[string]reservation
	eventSequence              uint64
	pendingPrefillSequence     uint64
	lastSampleFinished         uint64
	observationSequence        uint64
	hasSample                  bool
	requestAwareObservation    RequestAwareObservation
	hasRequestAwareObservation bool
}

type Snapshot struct {
	IntakeOpen                      bool
	Reservations                    int
	ReservedPhysicalKV              int64
	ReservedActiveKV                int64
	ForwardedPendingPrefills        int
	ForwardedPendingPrefillTokens   int64
	ForwardedPendingPrefillSequence uint64
	EventSequence                   uint64
	Virtual                         domain.VirtualStateInterval
}

type RequestAwarePendingSnapshot struct {
	PrefillSequences          int
	PrefillTokens             int64
	LongPrefillSequences      int
	QuiescentPrefillSequences int
	UnknownPrefillSequences   int
}

func NewManager(manifestID string, base domain.VirtualState) *Manager {
	return &Manager{
		manifestID:   manifestID,
		intakeOpen:   true,
		base:         domain.VirtualStateInterval{Lower: base, Upper: base},
		reservations: make(map[string]reservation),
	}
}

func (m *Manager) Available() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.intakeOpen
}

func (m *Manager) MarkForwarded(requestID string) bool {
	if m == nil || requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	if !m.intakeOpen || !exists || item.Forwarded {
		return false
	}
	m.eventSequence++
	m.advancePendingPrefillEpisodeLocked()
	item.Forwarded = true
	item.ForwardedSequence = m.eventSequence
	m.reservations[requestID] = item
	return true
}

func (m *Manager) MarkPrefillComplete(requestID string) bool {
	if m == nil || requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	if !exists || !item.Forwarded || item.PrefillComplete {
		return false
	}
	m.eventSequence++
	m.advancePendingPrefillEpisodeLocked()
	item.PrefillComplete = true
	item.PrefillCompletedSequence = m.eventSequence
	m.reservations[requestID] = item
	return true
}

func (m *Manager) Terminate(requestID string, cause TerminalCause) bool {
	if m == nil || requestID == "" || cause.Validate() != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	if !exists {
		return false
	}
	m.eventSequence++
	if item.Forwarded && !item.PrefillComplete {
		m.advancePendingPrefillEpisodeLocked()
	}
	delete(m.reservations, requestID)
	return true
}

func (m *Manager) InvalidateEpoch() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := m.intakeOpen
	m.intakeOpen = false
	return changed
}

func (m *Manager) RebaseEpoch(observed domain.VirtualState) error {
	if m == nil {
		return fmt.Errorf("predictive manager is nil")
	}
	if !validVirtualState(observed) {
		return fmt.Errorf("predictive epoch base must be non-negative and internally consistent")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.intakeOpen = false
	m.eventSequence++
	m.advancePendingPrefillEpisodeLocked()
	m.base = domain.VirtualStateInterval{Lower: observed, Upper: observed}
	clear(m.reservations)
	m.lastSampleFinished = m.eventSequence
	m.observationSequence = 0
	m.hasSample = true
	m.requestAwareObservation = RequestAwareObservation{}
	m.hasRequestAwareObservation = false
	m.intakeOpen = true
	return nil
}

func (m *Manager) EventSequence() uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.eventSequence
}

func (m *Manager) StartSampleWindow() uint64 { return m.EventSequence() }

func (m *Manager) ReconcileSample(sample SampleWindow) error {
	if m == nil {
		return fmt.Errorf("predictive manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if sample.StartedSequence > sample.FinishedSequence || sample.FinishedSequence > m.eventSequence {
		return fmt.Errorf("predictive sample watermarks are invalid")
	}
	if m.hasSample && sample.FinishedSequence < m.lastSampleFinished {
		return fmt.Errorf("predictive sample finish watermark is stale")
	}
	if sample.ObservationSequence != 0 && sample.ObservationSequence <= m.observationSequence {
		return fmt.Errorf("predictive observation sequence is stale")
	}
	if sample.ObservationSequence == 0 && m.observationSequence != 0 {
		return fmt.Errorf("predictive observation sequence is stale")
	}
	if !validVirtualState(sample.Observed) {
		return fmt.Errorf("predictive sample state must be non-negative")
	}
	if sample.RequestAwareObservation != nil &&
		!validRequestAwareObservationForSample(*sample.RequestAwareObservation, sample) {
		return fmt.Errorf("predictive request-aware observation is inconsistent with sample")
	}
	m.base = domain.VirtualStateInterval{Lower: sample.Observed, Upper: sample.Observed}
	for id, item := range m.reservations {
		if item.PrefillComplete && item.PrefillCompletedSequence <= sample.StartedSequence {
			item.InputCoveredByObservation = true
			m.reservations[id] = item
		}
	}
	m.lastSampleFinished = sample.FinishedSequence
	m.observationSequence = sample.ObservationSequence
	m.hasSample = true
	if sample.RequestAwareObservation != nil {
		m.requestAwareObservation = *sample.RequestAwareObservation
		m.hasRequestAwareObservation = true
	}
	return nil
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := Snapshot{
		IntakeOpen:                      m.intakeOpen,
		Reservations:                    len(m.reservations),
		ForwardedPendingPrefillSequence: m.pendingPrefillSequence,
		EventSequence:                   m.eventSequence,
		Virtual:                         m.virtualStateIntervalLocked(),
	}
	for _, item := range m.reservations {
		result.ReservedPhysicalKV = addInt64Saturating(result.ReservedPhysicalKV, item.Cost.KV.PhysicalKVUpper)
		result.ReservedActiveKV = addInt64Saturating(result.ReservedActiveKV, item.Cost.KV.ActiveKVUpper)
		if item.Forwarded && !item.PrefillComplete {
			result.ForwardedPendingPrefills++
			tokens := item.PrefillInterferenceTokens
			if tokens <= 0 {
				tokens = item.Cost.UncachedPrefillUpper
			}
			result.ForwardedPendingPrefillTokens = addInt64Saturating(result.ForwardedPendingPrefillTokens, tokens)
		}
	}
	return result
}

func (m *Manager) advancePendingPrefillEpisodeLocked() {
	m.pendingPrefillSequence++
	if m.pendingPrefillSequence == 0 {
		m.pendingPrefillSequence = 1
	}
}

func (m *Manager) virtualStateIntervalLocked() domain.VirtualStateInterval {
	state := m.base
	for _, item := range m.reservations {
		addReservationToStateInterval(&state, &item)
	}
	return state
}

func addReservationToStateInterval(state *domain.VirtualStateInterval, item *reservation) {
	if item.InputCoveredByObservation {
		cost := futureReservationStateCost(item)
		state.Lower = addState(state.Lower, cost)
		state.Upper = addState(state.Upper, cost)
		return
	}
	cost := fullReservationStateCost(item)
	state.Lower = addState(state.Lower, cost)
	state.Upper = addState(state.Upper, cost)
	if !item.PrefillComplete {
		state.Lower.PendingPrefillSequences = addIntSaturating(state.Lower.PendingPrefillSequences, 1)
		state.Upper.PendingPrefillSequences = addIntSaturating(state.Upper.PendingPrefillSequences, 1)
	}
}

func fullReservationStateCost(item *reservation) domain.RequestCost {
	cost := item.Cost
	if item.PrefillComplete {
		cost.UncachedPrefillUpper = 0
	}
	return cost
}

func futureReservationStateCost(item *reservation) domain.RequestCost {
	return domain.RequestCost{KV: item.Cost.FutureKV, ActiveContextTokensUpper: item.Cost.FutureContextTokensUpper}
}

func addState(state domain.VirtualState, cost domain.RequestCost) domain.VirtualState {
	state.PhysicalKVUpper = addInt64Saturating(state.PhysicalKVUpper, cost.KV.PhysicalKVUpper)
	state.ActiveKVUpper = addInt64Saturating(state.ActiveKVUpper, cost.KV.ActiveKVUpper)
	state.DecodeSequences = addIntSaturating(state.DecodeSequences, cost.DecodeSequencesUpper)
	state.ActiveContextTokens = addInt64Saturating(state.ActiveContextTokens, cost.ActiveContextTokensUpper)
	state.UncachedPrefillTokens = addInt64Saturating(state.UncachedPrefillTokens, cost.UncachedPrefillUpper)
	return state
}

func validVirtualState(state domain.VirtualState) bool {
	return state.PhysicalKVUpper >= 0 && state.ActiveKVUpper >= 0 && state.DecodeSequences >= 0 &&
		state.PendingPrefillSequences >= 0 && state.PendingPrefillSequences <= state.DecodeSequences &&
		state.ActiveContextTokens >= 0 && state.UncachedPrefillTokens >= 0
}

func validRequestCost(cost domain.RequestCost) bool {
	if cost.InputTokens < 0 || cost.UncachedPrefillUpper != cost.InputTokens ||
		cost.DecodeHorizonUpper < 0 || cost.DecodeSequencesUpper != 1 || cost.InputTokens > math.MaxInt64-cost.DecodeHorizonUpper {
		return false
	}
	if cost.ActiveContextTokensUpper != cost.InputTokens+cost.DecodeHorizonUpper || cost.FutureContextTokensUpper != cost.DecodeHorizonUpper ||
		cost.KV.PhysicalKVUpper < 0 || cost.KV.PhysicalKVUpper != cost.KV.ActiveKVUpper || cost.KV.PhysicalKVUpper < cost.ActiveContextTokensUpper ||
		cost.FutureKV.PhysicalKVUpper < 0 || cost.FutureKV.PhysicalKVUpper != cost.FutureKV.ActiveKVUpper || cost.FutureKV.PhysicalKVUpper > cost.KV.PhysicalKVUpper ||
		cost.KV.PhysicalKVUpper-cost.FutureKV.PhysicalKVUpper < cost.InputTokens {
		return false
	}
	return cost.Confidence > 0 && cost.Confidence <= 1 && !math.IsNaN(cost.Confidence) && !math.IsInf(cost.Confidence, 0)
}

func addInt64Saturating(left, right int64) int64 {
	if right > 0 && left > math.MaxInt64-right {
		return math.MaxInt64
	}
	return left + right
}

func addIntSaturating(left, right int) int {
	maximum := int(^uint(0) >> 1)
	if right > 0 && left > maximum-right {
		return maximum
	}
	return left + right
}
