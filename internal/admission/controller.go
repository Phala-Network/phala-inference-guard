package admission

import (
	"fmt"
	"math"
	"sync"
	"time"
)

const (
	maximumTPSReservations               = int64(1 << 20)
	defaultPendingFirstByteLeaseDuration = 1500 * time.Millisecond
)

type SampleWindow struct {
	controller     *AdmissionController
	runtimeEpoch   uint64
	tpsPolicyEpoch uint64
	id             uint64
	eventSequence  uint64
	exposure       sequenceExposureSnapshot
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

	runtimeIdentity               string
	policy                        admissionPolicy
	projector                     stateProjector
	tpsWindow                     tpsWindow
	windowConcurrency             int64
	runningLimit                  int64
	runningLimitSource            RunningLimitSource
	pendingFirstByteLeaseDuration time.Duration
	now                           func() time.Time
	exposure                      sequenceExposureLedger

	runtimeEpoch        uint64
	eventSequence       uint64
	sampleSequence      uint64
	observationSequence uint64
	lastPublishedSample uint64
	lastExposure        sequenceExposureSnapshot
	policyRevision      uint64
	policyUpdatedAt     time.Time
	tpsPolicyEpoch      uint64
	observation         observedState
	hasObservation      bool
	closedReason        Reason
	reservations        map[uint64]reservation
	overlay             reservationOverlay
	nextReservationID   uint64
	maximumReservations int64
	windowHistogram     windowConcurrencyHistogram
}

