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

type assimilationState uint8

const (
	assimilationUnabsorbed assimilationState = iota
	assimilationAmbiguous
	assimilationAbsorbed
)

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
	Assimilation              assimilationState
	BackendPresenceAbsorbed   bool
	ActiveDecodeAbsorbed      bool
}

const maximumRetiredReservations = 4_096

type retiredReservation struct {
	CompletedSequence        uint64
	MaterializedFloor        domain.RequestCost
	CompletedDecodeSequences int
}

type retiredReservationQueue struct {
	items                    []retiredReservation
	head                     int
	size                     int
	completedDecodeSequences int
}

func (q *retiredReservationQueue) Push(item retiredReservation) bool {
	if item.CompletedDecodeSequences < 0 {
		item.CompletedDecodeSequences = 0
	}
	if q.items == nil {
		q.items = make([]retiredReservation, maximumRetiredReservations)
	}
	if q.size < len(q.items) {
		q.items[(q.head+q.size)%len(q.items)] = item
		q.size++
		q.completedDecodeSequences = addIntSaturating(q.completedDecodeSequences, item.CompletedDecodeSequences)
		return false
	}
	q.completedDecodeSequences = subtractIntFloorZero(
		q.completedDecodeSequences,
		q.items[q.head].CompletedDecodeSequences,
	)
	q.items[q.head] = item
	q.head = (q.head + 1) % len(q.items)
	q.completedDecodeSequences = addIntSaturating(q.completedDecodeSequences, item.CompletedDecodeSequences)
	return true
}

func (q *retiredReservationQueue) Pop() (retiredReservation, bool) {
	if q.size == 0 {
		return retiredReservation{}, false
	}
	item := q.items[q.head]
	q.completedDecodeSequences = subtractIntFloorZero(q.completedDecodeSequences, item.CompletedDecodeSequences)
	q.items[q.head] = retiredReservation{}
	q.head = (q.head + 1) % len(q.items)
	q.size--
	if q.size == 0 {
		q.head = 0
	}
	return item, true
}

func (q *retiredReservationQueue) Len() int { return q.size }

func (q *retiredReservationQueue) CompletedDecodeSequences() int {
	return q.completedDecodeSequences
}

type SampleWindow struct {
	Observed            domain.VirtualState
	StartedSequence     uint64
	FinishedSequence    uint64
	ObservationSequence uint64
}

