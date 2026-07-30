package kvshadow

import (
	"sort"
	"sync"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
)

type reservation struct {
	ID            string
	Backend       string
	Created       time.Time
	Expires       time.Time
	EstimatedLow  int64
	EstimatedHigh int64
	RemainingHigh int64
	Epoch         uint64
}

type backendState struct {
	Snapshot       kvadmission.BackendSnapshot
	Epoch          uint64
	CooldownUntil  time.Time
	ResetTotal     uint64
	AbsorbedTokens uint64
}

type Manager struct {
	mu               sync.Mutex
	policy           kvadmission.Policy
	backends         map[string]*backendState
	reservations     map[string]*reservation
	decisions        map[kvadmission.Reason]uint64
	lastDecision     kvadmission.Decision
	expiredTotal     uint64
	releasedTotal    uint64
	duplicateIDTotal uint64
}

type BackendStats struct {
	Name             string
	Kind             kvadmission.BackendKind
	Epoch            uint64
	CapacityTokens   int64
	ObservedTokens   int64
	UnabsorbedTokens int64
	Reservations     int
	CooldownUntil    time.Time
	ResetTotal       uint64
	AbsorbedTokens   uint64
	Updated          time.Time
}

type Snapshot struct {
	Decisions        map[kvadmission.Reason]uint64
	LastDecision     kvadmission.Decision
	Reservations     int
	UnabsorbedTokens int64
	ExpiredTotal     uint64
	ReleasedTotal    uint64
	DuplicateIDTotal uint64
	Backends         []BackendStats
}

func New(policy kvadmission.Policy) *Manager {
	return &Manager{
		policy:       policy,
		backends:     make(map[string]*backendState),
		reservations: make(map[string]*reservation),
		decisions:    make(map[kvadmission.Reason]uint64),
	}
}

func (m *Manager) DecideAndReserve(now time.Time, requestID string, cost kvadmission.Cost, snapshots []kvadmission.BackendSnapshot) (kvadmission.Decision, bool) {
	if m == nil {
		return kvadmission.Decision{Reason: kvadmission.ReasonCapacityUnknown}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(now)
	m.observeLocked(now, snapshots)

	reserved := m.reservedByBackendLocked()
	enriched := m.enrichedSnapshotsLocked(now, snapshots)
	decision := kvadmission.Evaluate(now, cost, enriched, reserved, m.policy)
	m.decisions[decision.Reason]++
	m.lastDecision = decision
	if decision.Reason != kvadmission.ReasonFit || requestID == "" {
		return decision, false
	}
	if _, exists := m.reservations[requestID]; exists {
		m.duplicateIDTotal++
		return decision, false
	}
	state := m.backends[decision.Backend]
	epoch := uint64(0)
	if state != nil {
		epoch = state.Epoch
	}
	ttl := m.policy.ReservationTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	m.reservations[requestID] = &reservation{
		ID:            requestID,
		Backend:       decision.Backend,
		Created:       now,
		Expires:       now.Add(ttl),
		EstimatedLow:  cost.EstimatedInputLow,
		EstimatedHigh: cost.ProjectedHigh(),
		RemainingHigh: cost.ProjectedHigh(),
		Epoch:         epoch,
	}
	return decision, true
}

func (m *Manager) Observe(now time.Time, snapshots []kvadmission.BackendSnapshot) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(now)
	m.observeLocked(now, snapshots)
}

func (m *Manager) Release(requestID string) bool {
	if m == nil || requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.reservations[requestID]; !ok {
		return false
	}
	delete(m.reservations, requestID)
	m.releasedTotal++
	return true
}

func (m *Manager) Sweep(now time.Time) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked(now)
}

