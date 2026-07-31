package predictive

import (
	"sync"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type Scheduler interface {
	Predict(state domain.VirtualState, request domain.RequestCost) domain.SchedulerEstimate
}

type reservation struct {
	ID      string
	Created time.Time
	Cost    domain.RequestCost
}

type Manager struct {
	mu           sync.Mutex
	base         domain.VirtualState
	constraints  domain.Constraints
	scheduler    Scheduler
	reservations map[string]reservation
}

type Snapshot struct {
	Reservations      int
	ReservedPhysicalKV int64
	ReservedActiveKV   int64
}

func NewManager(base domain.VirtualState, constraints domain.Constraints, scheduler Scheduler) *Manager {
	return &Manager{
		base:         base,
		constraints:  constraints,
		scheduler:    scheduler,
		reservations: make(map[string]reservation),
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
	state := m.virtualStateLocked()
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
		Confidence: cost.Confidence,
	})
	if decision.Reason == domain.ReasonFit {
		m.reservations[requestID] = reservation{
			ID:      requestID,
			Created: now,
			Cost:    cost,
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
	if _, exists := m.reservations[requestID]; !exists {
		return false
	}
	delete(m.reservations, requestID)
	return true
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := Snapshot{Reservations: len(m.reservations)}
	for _, item := range m.reservations {
		result.ReservedPhysicalKV += item.Cost.KV.PhysicalKVUpper
		result.ReservedActiveKV += item.Cost.KV.ActiveKVUpper
	}
	return result
}

func (m *Manager) virtualStateLocked() domain.VirtualState {
	state := m.base
	for _, item := range m.reservations {
		state.PhysicalKVUpper += item.Cost.KV.PhysicalKVUpper
		state.ActiveKVUpper += item.Cost.KV.ActiveKVUpper
	}
	return state
}
