package admission

import (
	"fmt"
	"math"
	"sync"
	"time"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type SampleWindow struct {
	controller    *AdmissionController
	runtimeEpoch  uint64
	id            uint64
	eventSequence uint64
}

type ReservationHandle struct {
	controller   *AdmissionController
	runtimeEpoch uint64
	id           uint64
}

func (h ReservationHandle) usable() bool {
	return h.controller != nil && h.runtimeEpoch != 0 && h.id != 0
}

func (h ReservationHandle) MarkForwarded() bool {
	return h.usable() && h.controller.markForwarded(h.runtimeEpoch, h.id)
}

func (h ReservationHandle) MarkFirstByte() bool {
	return h.usable() && h.controller.markFirstByte(h.runtimeEpoch, h.id)
}

func (h ReservationHandle) Terminate(cause TerminalCause) bool {
	return h.usable() && cause.valid() && h.controller.terminate(h.runtimeEpoch, h.id, cause)
}

type AdmissionController struct {
	mu sync.Mutex

	capability Capability
	policy     admissionPolicy
	projector  stateProjector
	tpsWindow  tpsWindow

	runtimeEpoch        uint64
	eventSequence       uint64
	sampleSequence      uint64
	observationSequence uint64
	lastPublishedSample uint64
	observation         observedState
	hasObservation      bool
	closedReason        Reason

	reservations        map[uint64]reservation
	overlay             reservationOverlay
	nextReservationID   uint64
	maximumReservations int64
}

func NewAdmissionController(config ControllerConfig) (*AdmissionController, error) {
	capability := config.Capability
	if err := capability.Validate(); err != nil {
		return nil, err
	}
	if !finiteNonnegative(config.TPS.Reference) || config.TPS.Reference > 1_000_000 {
		return nil, fmt.Errorf("TPS reference must be finite and in [0, 1000000]")
	}
	policy, err := newAdmissionPolicy(capability)
	if err != nil {
		return nil, err
	}
	return &AdmissionController{
		capability:          capability,
		policy:              policy,
		tpsWindow:           newTPSWindow(config.TPS.Reference),
		runtimeEpoch:        1,
		reservations:        make(map[uint64]reservation),
		maximumReservations: capability.KVHardLimitTokens / capability.KVBlockSize,
	}, nil
}

func (c *AdmissionController) StartSampleWindow() (SampleWindow, bool) {
	if c == nil {
		return SampleWindow{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closedReason != "" {
		return SampleWindow{}, false
	}
	if c.sampleSequence == math.MaxUint64 {
		c.failClosedLocked(ReasonCounterOverflow)
		return SampleWindow{}, false
	}
	c.sampleSequence++
	return SampleWindow{
		controller:    c,
		runtimeEpoch:  c.runtimeEpoch,
		id:            c.sampleSequence,
		eventSequence: c.eventSequence,
	}, true
}

func (c *AdmissionController) PublishObservation(window SampleWindow, observation BackendObservation) PublicationResult {
	if c == nil {
		return PublicationResult{Reason: ReasonControllerUnavailable}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closedReason != "" {
		return PublicationResult{Reason: c.closedReason, RuntimeEpoch: c.runtimeEpoch}
	}
	if window.controller != c || window.runtimeEpoch != c.runtimeEpoch ||
		window.id == 0 || window.id > c.sampleSequence || window.id <= c.lastPublishedSample ||
		!validBackendObservation(observation) {
		return PublicationResult{Reason: ReasonObservationInvalid, RuntimeEpoch: c.runtimeEpoch}
	}
	if !c.capability.matchesObservation(observation) {
		c.failClosedLocked(ReasonCapabilityDrift)
		return PublicationResult{
			CapabilityDrift: true,
			Reason:          ReasonCapabilityDrift,
			RuntimeEpoch:    c.runtimeEpoch,
		}
	}
	if c.hasObservation && observation.ObservedAt.Before(c.observation.observation.ObservedAt) {
		return PublicationResult{Reason: ReasonObservationInvalid, RuntimeEpoch: c.runtimeEpoch}
	}
	if c.hasObservation && c.observation.observation.RuntimeStartTime > 0 && observation.RuntimeStartTime == 0 {
		return PublicationResult{Reason: ReasonObservationInvalid, RuntimeEpoch: c.runtimeEpoch}
	}
	if c.observationSequence == math.MaxUint64 {
		c.failClosedLocked(ReasonCounterOverflow)
		return PublicationResult{Reason: ReasonCounterOverflow, RuntimeEpoch: c.runtimeEpoch}
	}

	runtimeReset := c.hasObservation &&
		((observation.RuntimeStartTime > 0 && c.observation.observation.RuntimeStartTime > 0 &&
			observation.RuntimeStartTime != c.observation.observation.RuntimeStartTime) ||
			observation.GenerationTokensTotal < c.observation.observation.GenerationTokensTotal ||
			observation.PreemptionsTotal < c.observation.observation.PreemptionsTotal)
	var generationDelta, preemptionDelta uint64
	var observationInterval time.Duration
	var previousRunning int64
	var cache cachePrefillObservation
	if runtimeReset {
		if c.runtimeEpoch == math.MaxUint64 {
			c.failClosedLocked(ReasonCounterOverflow)
			return PublicationResult{Reason: ReasonCounterOverflow, RuntimeEpoch: c.runtimeEpoch}
		}
		c.runtimeEpoch++
		clear(c.reservations)
		c.overlay = reservationOverlay{}
		c.sampleSequence = 0
		c.lastPublishedSample = 0
		c.tpsWindow.reset()
	} else {
		if c.hasObservation {
			generationDelta = observation.GenerationTokensTotal - c.observation.observation.GenerationTokensTotal
			preemptionDelta = observation.PreemptionsTotal - c.observation.observation.PreemptionsTotal
			observationInterval = observation.ObservedAt.Sub(c.observation.observation.ObservedAt)
			previousRunning = c.observation.observation.Running
			cache = nextCachePrefillObservation(c.observation, observation)
		}
		nextOverlay, ok := c.reconciledOverlayLocked(window.eventSequence)
		if !ok {
			c.failClosedLocked(ReasonControllerUnavailable)
			return PublicationResult{Reason: ReasonControllerUnavailable, RuntimeEpoch: c.runtimeEpoch}
		}
		c.applyReconciliationLocked(window.eventSequence)
		c.overlay = nextOverlay
		c.lastPublishedSample = window.id
		if c.hasObservation && c.tpsWindow.enabled() && !c.tpsWindow.observe(tpsSample{
			start:                     c.observation.observation.ObservedAt,
			end:                       observation.ObservedAt,
			maximumInterval:           observation.MaximumAge,
			generatedTokens:           generationDelta,
			previousRunning:           c.observation.observation.Running,
			running:                   observation.Running,
			previousLocalActiveDecode: c.observation.localActiveDecode,
			localActiveDecode:         c.overlay.localActiveDecode,
		}) {
			c.failClosedLocked(ReasonCounterOverflow)
			return PublicationResult{Reason: ReasonCounterOverflow, RuntimeEpoch: c.runtimeEpoch}
		}
	}

	c.observationSequence++
	c.observation = observedState{
		observation:       observation,
		sequence:          c.observationSequence,
		generationDelta:   generationDelta,
		preemptionDelta:   preemptionDelta,
		interval:          observationInterval,
		previousRunning:   previousRunning,
		localActiveDecode: c.overlay.localActiveDecode,
		cache:             cache,
	}
	c.hasObservation = true
	return PublicationResult{
		Accepted:            true,
		RuntimeReset:        runtimeReset,
		Reason:              ReasonOpen,
		ObservationSequence: c.observationSequence,
		RuntimeEpoch:        c.runtimeEpoch,
	}
}

func (c *AdmissionController) Admit(now time.Time, estimate predictive.RequestEstimate) AdmissionResult {
	if c == nil {
		return AdmissionResult{Decision: DecisionRecord{
			Action: ActionProtect, Reason: ReasonControllerUnavailable, Scope: ProtectionAvailability,
			Estimate: estimate,
		}}
	}
	work, err := predictive.BuildRequestWork(estimate, c.capability.KVBlockSize)
	if err != nil {
		c.mu.Lock()
		defer c.mu.Unlock()
		return AdmissionResult{Decision: DecisionRecord{
			Action: ActionProtect, Reason: ReasonInvalidRequest, Scope: ProtectionRequest,
			Estimate: estimate, HardKVLimitTokens: c.capability.KVHardLimitTokens,
			ObservationSequence: c.observationSequence,
			ControllerSequence:  c.eventSequence, RuntimeEpoch: c.runtimeEpoch,
		}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, reason, ok := c.stateLocked(now)
	if !ok {
		return AdmissionResult{Decision: c.unavailableDecisionLocked(reason, estimate, work, state)}
	}
	work = c.policy.withObservedPrefillCost(state, work)
	policy := c.policy.evaluate(state, work)
	decision := c.decisionLocked(policy, estimate, work, state)
	if policy.action != ActionAdmit {
		return AdmissionResult{Decision: decision}
	}
	if int64(len(c.reservations)) >= c.maximumReservations {
		c.failClosedLocked(ReasonResourceExhausted)
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonResourceExhausted, estimate, work, state)}
	}
	if c.nextReservationID == math.MaxUint64 {
		c.failClosedLocked(ReasonCounterOverflow)
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonCounterOverflow, estimate, work, state)}
	}
	reservationID := c.nextReservationID + 1
	item := reservation{
		id:           reservationID,
		runtimeEpoch: c.runtimeEpoch,
		work:         work,
		prefillClass: policy.prefillClass,
		phase:        reservationReserved,
	}
	contribution, valid := item.contribution()
	if !valid {
		c.failClosedLocked(ReasonControllerUnavailable)
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonControllerUnavailable, estimate, work, state)}
	}
	nextOverlay, valid := addOverlay(c.overlay, contribution)
	if !valid {
		c.failClosedLocked(ReasonResourceExhausted)
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonResourceExhausted, estimate, work, state)}
	}
	sequence, valid := c.nextEventSequenceLocked()
	if !valid {
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonCounterOverflow, estimate, work, state)}
	}
	item.admittedSequence = sequence
	c.nextReservationID = reservationID
	c.overlay = nextOverlay
	c.reservations[reservationID] = item
	decision.ControllerSequence = sequence
	decision.ReservationID = reservationID
	decision.RuntimeEpoch = c.runtimeEpoch
	return AdmissionResult{
		Decision: decision,
		Handle:   ReservationHandle{controller: c, runtimeEpoch: c.runtimeEpoch, id: reservationID},
	}
}