type Manager struct {
	mu                     sync.Mutex
	manifestID             string
	intakeOpen             bool
	base                   domain.VirtualStateInterval
	reservations           map[string]reservation
	retired                retiredReservationQueue
	retiredEvictions       uint64
	eventSequence          uint64
	pendingPrefillSequence uint64
	lastSampleFinished     uint64
	observationSequence    uint64
	hasSample              bool
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
	RetiredReservations             int
	RetiredEvictions                uint64
	CompletedDecodeCredits          int
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
	if item.Assimilation == assimilationAbsorbed && item.PrefillComplete {
		materialized := materializedStateFloor(item.Cost)
		m.base.Lower = subtractState(m.base.Lower, materialized)
		m.base.Upper = subtractState(m.base.Upper, materialized)
		completedDecodeSequences := 0
		if cause == TerminalCompleted && item.ActiveDecodeAbsorbed {
			completedDecodeSequences = item.Cost.DecodeSequencesUpper
		}
		if m.retired.Push(retiredReservation{
			CompletedSequence:        m.eventSequence,
			MaterializedFloor:        materialized,
			CompletedDecodeSequences: completedDecodeSequences,
		}) {
			m.retiredEvictions++
		}
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
	m.retired = retiredReservationQueue{}
	m.lastSampleFinished = m.eventSequence
	m.observationSequence = 0
	m.hasSample = true
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
	previousBase := m.base.Upper
	m.base = domain.VirtualStateInterval{Lower: sample.Observed, Upper: sample.Observed}
	previousPresence := domain.RequestCost{}
	newPresence := domain.RequestCost{}
	for _, item := range m.reservations {
		materialized := materializedStateFloor(item.Cost)
		switch {
		case item.BackendPresenceAbsorbed:
			addMaterializedFloor(&previousPresence, materialized)
		case reservationEligibleForBackendObservation(item, sample):
			addMaterializedFloor(&newPresence, materialized)
		}
	}
	observedActiveDecodeSequences := sample.Observed.DecodeSequences - sample.Observed.PendingPrefillSequences
	previousActiveDecodeSequences := subtractIntFloorZero(
		previousBase.DecodeSequences,
		previousBase.PendingPrefillSequences,
	)
	previousUnowned := domain.RequestCost{
		KV: domain.KVIncrement{
			PhysicalKVUpper: subtractFloorZero(previousBase.PhysicalKVUpper, previousPresence.KV.PhysicalKVUpper),
			ActiveKVUpper:   subtractFloorZero(previousBase.ActiveKVUpper, previousPresence.KV.ActiveKVUpper),
		},
		DecodeSequencesUpper: subtractIntFloorZero(previousActiveDecodeSequences, previousPresence.DecodeSequencesUpper),
		ActiveContextTokensUpper: subtractFloorZero(
			previousBase.ActiveContextTokens,
			previousPresence.ActiveContextTokensUpper,
		),
	}
	retainedPresenceObserved := previousPresence.DecodeSequencesUpper > 0 && observationCoversMaterialized(
		sample.Observed, observedActiveDecodeSequences, previousUnowned, previousPresence,
	)
	newPresenceObserved := newPresence.DecodeSequencesUpper > 0 && observationCoversMaterialized(
		sample.Observed, observedActiveDecodeSequences, previousUnowned, previousPresence, newPresence,
	)
	for id, item := range m.reservations {
		switch {
		case item.BackendPresenceAbsorbed:
			item.BackendPresenceAbsorbed = retainedPresenceObserved
		case reservationEligibleForBackendObservation(item, sample):
			item.BackendPresenceAbsorbed = newPresenceObserved
		default:
			item.BackendPresenceAbsorbed = false
		}
		item.ActiveDecodeAbsorbed = false
		switch {
		case !item.Forwarded || !item.PrefillComplete:
			item.Assimilation = assimilationUnabsorbed
		case reservationAbsorbedBySample(item, sample):
			item.Assimilation = assimilationAbsorbed
			item.ActiveDecodeAbsorbed = item.BackendPresenceAbsorbed
		case item.ForwardedSequence > sample.FinishedSequence || item.PrefillCompletedSequence > sample.FinishedSequence:
			item.Assimilation = assimilationUnabsorbed
		default:
			item.Assimilation = assimilationAmbiguous
		}
		m.reservations[id] = item
	}
	retiredCount := m.retired.Len()
	for range retiredCount {
		item, ok := m.retired.Pop()
		if !ok {
			break
		}
		switch {
		case item.CompletedSequence <= sample.StartedSequence:
			continue
		case item.CompletedSequence > sample.FinishedSequence:
			m.base.Lower = subtractState(m.base.Lower, item.MaterializedFloor)
			m.base.Upper = subtractState(m.base.Upper, item.MaterializedFloor)
		default:
			m.base.Lower = subtractState(m.base.Lower, item.MaterializedFloor)
			item.CompletedDecodeSequences = 0
		}
		m.retired.Push(item)
	}
	m.lastSampleFinished = sample.FinishedSequence
	m.observationSequence = sample.ObservationSequence
	m.hasSample = true
	return nil
}

func reservationAbsorbedBySample(item reservation, sample SampleWindow) bool {
	return item.Forwarded && item.PrefillComplete &&
		item.ForwardedSequence <= sample.StartedSequence &&
		item.PrefillCompletedSequence <= sample.StartedSequence
}

func reservationEligibleForBackendObservation(item reservation, sample SampleWindow) bool {
	return item.Forwarded && item.ForwardedSequence <= sample.StartedSequence
}

func addMaterializedFloor(total *domain.RequestCost, increment domain.RequestCost) {
	total.KV.PhysicalKVUpper = addInt64Saturating(total.KV.PhysicalKVUpper, increment.KV.PhysicalKVUpper)
	total.KV.ActiveKVUpper = addInt64Saturating(total.KV.ActiveKVUpper, increment.KV.ActiveKVUpper)
	total.DecodeSequencesUpper = addIntSaturating(total.DecodeSequencesUpper, increment.DecodeSequencesUpper)
	total.ActiveContextTokensUpper = addInt64Saturating(
		total.ActiveContextTokensUpper,
		increment.ActiveContextTokensUpper,
	)
}

func observationCoversMaterialized(
	observed domain.VirtualState,
	observedActiveDecodeSequences int,
	components ...domain.RequestCost,
) bool {
	required := domain.RequestCost{}
	for _, component := range components {
		addMaterializedFloor(&required, component)
	}
	return observedActiveDecodeSequences >= required.DecodeSequencesUpper &&
		observed.PhysicalKVUpper >= required.KV.PhysicalKVUpper &&
		observed.ActiveKVUpper >= required.KV.ActiveKVUpper &&
		observed.ActiveContextTokens >= required.ActiveContextTokensUpper
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
		RetiredReservations:             m.retired.Len(),
		RetiredEvictions:                m.retiredEvictions,
		CompletedDecodeCredits:          m.retired.CompletedDecodeSequences(),
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
	switch item.Assimilation {
	case assimilationUnabsorbed:
		cost := fullReservationStateCost(item)
		state.Lower = addState(state.Lower, cost)
		state.Upper = addState(state.Upper, cost)
		if !item.PrefillComplete {
			state.Lower.PendingPrefillSequences = addIntSaturating(state.Lower.PendingPrefillSequences, 1)
			state.Upper.PendingPrefillSequences = addIntSaturating(state.Upper.PendingPrefillSequences, 1)
		}
	case assimilationAmbiguous:
		state.Lower = addState(state.Lower, futureReservationStateCost(item))
		state.Upper = addState(state.Upper, fullReservationStateCost(item))
	case assimilationAbsorbed:
		cost := futureReservationStateCost(item)
		state.Lower = addState(state.Lower, cost)
		state.Upper = addState(state.Upper, cost)
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

func materializedStateFloor(cost domain.RequestCost) domain.RequestCost {
	return domain.RequestCost{
		KV: domain.KVIncrement{
			PhysicalKVUpper: cost.KV.PhysicalKVUpper - cost.FutureKV.PhysicalKVUpper,
			ActiveKVUpper:   cost.KV.ActiveKVUpper - cost.FutureKV.ActiveKVUpper,
		},
		DecodeSequencesUpper:     cost.DecodeSequencesUpper,
		ActiveContextTokensUpper: cost.ActiveContextTokensUpper - cost.FutureContextTokensUpper,
	}
}

func addState(state domain.VirtualState, cost domain.RequestCost) domain.VirtualState {
	state.PhysicalKVUpper = addInt64Saturating(state.PhysicalKVUpper, cost.KV.PhysicalKVUpper)
	state.ActiveKVUpper = addInt64Saturating(state.ActiveKVUpper, cost.KV.ActiveKVUpper)
	state.DecodeSequences = addIntSaturating(state.DecodeSequences, cost.DecodeSequencesUpper)
	state.ActiveContextTokens = addInt64Saturating(state.ActiveContextTokens, cost.ActiveContextTokensUpper)
	state.UncachedPrefillTokens = addInt64Saturating(state.UncachedPrefillTokens, cost.UncachedPrefillUpper)
	return state
}

func subtractState(state domain.VirtualState, cost domain.RequestCost) domain.VirtualState {
	state.PhysicalKVUpper = subtractFloorZero(state.PhysicalKVUpper, cost.KV.PhysicalKVUpper)
	state.ActiveKVUpper = subtractFloorZero(state.ActiveKVUpper, cost.KV.ActiveKVUpper)
	state.DecodeSequences = subtractIntFloorZero(state.DecodeSequences, cost.DecodeSequencesUpper)
	state.ActiveContextTokens = subtractFloorZero(state.ActiveContextTokens, cost.ActiveContextTokensUpper)
	state.UncachedPrefillTokens = subtractFloorZero(state.UncachedPrefillTokens, cost.UncachedPrefillUpper)
	return state
}

func subtractFloorZero(value, decrement int64) int64 {
	if decrement >= value {
		return 0
	}
	return value - decrement
}

func subtractIntFloorZero(value, decrement int) int {
	if decrement >= value {
		return 0
	}
	return value - decrement
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