func NewAdmissionController(config ControllerConfig) (*AdmissionController, error) {
	if config.RuntimeIdentity == "" {
		return nil, fmt.Errorf("admission runtime identity is invalid")
	}
	if !finiteNonnegative(config.TPS.Reference) || config.TPS.Reference > 1_000_000 {
		return nil, fmt.Errorf("TPS reference must be finite and in [0, 1000000]")
	}
	if config.WindowConcurrency == 0 {
		config.WindowConcurrency = DefaultWindowConcurrency
	}
	if config.RunningLimitSource == "" {
		config.RunningLimitSource = RunningLimitSourceUnknown
	}
	if config.PendingFirstByteLeaseDuration == 0 {
		config.PendingFirstByteLeaseDuration = defaultPendingFirstByteLeaseDuration
	}
	if config.WindowConcurrency <= 0 || config.WindowConcurrency > maximumTPSReservations ||
		config.RunningLimit < 0 || config.RunningLimit > maximumTPSReservations ||
		config.PendingFirstByteLeaseDuration < 0 ||
		!config.RunningLimitSource.valid() ||
		(config.RunningLimit > 0 && config.RunningLimitSource == RunningLimitSourceUnknown) {
		return nil, fmt.Errorf("admission running and window bounds are invalid")
	}
	policy := newAdmissionPolicy()
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &AdmissionController{
		runtimeIdentity:               config.RuntimeIdentity,
		policy:                        policy,
		tpsWindow:                     newTPSWindow(config.TPS.Reference),
		windowConcurrency:             config.WindowConcurrency,
		runningLimit:                  config.RunningLimit,
		runningLimitSource:            config.RunningLimitSource,
		pendingFirstByteLeaseDuration: config.PendingFirstByteLeaseDuration,
		now:                           now,
		runtimeEpoch:                  1,
		policyRevision:                1,
		tpsPolicyEpoch:                1,
		reservations:                  make(map[uint64]reservation),
		maximumReservations:           maximumTPSReservations,
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
	exposure, ok := c.exposure.snapshot(c.now())
	if !ok {
		c.failClosedLocked(ReasonControllerUnavailable)
		return SampleWindow{}, false
	}
	c.sampleSequence++
	return SampleWindow{
		controller:     c,
		runtimeEpoch:   c.runtimeEpoch,
		tpsPolicyEpoch: c.tpsPolicyEpoch,
		id:             c.sampleSequence,
		eventSequence:  c.eventSequence,
		exposure:       exposure,
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
	runtimeIdentityChanged := c.hasObservation &&
		observation.RuntimeStartTime > 0 && c.observation.observation.RuntimeStartTime > 0 &&
		observation.RuntimeStartTime != c.observation.observation.RuntimeStartTime
	if observation.RuntimeIdentity != c.runtimeIdentity {
		c.failClosedLocked(ReasonRuntimeIdentityDrift)
		return PublicationResult{
			RuntimeIdentityDrift: true,
			Reason:               ReasonRuntimeIdentityDrift,
			RuntimeEpoch:         c.runtimeEpoch,
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
		(runtimeIdentityChanged ||
			observation.GenerationTokensTotal < c.observation.observation.GenerationTokensTotal ||
			observation.PreemptionsTotal < c.observation.observation.PreemptionsTotal)
	if c.hasObservation && !runtimeReset {
		c.windowHistogram.observe(c.overlay.unobservedSequences)
	}
	var generationDelta, preemptionDelta uint64
	var observationInterval time.Duration
	var previousRunning int64
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
		c.lastExposure = sequenceExposureSnapshot{}
		c.exposure.reset()
		c.tpsWindow.reset()
	} else {
		if c.hasObservation {
			generationDelta = observation.GenerationTokensTotal - c.observation.observation.GenerationTokensTotal
			preemptionDelta = observation.PreemptionsTotal - c.observation.observation.PreemptionsTotal
			observationInterval = observation.ObservedAt.Sub(c.observation.observation.ObservedAt)
			previousRunning = c.observation.observation.Running
		}
		exposure, exposureOK := window.exposure.subtract(c.lastExposure)
		if !exposureOK {
			c.failClosedLocked(ReasonControllerUnavailable)
			return PublicationResult{Reason: ReasonControllerUnavailable, RuntimeEpoch: c.runtimeEpoch}
		}
		forwardedSequenceSeconds, responseSequenceSeconds, exposureSecondsOK := exposure.seconds()
		if !exposureSecondsOK {
			c.failClosedLocked(ReasonControllerUnavailable)
			return PublicationResult{Reason: ReasonControllerUnavailable, RuntimeEpoch: c.runtimeEpoch}
		}
		reconciliationAt := c.now()
		nextOverlay, forwardedSequenceLiabilities, ok := c.reconciledOverlayLocked(
			window.eventSequence,
			observation,
			reconciliationAt,
		)
		if !ok {
			c.failClosedLocked(ReasonControllerUnavailable)
			return PublicationResult{Reason: ReasonControllerUnavailable, RuntimeEpoch: c.runtimeEpoch}
		}
		c.applyReconciliationLocked(window.eventSequence, observation, reconciliationAt)
		c.overlay = nextOverlay
		c.lastPublishedSample = window.id
		c.lastExposure = window.exposure
		if c.hasObservation && window.tpsPolicyEpoch == c.tpsPolicyEpoch &&
			c.tpsWindow.enabled() && !c.tpsWindow.observe(tpsSample{
			start:                         c.observation.observation.ObservedAt,
			end:                           observation.ObservedAt,
			maximumInterval:               observation.MaximumAge,
			generatedTokens:               generationDelta,
			previousRunning:               c.observation.observation.Running,
			running:                       observation.Running,
			forwardedSequenceLiabilities:  forwardedSequenceLiabilities,
			localExposureMeasured:         true,
			localForwardedSequenceSeconds: forwardedSequenceSeconds,
			localResponseSequenceSeconds:  responseSequenceSeconds,
		}) {
			c.failClosedLocked(ReasonCounterOverflow)
			return PublicationResult{Reason: ReasonCounterOverflow, RuntimeEpoch: c.runtimeEpoch}
		}
	}

	c.observationSequence++
	c.observation = observedState{
		observation:     observation,
		sequence:        c.observationSequence,
		generationDelta: generationDelta,
		preemptionDelta: preemptionDelta,
		interval:        observationInterval,
		previousRunning: previousRunning,
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

func (c *AdmissionController) Admit(now time.Time, demand TPSRequestDemand) AdmissionResult {
	if c == nil {
		return AdmissionResult{Decision: DecisionRecord{
			Action: ActionProtect, Reason: ReasonControllerUnavailable, Scope: ProtectionAvailability,
			Demand: demand,
		}}
	}
	if !demand.valid() {
		c.mu.Lock()
		defer c.mu.Unlock()
		return AdmissionResult{Decision: DecisionRecord{
			Action: ActionProtect, Reason: ReasonInvalidRequest, Scope: ProtectionRequest,
			Demand:              demand,
			ObservationSequence: c.observationSequence,
			ControllerSequence:  c.eventSequence, RuntimeEpoch: c.runtimeEpoch,
			PolicyRevision: c.policyRevision,
		}}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	state, reason, ok := c.stateLocked(now)
	if !ok {
		return AdmissionResult{Decision: c.unavailableDecisionLocked(reason, demand, state)}
	}
	policy := c.policy.evaluateDemand(state, demand, admissionBounds{
		windowConcurrency: c.windowConcurrency,
		runningLimit:      c.runningLimit,
	})
	decision := c.decisionLocked(policy, demand, state)
	if policy.action != ActionAdmit {
		return AdmissionResult{Decision: decision}
	}
	if int64(len(c.reservations)) >= c.maximumReservations {
		c.failClosedLocked(ReasonResourceExhausted)
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonResourceExhausted, demand, state)}
	}
	if c.nextReservationID == math.MaxUint64 {
		c.failClosedLocked(ReasonCounterOverflow)
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonCounterOverflow, demand, state)}
	}
	reservationID := c.nextReservationID + 1
	item := reservation{
		id:           reservationID,
		runtimeEpoch: c.runtimeEpoch,
		demand:       demand,
		phase:        reservationReserved,
	}
	contribution, valid := item.contribution()
	if !valid {
		c.failClosedLocked(ReasonControllerUnavailable)
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonControllerUnavailable, demand, state)}
	}
	nextOverlay, valid := addOverlay(c.overlay, contribution)
	if !valid {
		c.failClosedLocked(ReasonResourceExhausted)
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonResourceExhausted, demand, state)}
	}
	sequence, valid := c.nextEventSequenceLocked()
	if !valid {
		return AdmissionResult{Decision: c.unavailableDecisionLocked(ReasonCounterOverflow, demand, state)}
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
		decision := c.unavailableDecisionLocked(reason, TPSRequestDemand{}, state)
		return CapacitySnapshot{
			IntakeOpen:                 c.closedReason == "",
			HasObservation:             c.hasObservation,
			MinimumDecision:            decision,
			State:                      state,
			Observation:                c.observation.observation,
			ObservationSequence:        c.observationSequence,
			ControllerSequence:         c.eventSequence,
			RuntimeEpoch:               c.runtimeEpoch,
			Policy:                     c.policySnapshotLocked(),
			WindowConcurrencyHistogram: c.windowHistogram.snapshot(),
		}
	}
	minimumDemand := TPSRequestDemand{DecodeSequences: 1, Source: TPSDemandSourceFallback}
	decision := c.decisionLocked(
		c.policy.evaluateDemand(state, minimumDemand, admissionBounds{
			windowConcurrency: c.windowConcurrency,
			runningLimit:      c.runningLimit,
		}),
		minimumDemand,
		state,
	)
	return CapacitySnapshot{
		IntakeOpen:                 true,
		HasObservation:             true,
		Available:                  decision.Admitted(),
		MinimumDecision:            decision,
		State:                      state,
		Observation:                c.observation.observation,
		ObservationSequence:        c.observationSequence,
		ControllerSequence:         c.eventSequence,
		RuntimeEpoch:               c.runtimeEpoch,
		Policy:                     c.policySnapshotLocked(),
		WindowConcurrencyHistogram: c.windowHistogram.snapshot(),
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
	forwardedAt := c.now()
	demand, valid := item.effectiveDemand()
	if forwardedAt.IsZero() || !valid || !c.exposure.addForwarded(forwardedAt, demand.DecodeSequences) {
		c.failClosedLocked(ReasonControllerUnavailable)
		return false
	}
	item.phase = reservationForwarded
	item.forwardedAt = forwardedAt
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
	if !ok || item.runtimeEpoch != epoch || item.phase != reservationForwarded {
		return false
	}
	oldContribution, oldValid := item.contribution()
	next := item
	next.phase = reservationActiveDecode
	next.pendingFirstByteReleased = true
	newContribution, newValid := next.contribution()
	nextOverlay, overlayValid := replaceOverlay(c.overlay, oldContribution, newContribution)
	if !oldValid || !newValid || !overlayValid {
		c.failClosedLocked(ReasonControllerUnavailable)
		return false
	}
	_, ok = c.nextEventSequenceLocked()
	if !ok {
		return false
	}
	if !c.exposure.addResponse(c.now(), 1) {
		c.failClosedLocked(ReasonControllerUnavailable)
		return false
	}
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
		(item.phase == reservationActiveDecode && cause == TerminalSuccess)
	var next reservation
	var newContribution reservationOverlay
	newValid := true
	if !remove {
		next = item
		next.phase = reservationResidualDebt
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
	forwardedExposure, responseExposure, exposureValid := reservationExposureCounts(item)
	if !exposureValid || (forwardedExposure > 0 &&
		!c.exposure.remove(c.now(), forwardedExposure, responseExposure)) {
		c.failClosedLocked(ReasonControllerUnavailable)
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

func (c *AdmissionController) unavailableDecisionLocked(reason Reason, demand TPSRequestDemand, state ProjectedState) DecisionRecord {
	return DecisionRecord{
		Action: ActionProtect, Reason: reason, Scope: ProtectionAvailability,
		Demand: demand, State: state,
		ObservationSequence: c.observationSequence,
		ControllerSequence:  c.eventSequence,
		RuntimeEpoch:        c.runtimeEpoch,
		PolicyRevision:      c.policyRevision,
	}
}

func (c *AdmissionController) decisionLocked(policy policyDecision, demand TPSRequestDemand, state ProjectedState) DecisionRecord {
	return DecisionRecord{
		Action: policy.action, Reason: policy.reason, Scope: policy.scope,
		Demand: demand, State: state,
		ProjectedRunning:         policy.projectedRunning,
		ProjectedWindowSequences: policy.projectedWindowSequences,
		RunningLimit:             c.runningLimit,
		RunningLimitSource:       c.runningLimitSource,
		WindowConcurrency:        c.windowConcurrency,
		TPSDecisionResult:        policy.tpsDecisionResult,
		TPSDecisionSubreason:     policy.tpsDecisionSubreason,
		ObservationSequence:      c.observationSequence,
		ControllerSequence:       c.eventSequence,
		RuntimeEpoch:             c.runtimeEpoch,
		PolicyRevision:           c.policyRevision,
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
	c.exposure.reset()
	c.lastExposure = sequenceExposureSnapshot{}
	c.tpsWindow.reset()
}

func reservationExposureCounts(item reservation) (forwarded, response int64, valid bool) {
	demand, ok := item.effectiveDemand()
	if !ok {
		return 0, 0, false
	}
	switch item.phase {
	case reservationReserved:
		return 0, 0, true
	case reservationForwarded:
		return demand.DecodeSequences, 0, true
	case reservationActiveDecode:
		return demand.DecodeSequences, 1, true
	default:
		return 0, 0, false
	}
}

func (c *AdmissionController) reconciledOverlayLocked(
	watermark uint64,
	observation BackendObservation,
	reconciliationAt time.Time,
) (reservationOverlay, int64, bool) {
	nextOverlay := c.overlay
	var forwardedSequenceLiabilities int64
	for _, item := range c.reservations {
		if item.forwardedSequence > 0 && item.forwardedSequence <= watermark {
			demand, valid := item.effectiveDemand()
			if !valid {
				return reservationOverlay{}, 0, false
			}
			var ok bool
			forwardedSequenceLiabilities, ok = addNonnegativeInt64(
				forwardedSequenceLiabilities,
				demand.DecodeSequences,
			)
			if !ok {
				return reservationOverlay{}, 0, false
			}
		}
		oldContribution, oldValid := item.contribution()
		if !oldValid {
			return reservationOverlay{}, 0, false
		}
		next, remove, changed := reconcileReservation(
			item,
			watermark,
			observation,
			reconciliationAt,
			c.pendingFirstByteLeaseDuration,
		)
		if remove {
			var ok bool
			nextOverlay, ok = subtractOverlay(nextOverlay, oldContribution)
			if !ok {
				return reservationOverlay{}, 0, false
			}
			continue
		}
		if changed {
			newContribution, newValid := next.contribution()
			if !newValid {
				return reservationOverlay{}, 0, false
			}
			var ok bool
			nextOverlay, ok = replaceOverlay(nextOverlay, oldContribution, newContribution)
			if !ok {
				return reservationOverlay{}, 0, false
			}
		}
	}
	return nextOverlay, forwardedSequenceLiabilities, true
}

func (c *AdmissionController) applyReconciliationLocked(
	watermark uint64,
	observation BackendObservation,
	reconciliationAt time.Time,
) {
	for id, item := range c.reservations {
		next, remove, changed := reconcileReservation(
			item,
			watermark,
			observation,
			reconciliationAt,
			c.pendingFirstByteLeaseDuration,
		)
		if remove {
			delete(c.reservations, id)
			continue
		}
		if changed {
			c.reservations[id] = next
		}
	}
}

func reconcileReservation(
	item reservation,
	watermark uint64,
	observation BackendObservation,
	reconciliationAt time.Time,
	pendingFirstByteLeaseDuration time.Duration,
) (reservation, bool, bool) {
	if item.phase == reservationResidualDebt && item.terminalSequence > 0 &&
		item.terminalSequence <= watermark {
		return reservation{}, true, true
	}
	next := item
	if item.phase == reservationForwarded && !item.pendingFirstByteReleased &&
		item.forwardedSequence > 0 && item.forwardedSequence <= watermark &&
		pendingFirstByteLeaseExpired(
			item,
			observation,
			reconciliationAt,
			pendingFirstByteLeaseDuration,
		) {
		next.pendingFirstByteReleased = true
	}
	return next, false, next != item
}

func pendingFirstByteLeaseExpired(
	item reservation,
	observation BackendObservation,
	reconciliationAt time.Time,
	duration time.Duration,
) bool {
	return duration > 0 && observation.Waiting == 0 && !item.forwardedAt.IsZero() &&
		!reconciliationAt.IsZero() && !reconciliationAt.Before(observation.ObservedAt) &&
		reconciliationAt.Sub(observation.ObservedAt) <= observation.MaximumAge &&
		!observation.ObservedAt.Before(item.forwardedAt) &&
		observation.ObservedAt.Sub(item.forwardedAt) >= duration
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
	if observation.RuntimeIdentity == "" ||
		observation.ObservedAt.IsZero() || observation.MaximumAge <= 0 ||
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