func (c *AdmissionController) Snapshot(now time.Time) CapacitySnapshot {
	if c == nil {
		return CapacitySnapshot{MinimumDecision: DecisionRecord{
			Action: ActionProtect, Reason: ReasonControllerUnavailable, Scope: ProtectionAvailability,
		}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, reason, ok := c.stateLocked(now)
	if !ok {
		decision := c.unavailableDecisionLocked(reason, c.policy.minimumWork.Estimate, c.policy.minimumWork, state)
		return CapacitySnapshot{
			IntakeOpen:          c.closedReason == "",
			HasObservation:      c.hasObservation,
			MinimumDecision:     decision,
			State:               state,
			Observation:         c.observation.observation,
			ObservationSequence: c.observationSequence,
			ControllerSequence:  c.eventSequence,
			RuntimeEpoch:        c.runtimeEpoch,
		}
	}
	minimumWork := c.policy.withObservedPrefillCost(state, c.policy.minimumWork)
	decision := c.decisionLocked(
		c.policy.evaluate(state, minimumWork),
		c.policy.minimumWork.Estimate,
		minimumWork,
		state,
	)
	return CapacitySnapshot{
		IntakeOpen:          true,
		HasObservation:      true,
		Available:           decision.Admitted(),
		MinimumDecision:     decision,
		State:               state,
		Observation:         c.observation.observation,
		ObservationSequence: c.observationSequence,
		ControllerSequence:  c.eventSequence,
		RuntimeEpoch:        c.runtimeEpoch,
	}
}

func (c *AdmissionController) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failClosedLocked(ReasonClosed)
}

func (c *AdmissionController) markForwarded(epoch, id uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closedReason != "" || epoch != c.runtimeEpoch {
		return false
	}
	item, ok := c.reservations[id]
	if !ok || item.runtimeEpoch != epoch || item.phase != reservationReserved {
		return false
	}
	sequence, ok := c.nextEventSequenceLocked()
	if !ok {
		return false
	}
	item.phase = reservationForwardedPrefill
	item.forwardedSequence = sequence
	c.reservations[id] = item
	return true
}

func (c *AdmissionController) markFirstByte(epoch, id uint64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closedReason != "" || epoch != c.runtimeEpoch {
		return false
	}
	item, ok := c.reservations[id]
	if !ok || item.runtimeEpoch != epoch || item.phase != reservationForwardedPrefill {
		return false
	}
	oldContribution, oldValid := item.contribution()
	next := item
	next.phase = reservationActiveDecode
	newContribution, newValid := next.contribution()
	nextOverlay, overlayValid := replaceOverlay(c.overlay, oldContribution, newContribution)
	if !oldValid || !newValid || !overlayValid {
		c.failClosedLocked(ReasonControllerUnavailable)
		return false
	}
	sequence, ok := c.nextEventSequenceLocked()
	if !ok {
		return false
	}
	next.firstByteSequence = sequence
	c.overlay = nextOverlay
	c.reservations[id] = next
	return true
}

func (c *AdmissionController) terminate(epoch, id uint64, cause TerminalCause) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closedReason != "" || epoch != c.runtimeEpoch || !cause.valid() {
		return false
	}
	item, ok := c.reservations[id]
	if !ok || item.runtimeEpoch != epoch || item.phase == reservationResidualDebt {
		return false
	}
	oldContribution, oldValid := item.contribution()
	if !oldValid {
		c.failClosedLocked(ReasonControllerUnavailable)
		return false
	}
	remove := item.phase == reservationReserved ||
		(item.phase == reservationActiveDecode && item.inputCovered)
	var next reservation
	var newContribution reservationOverlay
	newValid := true
	if !remove {
		next = item
		next.phase = reservationResidualDebt
		next.terminalCause = cause
		newContribution, newValid = next.contribution()
	}
	nextOverlay, overlayValid := replaceOverlay(c.overlay, oldContribution, newContribution)
	if !newValid || !overlayValid {
		c.failClosedLocked(ReasonControllerUnavailable)
		return false
	}
	sequence, ok := c.nextEventSequenceLocked()
	if !ok {
		return false
	}
	c.overlay = nextOverlay
	if remove {
		delete(c.reservations, id)
	} else {
		next.terminalSequence = sequence
		c.reservations[id] = next
	}
	return true
}

