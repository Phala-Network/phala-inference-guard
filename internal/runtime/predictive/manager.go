package predictive

import (
	"fmt"
	"sync"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type Scheduler interface {
	Identity() ModelIdentity
	Predict(now time.Time, state domain.VirtualState, request domain.RequestCost) SchedulerPrediction
}

type SchedulerObserver interface {
	Observe(prediction SchedulerPrediction, outcome SchedulerOutcome) error
}

type assimilationState uint8

const (
	assimilationUnabsorbed assimilationState = iota
	assimilationAmbiguous
	assimilationAbsorbed
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
	case TerminalCompleted, TerminalLocalQoSReject, TerminalClientCancelled, TerminalClientDisconnected, TerminalUpstreamFailure, TerminalTimeout, TerminalExpired:
		return nil
	default:
		return fmt.Errorf("predictive terminal cause %q is invalid", c)
	}
}

func (c TerminalCause) allowsCompletedOutcome() bool {
	return c == TerminalCompleted
}

type reservation struct {
	ID                       string
	Created                  time.Time
	Cost                     domain.RequestCost
	Prediction               SchedulerPrediction
	OutcomeObserved          bool
	PrefillComplete          bool
	TerminalCause            TerminalCause
	AdmittedSequence         uint64
	PrefillCompletedSequence uint64
	Assimilation             assimilationState
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
	manifestID         string
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

type managerAdmissionResult struct {
	Decision      domain.Decision
	Prediction    SchedulerPrediction
	HasPrediction bool
}

func NewManager(manifestID string, base domain.VirtualState, constraints domain.Constraints, scheduler Scheduler) *Manager {
	return &Manager{
		manifestID: manifestID,
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
	return m.decideAndReserve(now, requestID, cost).Decision
}

func (m *Manager) decideAndReserve(now time.Time, requestID string, cost domain.RequestCost) managerAdmissionResult {
	if m == nil {
		return managerAdmissionResult{Decision: domain.Decision{Reason: domain.ReasonPredictorProfileUnknown}}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.manifestID == "" || cost.ManifestID == "" || cost.ManifestID != m.manifestID {
		return managerAdmissionResult{Decision: domain.Decision{Reason: domain.ReasonTokenizerProfileUnknown}}
	}
	if requestID == "" || !validRequestCost(cost) {
		return managerAdmissionResult{Decision: domain.Decision{Reason: domain.ReasonPredictorProfileUnknown}}
	}
	if _, exists := m.reservations[requestID]; exists {
		return managerAdmissionResult{Decision: domain.Decision{Reason: domain.ReasonDuplicateRequest}}
	}
	if m.scheduler == nil {
		return managerAdmissionResult{Decision: domain.Decision{Reason: domain.ReasonPredictorProfileUnknown}}
	}
	schedulerIdentity := m.scheduler.Identity()
	if schedulerIdentity.Validate() != nil {
		return managerAdmissionResult{Decision: domain.Decision{Reason: domain.ReasonPredictorProfileUnknown}}
	}
	state := m.virtualStateIntervalLocked().Upper
	prediction := m.scheduler.Predict(now, state, cost)
	if prediction.Identity != schedulerIdentity || !validSchedulerPrediction(prediction) {
		return managerAdmissionResult{Decision: domain.Decision{Reason: domain.ReasonPredictorProfileUnknown}}
	}
	projection := domain.Projection{
		PhysicalKVUpper: addInt64Saturating(state.PhysicalKVUpper, cost.KV.PhysicalKVUpper),
		ActiveKVUpper:   addInt64Saturating(state.ActiveKVUpper, cost.KV.ActiveKVUpper),
	}
	decision := domain.Evaluate(domain.EvaluationInput{
		Projection:  projection,
		Scheduler:   prediction.Estimate,
		Constraints: m.constraints,
		Confidence:  minimumConfidence(cost.Confidence, prediction.Confidence),
	})
	if decision.Reason == domain.ReasonFit {
		m.eventSequence++
		m.reservations[requestID] = reservation{
			ID:               requestID,
			Created:          now,
			Cost:             cost,
			Prediction:       prediction,
			AdmittedSequence: m.eventSequence,
			Assimilation:     assimilationUnabsorbed,
		}
	}
	return managerAdmissionResult{
		Decision:      decision,
		Prediction:    prediction,
		HasPrediction: true,
	}
}

func (m *Manager) ReservationPrediction(requestID string) (SchedulerPrediction, bool) {
	if m == nil || requestID == "" {
		return SchedulerPrediction{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	if !exists {
		return SchedulerPrediction{}, false
	}
	return item.Prediction, true
}

func (m *Manager) HasReservation(requestID string) bool {
	if m == nil || requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	_, exists := m.reservations[requestID]
	return exists
}

func (m *Manager) CanMarkPrefillComplete(requestID string) bool {
	if m == nil || requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	return exists && !item.PrefillComplete
}

func (m *Manager) MarkPrefillComplete(requestID string) bool {
	if m == nil || requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	if !exists || item.PrefillComplete {
		return false
	}
	m.eventSequence++
	item.PrefillComplete = true
	item.PrefillCompletedSequence = m.eventSequence
	if item.Assimilation == assimilationAbsorbed {
		prefill := prefillStateCost(item.Cost)
		m.base.Lower = subtractState(m.base.Lower, prefill)
		m.base.Upper = subtractState(m.base.Upper, prefill)
	}
	m.reservations[requestID] = item
	return true
}

func (m *Manager) ObserveOutcome(requestID string, outcome SchedulerOutcome) bool {
	if m == nil || requestID == "" {
		return false
	}
	observer, ok := m.scheduler.(SchedulerObserver)
	if !ok {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if item, exists := m.reservations[requestID]; exists {
		if item.OutcomeObserved || observer.Observe(item.Prediction, outcome) != nil {
			return false
		}
		item.OutcomeObserved = true
		m.reservations[requestID] = item
		return true
	}
	if item, exists := m.completed[requestID]; exists {
		if !item.Reservation.TerminalCause.allowsCompletedOutcome() || item.Reservation.OutcomeObserved || observer.Observe(item.Reservation.Prediction, outcome) != nil {
			return false
		}
		item.Reservation.OutcomeObserved = true
		m.completed[requestID] = item
		return true
	}
	return false
}

func (m *Manager) Complete(requestID string) bool {
	return m.Terminate(requestID, TerminalCompleted)
}

func (m *Manager) Terminate(requestID string, cause TerminalCause) bool {
	if m == nil {
		return false
	}
	if err := cause.Validate(); err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	if !exists {
		return false
	}
	m.eventSequence++
	item.TerminalCause = cause
	activeCost := reservationStateCost(item)
	switch item.Assimilation {
	case assimilationAbsorbed:
		m.base.Lower = subtractState(m.base.Lower, activeCost)
		m.base.Upper = subtractState(m.base.Upper, activeCost)
	case assimilationAmbiguous:
		m.base.Lower = subtractState(m.base.Lower, activeCost)
	}
	delete(m.reservations, requestID)
	m.completed[requestID] = completedReservation{
		Reservation:       item,
		CompletedSequence: m.eventSequence,
	}
	return true
}

func (m *Manager) rollbackLatestReservation(requestID string) bool {
	if m == nil || requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	if !exists || item.AdmittedSequence != m.eventSequence || item.Assimilation != assimilationUnabsorbed || item.PrefillComplete || item.OutcomeObserved {
		return false
	}
	delete(m.reservations, requestID)
	m.eventSequence--
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

func (m *Manager) StartSampleWindow() (uint64, domain.VirtualState) {
	if m == nil {
		return 0, domain.VirtualState{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.eventSequence, m.virtualStateIntervalLocked().Upper
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
	if sample.Observed.PhysicalKVUpper < 0 || sample.Observed.ActiveKVUpper < 0 || sample.Observed.DecodeSequences < 0 || sample.Observed.ActiveContextTokens < 0 || sample.Observed.UncachedPrefillTokens < 0 {
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
		if item.Assimilation == assimilationAbsorbed && item.PrefillComplete {
			prefill := prefillStateCost(item.Cost)
			switch {
			case item.PrefillCompletedSequence > sample.FinishedSequence:
				m.base.Lower = subtractState(m.base.Lower, prefill)
				m.base.Upper = subtractState(m.base.Upper, prefill)
			case item.PrefillCompletedSequence > sample.StartedSequence:
				m.base.Lower = subtractState(m.base.Lower, prefill)
			}
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
			activeCost := reservationStateCost(item.Reservation)
			m.base.Lower = subtractState(m.base.Lower, activeCost)
			m.base.Upper = subtractState(m.base.Upper, activeCost)
		default:
			m.base.Lower = subtractState(m.base.Lower, reservationStateCost(item.Reservation))
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
		cost := reservationStateCost(item)
		switch item.Assimilation {
		case assimilationUnabsorbed:
			state.Lower = addState(state.Lower, cost)
			state.Upper = addState(state.Upper, cost)
		case assimilationAmbiguous:
			state.Upper = addState(state.Upper, cost)
		}
	}
	return state
}

func reservationStateCost(item reservation) domain.RequestCost {
	cost := item.Cost
	if item.PrefillComplete && item.Assimilation != assimilationAmbiguous {
		cost.UncachedPrefillUpper = 0
	}
	return cost
}

func prefillStateCost(cost domain.RequestCost) domain.RequestCost {
	return domain.RequestCost{UncachedPrefillUpper: cost.UncachedPrefillUpper}
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

func validRequestCost(cost domain.RequestCost) bool {
	return cost.InputTokens >= 0 && cost.KV.PhysicalKVUpper >= 0 && cost.KV.ActiveKVUpper >= 0 && cost.KV.CacheDiscountTokens >= 0 && cost.KV.CacheDiscountTokens <= cost.InputTokens && cost.UncachedPrefillUpper >= 0 && cost.UncachedPrefillUpper <= cost.InputTokens && cost.CachedPrefillExpected >= 0 && cost.CachedPrefillExpected <= cost.InputTokens && cost.DecodeHorizonUpper >= 0 && cost.DecodeSequencesUpper >= 0 && cost.ActiveContextTokensUpper >= 0 && positiveFinite(cost.Confidence) && cost.Confidence <= 1
}

func validSchedulerPrediction(prediction SchedulerPrediction) bool {
	estimate := prediction.Estimate
	return nonNegativeFinite(estimate.ExistingUserTPSLower) && nonNegativeFinite(estimate.AllUserTPSLower) && estimate.TTFTUpper > 0 && estimate.TPOTUpper > 0 && nonNegativeFinite(estimate.WorkspaceRiskUpper) && nonNegativeFinite(estimate.PreemptionRiskUpper) && positiveFinite(prediction.Confidence) && prediction.Confidence <= 1
}

func minimumConfidence(left, right float64) float64 {
	if left <= 0 || right <= 0 {
		return 0
	}
	if left < right {
		return left
	}
	return right
}
