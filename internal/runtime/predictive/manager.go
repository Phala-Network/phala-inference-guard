package predictive

import (
	"fmt"
	"math"
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

func observeSchedulerOutcome(observer SchedulerObserver, prediction SchedulerPrediction, outcome SchedulerOutcome) (accepted bool) {
	defer func() {
		if recover() != nil {
			accepted = false
		}
	}()
	return observer.Observe(prediction, outcome) == nil
}

type SchedulerLearningInvalidator interface {
	InvalidateLearning()
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

func (c TerminalCause) allowsOutcome(forwarded bool, outcome SchedulerOutcome) bool {
	if outcome.Censored {
		return forwarded && c != TerminalLocalQoSReject
	}
	return forwarded && c.allowsCompletedOutcome()
}

type reservation struct {
	ID                       string
	Created                  time.Time
	Cost                     domain.RequestCost
	Prediction               SchedulerPrediction
	OutcomeObserved          bool
	Forwarded                bool
	PrefillComplete          bool
	TerminalCause            TerminalCause
	AdmittedSequence         uint64
	ForwardedSequence        uint64
	PrefillCompletedSequence uint64
	Assimilation             assimilationState
}

const maximumRetiredReservations = 4_096

type retiredReservation struct {
	CompletedSequence uint64
	MaterializedFloor domain.RequestCost
}

type retiredReservationQueue struct {
	items []retiredReservation
	head  int
	size  int
}

func (q *retiredReservationQueue) Push(item retiredReservation) bool {
	if q.items == nil {
		q.items = make([]retiredReservation, maximumRetiredReservations)
	}
	if q.size < len(q.items) {
		index := (q.head + q.size) % len(q.items)
		q.items[index] = item
		q.size++
		return false
	}
	q.items[q.head] = item
	q.head = (q.head + 1) % len(q.items)
	return true
}

func (q *retiredReservationQueue) Pop() (retiredReservation, bool) {
	if q.size == 0 {
		return retiredReservation{}, false
	}
	item := q.items[q.head]
	q.items[q.head] = retiredReservation{}
	q.head = (q.head + 1) % len(q.items)
	q.size--
	if q.size == 0 {
		q.head = 0
	}
	return item, true
}

func (q *retiredReservationQueue) Len() int {
	return q.size
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
	retired            retiredReservationQueue
	retiredEvictions   uint64
	eventSequence      uint64
	lastSampleFinished uint64
	hasSample          bool
}

type Snapshot struct {
	Reservations        int
	ReservedPhysicalKV  int64
	ReservedActiveKV    int64
	EventSequence       uint64
	RetiredReservations int
	RetiredEvictions    uint64
	Virtual             domain.VirtualStateInterval
}

type managerAdmissionResult struct {
	Decision   domain.Decision
	Prediction SchedulerPrediction
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
		Decision:   decision,
		Prediction: prediction,
	}
}

func (m *Manager) MarkForwarded(requestID string) bool {
	if m == nil || requestID == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	item, exists := m.reservations[requestID]
	if !exists || item.Forwarded {
		return false
	}
	m.eventSequence++
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
	item.PrefillComplete = true
	item.PrefillCompletedSequence = m.eventSequence
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
	item, exists := m.reservations[requestID]
	if !exists || !item.Forwarded || item.OutcomeObserved || !observeSchedulerOutcome(observer, item.Prediction, outcome) {
		return false
	}
	item.OutcomeObserved = true
	m.reservations[requestID] = item
	return true
}

func (m *Manager) Complete(requestID string) bool {
	return m.Terminate(requestID, TerminalCompleted)
}

func (m *Manager) Terminate(requestID string, cause TerminalCause) bool {
	return m.TerminateWithOutcome(requestID, cause, nil)
}

func (m *Manager) TerminateWithOutcome(requestID string, cause TerminalCause, outcome *SchedulerOutcome) bool {
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
	if outcome != nil && cause.allowsOutcome(item.Forwarded, *outcome) && !item.OutcomeObserved {
		if observer, ok := m.scheduler.(SchedulerObserver); ok && observeSchedulerOutcome(observer, item.Prediction, *outcome) {
			item.OutcomeObserved = true
		}
	}
	m.eventSequence++
	item.TerminalCause = cause
	if item.Assimilation == assimilationAbsorbed && item.PrefillComplete {
		materialized := materializedStateFloor(item.Cost)
		m.base.Lower = subtractState(m.base.Lower, materialized)
		m.base.Upper = subtractState(m.base.Upper, materialized)
		if m.retired.Push(retiredReservation{
			CompletedSequence: m.eventSequence,
			MaterializedFloor: materialized,
		}) {
			m.retiredEvictions++
		}
	}
	delete(m.reservations, requestID)
	return true
}

func (m *Manager) InvalidateLearning() bool {
	if m == nil {
		return false
	}
	invalidator, ok := m.scheduler.(SchedulerLearningInvalidator)
	if !ok {
		return false
	}
	invalidator.InvalidateLearning()
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

func (m *Manager) StartSampleWindow() uint64 {
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
	if sample.Observed.PhysicalKVUpper < 0 || sample.Observed.ActiveKVUpper < 0 || sample.Observed.DecodeSequences < 0 || sample.Observed.ActiveContextTokens < 0 || sample.Observed.UncachedPrefillTokens < 0 {
		return fmt.Errorf("sample state must be non-negative")
	}

	m.base = domain.VirtualStateInterval{
		Lower: sample.Observed,
		Upper: sample.Observed,
	}
	for id, item := range m.reservations {
		switch {
		case !item.Forwarded || !item.PrefillComplete:
			item.Assimilation = assimilationUnabsorbed
		case item.ForwardedSequence <= sample.StartedSequence && item.PrefillCompletedSequence <= sample.StartedSequence:
			item.Assimilation = assimilationAbsorbed
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
		}
		m.retired.Push(item)
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
		Reservations:        len(m.reservations),
		EventSequence:       m.eventSequence,
		RetiredReservations: m.retired.Len(),
		RetiredEvictions:    m.retiredEvictions,
		Virtual:             m.virtualStateIntervalLocked(),
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
			cost := fullReservationStateCost(item)
			state.Lower = addState(state.Lower, cost)
			state.Upper = addState(state.Upper, cost)
		case assimilationAmbiguous:
			state.Lower = addState(state.Lower, futureReservationStateCost(item))
			state.Upper = addState(state.Upper, fullReservationStateCost(item))
		case assimilationAbsorbed:
			cost := futureReservationStateCost(item)
			state.Lower = addState(state.Lower, cost)
			state.Upper = addState(state.Upper, cost)
		}
	}
	return state
}

func fullReservationStateCost(item reservation) domain.RequestCost {
	cost := item.Cost
	if item.PrefillComplete {
		cost.UncachedPrefillUpper = 0
	}
	return cost
}

func futureReservationStateCost(item reservation) domain.RequestCost {
	return domain.RequestCost{
		KV:                       item.Cost.FutureKV,
		UncachedPrefillUpper:     0,
		ActiveContextTokensUpper: item.Cost.FutureContextTokensUpper,
	}
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

func validRequestCost(cost domain.RequestCost) bool {
	if cost.InputTokens < 0 || cost.UncachedPrefillUpper != cost.InputTokens {
		return false
	}
	if cost.DecodeHorizonUpper < 0 || cost.DecodeSequencesUpper != 1 || cost.InputTokens > math.MaxInt64-cost.DecodeHorizonUpper {
		return false
	}
	if cost.ActiveContextTokensUpper != cost.InputTokens+cost.DecodeHorizonUpper || cost.FutureContextTokensUpper != cost.DecodeHorizonUpper {
		return false
	}
	if cost.KV.PhysicalKVUpper < 0 || cost.KV.PhysicalKVUpper != cost.KV.ActiveKVUpper || cost.KV.PhysicalKVUpper < cost.ActiveContextTokensUpper {
		return false
	}
	if cost.FutureKV.PhysicalKVUpper < 0 || cost.FutureKV.PhysicalKVUpper != cost.FutureKV.ActiveKVUpper || cost.FutureKV.PhysicalKVUpper > cost.KV.PhysicalKVUpper {
		return false
	}
	if cost.KV.PhysicalKVUpper-cost.FutureKV.PhysicalKVUpper < cost.InputTokens {
		return false
	}
	return positiveFinite(cost.Confidence) && cost.Confidence <= 1
}

func validSchedulerPrediction(prediction SchedulerPrediction) bool {
	estimate := prediction.Estimate
	return nonNegativeFinite(estimate.ExistingUserTPSLower) && nonNegativeFinite(estimate.NewUserTPSLower) && estimate.TTFTUpper > 0 && estimate.TPOTUpper > 0 && nonNegativeFinite(estimate.WorkspaceRiskUpper) && nonNegativeFinite(estimate.PreemptionRiskUpper) && positiveFinite(prediction.Confidence) && prediction.Confidence <= 1
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