func (c *AdmissionController) stateLocked(now time.Time) (ProjectedState, Reason, bool) {
	if c.closedReason != "" {
		return ProjectedState{}, c.closedReason, false
	}
	if !c.hasObservation {
		return ProjectedState{}, ReasonObservationMissing, false
	}
	observation := c.observation.observation
	state, ok := c.projector.project(c.observation, c.overlay)
	if !ok {
		return ProjectedState{}, ReasonControllerUnavailable, false
	}
	if !now.IsZero() && !now.Before(observation.ObservedAt) {
		state.TPS = c.tpsWindow.snapshot(now)
	}
	if now.IsZero() || now.Before(observation.ObservedAt) || now.Sub(observation.ObservedAt) > observation.MaximumAge {
		return state, ReasonObservationStale, false
	}
	return state, ReasonOpen, true
}

func (c *AdmissionController) unavailableDecisionLocked(reason Reason, estimate predictive.RequestEstimate, work predictive.RequestWork, state ProjectedState) DecisionRecord {
	return DecisionRecord{
		Action: ActionProtect, Reason: reason, Scope: ProtectionAvailability,
		Estimate: estimate, Work: work, State: state,
		HardKVLimitTokens:   c.capability.KVHardLimitTokens,
		ObservationSequence: c.observationSequence,
		ControllerSequence:  c.eventSequence,
		RuntimeEpoch:        c.runtimeEpoch,
	}
}

