package predictive

import (
	"fmt"
	"sync"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type Scheduler interface {
	Predict(state domain.VirtualState, request domain.RequestCost) domain.SchedulerEstimate
}

type assimilationState uint8

const (
	assimilationUnabsorbed assimilationState = iota
	assimilationAmbiguous
	assimilationAbsorbed
)

type reservation struct {
	ID               string
	Created          time.Time
	Cost             domain.RequestCost
	AdmittedSequence uint64
	Assimilation     assimilationState
}

type completedReservation struct {
	Reservation       reservation
	CompletedSequence uint64
}

type SampleWindow struct {
	Observed         domain.VirtualState
	StartedSequence  uint64
	FinishedSequence uint64
}

type Manager struct {
	mu                 sync.Mutex
	base               domain.VirtualStateInterval
	constraints        domain.Constraints
	scheduler          Scheduler
	reservations       map[string]reservation
	completed          map[string]completedReservation
	eventSequence      uint64
	lastSampleFinished uint64
	hasSample          bool
}

type Snapshot struct {
	Reservations       int
	ReservedPhysicalKV int64
	ReservedActiveKV   int64
	EventSequence      uint64
	Virtual            domain.VirtualStateInterval
}

func NewManager(base domain.VirtualState, constraints domain.Constraints, scheduler Scheduler) *Manager {
	return &Manager{
		base: domain.VirtualStateInterval{
			Lower: base,
			Upper: base,
		},
		constraints:  constraints,
		scheduler:    scheduler,
		reservations: make(map[string]reservation),
		completed:    make(map[string]completedReservation),
	}
}

func (m *Manager) DecideAndReserve(now time.Time, requestID string, cost domain.RequestCost) domain.Decision {
	if m == nil {
		return domain.Decision{Reason: domain.ReasonPredictorProfileUnknown}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.reservations[requestID]; exists {
		return domain.Decision{Reason: domain.ReasonDuplicateRequest}
	}
	state := m.virtualStateIntervalLocked().Upper
	estimate := domain.SchedulerEstimate{}
	if m.scheduler != nil {
		estimate = m.scheduler.Predict(state, cost)
	}
	projection := domain.Projection{
		PhysicalKVUpper: state.PhysicalKVUpper + cost.KV.PhysicalKVUpper,
		ActiveKVUpper:   state.ActiveKVUpper + cost.KV.ActiveKVUpper,
	}
	decision := domain.Evaluate(domain.EvaluationInput{
		Projection:  projection,
		Scheduler:   estimate,
		Constraints: m.constraints,
		Confidence:  cost.Confidence,
	})
	if decision.Reason == domain.ReasonFit {
		m.eventSequence++
		m.reservations[requestID] = reservation{
			ID:               requestID,
			Created:          now,
			Cost:             cost,
			AdmittedSequence: m.eventSequence,
			Assimilation:     assimilationUnabsorbed,
		}
	}
	return decision
}

func (m *Manager) Complete(requestID string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	if !exists {
		return false
	}
	m.eventSequence++
	switch item.Assimilation {
	case assimilationAbsorbed:
		m.base.Lower = subtractState(m.base.Lower, item.Cost.KV)
		m.base.Upper = subtractState(m.base.Upper, item.Cost.KV)
	case assimilationAmbiguous:
		m.base.Lower = subtractState(m.base.Lower, item.Cost.KV)
	}
	delete(m.reservations, requestID)
	m.completed[requestID] = completedReservation{
		Reservation:       item,
		CompletedSequence: m.eventSequence,
	}
	return true
}

func (m *Manager) EventSequence() uint64 {
	if m == nil {
		return 0
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.eventSequence
}

func (m *Manager) ReconcileSample(sample SampleWindow) error {
	if m == nil {
		return fmt.Errorf("predictive manager is nil")
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if sample.StartedSequence > sample.FinishedSequence {
		return fmt.Errorf("sample finish watermark precedes start")
	}
	if sample.FinishedSequence > m.eventSequence {
		return fmt.Errorf("sample finish watermark is in the future")
	}
	if m.hasSample && sample.FinishedSequence < m.lastSampleFinished {
		return fmt.Errorf("sample finish watermark is stale")
	}
	if sample.Observed.PhysicalKVUpper < 0 || sample.Observed.ActiveKVUpper < 0 {
		return fmt.Errorf("sample state must be non-negative")
	}

	m.base = domain.VirtualStateInterval{
		Lower: sample.Observed,
		Upper: sample.Observed,
	}
	for id, item := range m.reservations {
		switch {
		case item.AdmittedSequence <= sample.StartedSequence:
			item.Assimilation = assimilationAbsorbed
		case item.AdmittedSequence > sample.FinishedSequence:
			item.Assimilation = assimilationUnabsorbed
		default:
			item.Assimilation = assimilationAmbiguous
		}
		m.reservations[id] = item
	}
	for id, item := range m.completed {
		switch {
		case item.CompletedSequence <= sample.StartedSequence:
			delete(m.completed, id)
		case item.Reservation.AdmittedSequence > sample.FinishedSequence:
			// The request was entirely newer than this sample window.
		case item.Reservation.AdmittedSequence <= sample.StartedSequence && item.CompletedSequence > sample.FinishedSequence:
			m.base.Lower = subtractState(m.base.Lower, item.Reservation.Cost.KV)
			m.base.Upper = subtractState(m.base.Upper, item.Reservation.Cost.KV)
		default:
			m.base.Lower = subtractState(m.base.Lower, item.Reservation.Cost.KV)
		}
	}
	m.lastSampleFinished = sample.FinishedSequence
	m.hasSample = true
	return nil
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := Snapshot{
		Reservations:  len(m.reservations),
		EventSequence: m.eventSequence,
		Virtual:       m.virtualStateIntervalLocked(),
	}
	for _, item := range m.reservations {
		result.ReservedPhysicalKV += item.Cost.KV.PhysicalKVUpper
		result.ReservedActiveKV += item.Cost.KV.ActiveKVUpper
	}
	return result
}

func (m *Manager) virtualStateIntervalLocked() domain.VirtualStateInterval {
	state := m.base
	for _, item := range m.reservations {
		switch item.Assimilation {
		case assimilationUnabsorbed:
			state.Lower = addState(state.Lower, item.Cost.KV)
			state.Upper = addState(state.Upper, item.Cost.KV)
		case assimilationAmbiguous:
			state.Upper = addState(state.Upper, item.Cost.KV)
		}
	}
	return state
}

func addState(state domain.VirtualState, increment domain.KVIncrement) domain.VirtualState {
	state.PhysicalKVUpper += increment.PhysicalKVUpper
	state.ActiveKVUpper += increment.ActiveKVUpper
	return state
}

func subtractState(state domain.VirtualState, increment domain.KVIncrement) domain.VirtualState {
	state.PhysicalKVUpper = subtractFloorZero(state.PhysicalKVUpper, increment.PhysicalKVUpper)
	state.ActiveKVUpper = subtractFloorZero(state.ActiveKVUpper, increment.ActiveKVUpper)
	return state
}

func subtractFloorZero(value, decrement int64) int64 {
	if decrement >= value {
		return 0
	}
	return value - decrement
}