func (m *Manager) Snapshot() Snapshot {
	if m == nil {
		return Snapshot{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	result := Snapshot{
		Decisions:        make(map[kvadmission.Reason]uint64, len(m.decisions)),
		LastDecision:     m.lastDecision,
		Reservations:     len(m.reservations),
		ExpiredTotal:     m.expiredTotal,
		ReleasedTotal:    m.releasedTotal,
		DuplicateIDTotal: m.duplicateIDTotal,
	}
	for reason, count := range m.decisions {
		result.Decisions[reason] = count
	}
	reserved := m.reservedByBackendLocked()
	counts := make(map[string]int)
	for _, item := range m.reservations {
		counts[item.Backend]++
		result.UnabsorbedTokens += item.RemainingHigh
	}
	result.Backends = make([]BackendStats, 0, len(m.backends))
	for name, state := range m.backends {
		result.Backends = append(result.Backends, BackendStats{
			Name:             name,
			Kind:             state.Snapshot.Kind,
			Epoch:            state.Epoch,
			CapacityTokens:   state.Snapshot.CapacityTokens,
			ObservedTokens:   state.Snapshot.UsedTokens,
			UnabsorbedTokens: reserved[name],
			Reservations:     counts[name],
			CooldownUntil:    state.CooldownUntil,
			ResetTotal:       state.ResetTotal,
			AbsorbedTokens:   state.AbsorbedTokens,
			Updated:          state.Snapshot.Updated,
		})
	}
	sort.Slice(result.Backends, func(i, j int) bool { return result.Backends[i].Name < result.Backends[j].Name })
	return result
}

func (m *Manager) observeLocked(now time.Time, snapshots []kvadmission.BackendSnapshot) {
	for _, snapshot := range snapshots {
		if snapshot.Name == "" {
			continue
		}
		state := m.backends[snapshot.Name]
		if state == nil {
			state = &backendState{Epoch: 1}
			m.backends[snapshot.Name] = state
		}
		if !state.Snapshot.Updated.IsZero() && !snapshot.Updated.After(state.Snapshot.Updated) {
			continue
		}

		reset := false
		if state.Snapshot.CapacityTokens > 0 && snapshot.CapacityTokens > 0 && state.Snapshot.CapacityTokens != snapshot.CapacityTokens {
			reset = true
		}
		if !state.Snapshot.Updated.IsZero() && snapshot.GenerationTokens < state.Snapshot.GenerationTokens {
			reset = true
		}
		if reset {
			m.clearBackendReservationsLocked(snapshot.Name)
			state.Epoch++
			state.ResetTotal++
		} else if !state.Snapshot.Updated.IsZero() && snapshot.UsedTokens > state.Snapshot.UsedTokens {
			delta := snapshot.UsedTokens - state.Snapshot.UsedTokens
			absorbed := m.absorbLocked(snapshot.Name, state.Epoch, delta)
			state.AbsorbedTokens += uint64(absorbed)
		}
		if snapshot.PreemptionDeltaValid && snapshot.PreemptionDelta > 0 {
			until := now.Add(m.policy.PreemptionCooldown)
			if until.After(state.CooldownUntil) {
				state.CooldownUntil = until
			}
		}
		state.Snapshot = snapshot
	}
}

func (m *Manager) enrichedSnapshotsLocked(now time.Time, snapshots []kvadmission.BackendSnapshot) []kvadmission.BackendSnapshot {
	result := make([]kvadmission.BackendSnapshot, 0, len(snapshots))
	for _, snapshot := range snapshots {
		if state := m.backends[snapshot.Name]; state != nil {
			snapshot.PreemptionCooldown = now.Before(state.CooldownUntil)
		}
		result = append(result, snapshot)
	}
	return result
}

func (m *Manager) reservedByBackendLocked() map[string]int64 {
	result := make(map[string]int64, len(m.backends))
	for _, item := range m.reservations {
		result[item.Backend] += item.RemainingHigh
	}
	return result
}

func (m *Manager) absorbLocked(backend string, epoch uint64, tokens int64) int64 {
	if tokens <= 0 {
		return 0
	}
	items := make([]*reservation, 0)
	for _, item := range m.reservations {
		if item.Backend == backend && item.Epoch == epoch && item.RemainingHigh > 0 {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Created.Equal(items[j].Created) {
			return items[i].ID < items[j].ID
		}
		return items[i].Created.Before(items[j].Created)
	})
	remaining := tokens
	for _, item := range items {
		if remaining <= 0 {
			break
		}
		value := item.RemainingHigh
		if value > remaining {
			value = remaining
		}
		item.RemainingHigh -= value
		remaining -= value
	}
	return tokens - remaining
}

func (m *Manager) clearBackendReservationsLocked(backend string) {
	for id, item := range m.reservations {
		if item.Backend == backend {
			delete(m.reservations, id)
		}
	}
}

func (m *Manager) sweepLocked(now time.Time) {
	for id, item := range m.reservations {
		if !now.Before(item.Expires) {
			delete(m.reservations, id)
			m.expiredTotal++
		}
	}
}