func (c *AdmissionController) decisionLocked(policy policyDecision, estimate predictive.RequestEstimate, work predictive.RequestWork, state ProjectedState) DecisionRecord {
	remainingKV := int64(0)
	if policy.postAdmitKVTokens >= 0 && policy.postAdmitKVTokens <= c.capability.KVHardLimitTokens {
		remainingKV = c.capability.KVHardLimitTokens - policy.postAdmitKVTokens
	}
	return DecisionRecord{
		Action: policy.action, Reason: policy.reason, Scope: policy.scope,
		PrefillClass: policy.prefillClass, Estimate: estimate, Work: work, State: state,
		PostAdmitKVTokens:          policy.postAdmitKVTokens,
		HardKVLimitTokens:          c.capability.KVHardLimitTokens,
		RemainingKVTokens:          remainingKV,
		PendingPrefillTokensBefore: state.PendingPrefillTokens,
		PendingPrefillTokensAfter:  policy.pendingPrefillTokensAfter,
		TPSSequenceLimit:           policy.tpsSequenceLimit,
		TPSCurrentSequences:        policy.tpsCurrentSequences,
		TPSPostAdmitSequences:      policy.tpsPostAdmitSequences,
		ObservationSequence:        c.observationSequence,
		ControllerSequence:         c.eventSequence,
		RuntimeEpoch:               c.runtimeEpoch,
	}
}

func (c *AdmissionController) nextEventSequenceLocked() (uint64, bool) {
	if c.eventSequence == math.MaxUint64 {
		c.failClosedLocked(ReasonCounterOverflow)
		return 0, false
	}
	c.eventSequence++
	return c.eventSequence, true
}

func (c *AdmissionController) failClosedLocked(reason Reason) {
	if c.closedReason != "" {
		return
	}
	c.closedReason = reason
	if c.runtimeEpoch < math.MaxUint64 {
		c.runtimeEpoch++
	}
	clear(c.reservations)
	c.overlay = reservationOverlay{}
	c.tpsWindow.reset()
}

func (c *AdmissionController) reconciledOverlayLocked(watermark uint64) (reservationOverlay, bool) {
	nextOverlay := c.overlay
	for _, item := range c.reservations {
		oldContribution, oldValid := item.contribution()
		if !oldValid {
			return reservationOverlay{}, false
		}
		next, remove, changed := reconcileReservation(item, watermark)
		if remove {
			var ok bool
			nextOverlay, ok = subtractOverlay(nextOverlay, oldContribution)
			if !ok {
				return reservationOverlay{}, false
			}
			continue
		}
		if changed {
			newContribution, newValid := next.contribution()
			if !newValid {
				return reservationOverlay{}, false
			}
			var ok bool
			nextOverlay, ok = replaceOverlay(nextOverlay, oldContribution, newContribution)
			if !ok {
				return reservationOverlay{}, false
			}
		}
	}
	return nextOverlay, true
}

func (c *AdmissionController) applyReconciliationLocked(watermark uint64) {
	for id, item := range c.reservations {
		next, remove, changed := reconcileReservation(item, watermark)
		if remove {
			delete(c.reservations, id)
			continue
		}
		if changed {
			c.reservations[id] = next
		}
	}
}

func reconcileReservation(item reservation, watermark uint64) (reservation, bool, bool) {
	if item.phase == reservationResidualDebt && item.terminalSequence > 0 &&
		item.terminalSequence <= watermark {
		return reservation{}, true, true
	}
	next := item
	if item.phase == reservationForwardedPrefill && !item.sequenceCovered &&
		item.forwardedSequence > 0 && item.forwardedSequence <= watermark {
		next.sequenceCovered = true
	}
	if item.phase == reservationActiveDecode && item.firstByteSequence > 0 &&
		item.firstByteSequence <= watermark {
		next.sequenceCovered = true
		next.inputCovered = true
	}
	return next, false, next != item
}

func (c *AdmissionController) slowOverlayLocked() (reservationOverlay, bool) {
	var total reservationOverlay
	for _, item := range c.reservations {
		contribution, ok := item.contribution()
		if !ok {
			return reservationOverlay{}, false
		}
		total, ok = addOverlay(total, contribution)
		if !ok {
			return reservationOverlay{}, false
		}
	}
	return total, true
}

func validBackendObservation(observation BackendObservation) bool {
	if observation.CapabilityFingerprint == "" || observation.MaxModelLenTokens <= 0 ||
		observation.KVCapacityTokens <= 0 || observation.KVBlockSize <= 0 ||
		observation.ObservedAt.IsZero() || observation.MaximumAge <= 0 ||
		observation.UsedKVTokens < 0 || observation.UsedKVTokens > observation.KVCapacityTokens ||
		observation.Running < 0 || observation.Waiting < 0 {
		return false
	}
	if observation.RuntimeStartTime < 0 || math.IsNaN(observation.RuntimeStartTime) ||
		math.IsInf(observation.RuntimeStartTime, 0) {
		return false
	}
	_, ok := addNonnegativeInt64(observation.Running, observation.Waiting)
	return ok
}
