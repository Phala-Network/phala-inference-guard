package server

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

const (
	priorityQueueMaximumEntries = 64
	priorityQueueCoalesceDelay  = 2 * time.Millisecond
	priorityQueueMaximumWait    = 50 * time.Millisecond
)

// priorityAdmissionService adds only a short, bounded ordering stage in front
// of the real admission service. The wrapped service remains the sole owner of
// QoS policy and reservations.
type priorityAdmissionService struct {
	delegate admissionService
	now      func() time.Time

	maxEntries int
	coalesce   time.Duration
	maxWait    time.Duration

	mu     sync.Mutex
	queue  []*priorityAdmissionRequest
	nextID uint64
	closed bool

	wake    chan struct{}
	closeCh chan struct{}
	doneCh  chan struct{}

	closeOnce sync.Once
	closeErr  error

	full     atomic.Uint64
	enqueued atomic.Uint64
	timedOut atomic.Uint64
	canceled atomic.Uint64
}

type priorityAdmissionRequest struct {
	ctx        context.Context
	demand     coreadmission.TPSRequestDemand
	id         uint64
	enqueuedAt time.Time
	result     chan admissionDecision
}

type priorityQueueSnapshot struct {
	Depth    int
	Enqueued uint64
	Full     uint64
	TimedOut uint64
	Canceled uint64
}

func newPriorityAdmissionService(delegate admissionService) *priorityAdmissionService {
	return newPriorityAdmissionServiceWithConfig(
		delegate,
		priorityQueueMaximumEntries,
		priorityQueueCoalesceDelay,
		priorityQueueMaximumWait,
		time.Now,
	)
}

func newPriorityAdmissionServiceWithConfig(
	delegate admissionService,
	maxEntries int,
	coalesce time.Duration,
	maxWait time.Duration,
	now func() time.Time,
) *priorityAdmissionService {
	if maxEntries <= 0 {
		maxEntries = priorityQueueMaximumEntries
	}
	if coalesce < 0 {
		coalesce = priorityQueueCoalesceDelay
	}
	if maxWait <= 0 {
		maxWait = priorityQueueMaximumWait
	}
	if maxWait < coalesce {
		maxWait = coalesce
	}
	if now == nil {
		now = time.Now
	}
	service := &priorityAdmissionService{
		delegate:   delegate,
		now:        now,
		maxEntries: maxEntries,
		coalesce:   coalesce,
		maxWait:    maxWait,
		wake:       make(chan struct{}, 1),
		closeCh:    make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
	go service.run()
	return service
}

func (s *priorityAdmissionService) Decide(
	ctx context.Context,
	demand coreadmission.TPSRequestDemand,
) admissionDecision {
	if s == nil || s.delegate == nil {
		return unavailableAdmissionDecision(demand)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		s.canceled.Add(1)
		return priorityQueueDecision(demand, coreadmission.ReasonPriorityQueueCanceled)
	}

	item := &priorityAdmissionRequest{
		ctx:        ctx,
		demand:     demand,
		result:     make(chan admissionDecision, 1),
		enqueuedAt: s.now(),
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return priorityQueueDecision(demand, coreadmission.ReasonClosed)
	}
	if len(s.queue) >= s.maxEntries {
		s.mu.Unlock()
		s.full.Add(1)
		return priorityQueueDecision(demand, coreadmission.ReasonPriorityQueueFull)
	}
	s.nextID++
	item.id = s.nextID
	s.queue = append(s.queue, item)
	s.enqueued.Add(1)
	s.mu.Unlock()
	s.signal()

	// Do not select on ctx here. The worker either skips a canceled item or
	// releases a reservation obtained during a cancellation race before sending
	// the result. Waiting for that result keeps the reservation lifecycle owned
	// by exactly one side.
	return <-item.result
}

func (s *priorityAdmissionService) Snapshot(now time.Time) admissionTelemetrySnapshot {
	if s == nil || s.delegate == nil {
		return admissionTelemetrySnapshot{}
	}
	snapshot := s.delegate.Snapshot(now)
	return snapshot
}

// UpdatePolicy preserves the runtime policy-management surface while keeping
// the queue as a transparent ordering layer. Policy mutations must reach the
// underlying controller; the queue itself has no mutable admission policy.
func (s *priorityAdmissionService) UpdatePolicy(
	update coreadmission.PolicyUpdate,
) (coreadmission.PolicyUpdateResult, error) {
	if s == nil || s.delegate == nil {
		return coreadmission.PolicyUpdateResult{}, coreadmission.ErrPolicyUnavailable
	}
	service, ok := s.delegate.(admissionPolicyService)
	if !ok {
		return coreadmission.PolicyUpdateResult{}, coreadmission.ErrPolicyUnavailable
	}
	return service.UpdatePolicy(update)
}

func (s *priorityAdmissionService) QueueSnapshot() priorityQueueSnapshot {
	if s == nil {
		return priorityQueueSnapshot{}
	}
	s.mu.Lock()
	depth := len(s.queue)
	s.mu.Unlock()
	return priorityQueueSnapshot{
		Depth:    depth,
		Enqueued: s.enqueued.Load(),
		Full:     s.full.Load(),
		TimedOut: s.timedOut.Load(),
		Canceled: s.canceled.Load(),
	}
}

func (s *priorityAdmissionService) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		close(s.closeCh)
		s.signal()
		<-s.doneCh
		if s.delegate != nil {
			s.closeErr = s.delegate.Close()
		}
	})
	return s.closeErr
}

func (s *priorityAdmissionService) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *priorityAdmissionService) run() {
	defer close(s.doneCh)
	for {
		item, wait, closed := s.next()
		if closed {
			s.drain(coreadmission.ReasonClosed)
			return
		}
		if item != nil {
			s.dispatch(item)
			continue
		}

		var timer *time.Timer
		if wait > 0 {
			timer = time.NewTimer(wait)
		}
		select {
		case <-s.wake:
			if timer != nil {
				stopTimer(timer)
			}
		case <-s.closeCh:
			if timer != nil {
				stopTimer(timer)
			}
			s.drain(coreadmission.ReasonClosed)
			return
		case <-timerChannel(timer):
		}
	}
}

func (s *priorityAdmissionService) next() (*priorityAdmissionRequest, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, 0, true
	}
	if len(s.queue) == 0 {
		return nil, 0, false
	}
	now := s.now()
	oldestAt := s.queue[0].enqueuedAt
	for _, item := range s.queue[1:] {
		if item.enqueuedAt.Before(oldestAt) {
			oldestAt = item.enqueuedAt
		}
	}
	readyAt := oldestAt.Add(s.coalesce)
	if now.Before(readyAt) {
		return nil, readyAt.Sub(now), false
	}
	index := s.chooseIndex(now)
	item := s.queue[index]
	s.queue = append(s.queue[:index], s.queue[index+1:]...)
	return item, 0, false
}

func (s *priorityAdmissionService) chooseIndex(now time.Time) int {
	// An expired entry is always selected oldest-first so no priority class can
	// keep another class past the hard local wait bound.
	selected := -1
	for index, item := range s.queue {
		if now.Sub(item.enqueuedAt) >= s.maxWait &&
			(selected < 0 || item.enqueuedAt.Before(s.queue[selected].enqueuedAt)) {
			selected = index
		}
	}
	if selected >= 0 {
		return selected
	}

	// Promote a basic request after half the deadline to prevent a continuous
	// premium stream from starving ordinary traffic.
	agingThreshold := s.maxWait / 2
	for index, item := range s.queue {
		if item.demand.Priority == coreadmission.RequestPriorityBasic &&
			now.Sub(item.enqueuedAt) >= agingThreshold &&
			(selected < 0 || item.enqueuedAt.Before(s.queue[selected].enqueuedAt)) {
			selected = index
		}
	}
	if selected >= 0 {
		return selected
	}

	for index, item := range s.queue {
		if selected < 0 || item.demand.Priority > s.queue[selected].demand.Priority ||
			(item.demand.Priority == s.queue[selected].demand.Priority &&
				item.enqueuedAt.Before(s.queue[selected].enqueuedAt)) {
			selected = index
		}
	}
	return selected
}

func (s *priorityAdmissionService) dispatch(item *priorityAdmissionRequest) {
	if item.ctx.Err() != nil {
		s.canceled.Add(1)
		item.result <- priorityQueueDecision(item.demand, coreadmission.ReasonPriorityQueueCanceled)
		return
	}
	if s.now().Sub(item.enqueuedAt) >= s.maxWait {
		s.timedOut.Add(1)
		item.result <- priorityQueueDecision(item.demand, coreadmission.ReasonPriorityQueueTimeout)
		return
	}
	decision := safePriorityDelegateDecide(s.delegate, item.ctx, item.demand)
	if item.ctx.Err() != nil {
		if decision.Reservation != nil {
			_ = decision.Reservation.Terminate(coreadmission.TerminalCancel)
		}
		s.canceled.Add(1)
		decision = priorityQueueDecision(item.demand, coreadmission.ReasonPriorityQueueCanceled)
	}
	item.result <- decision
}

func (s *priorityAdmissionService) drain(reason coreadmission.Reason) {
	s.mu.Lock()
	items := s.queue
	s.queue = nil
	s.mu.Unlock()
	for _, item := range items {
		item.result <- priorityQueueDecision(item.demand, reason)
	}
}

func safePriorityDelegateDecide(
	delegate admissionService,
	ctx context.Context,
	demand coreadmission.TPSRequestDemand,
) (result admissionDecision) {
	defer func() {
		if recover() != nil {
			result = unavailableAdmissionDecision(demand)
		}
	}()
	result = delegate.Decide(ctx, demand)
	if !result.valid() {
		if result.Reservation != nil {
			_ = result.Reservation.Terminate(coreadmission.TerminalCancel)
		}
		result = unavailableAdmissionDecision(demand)
	}
	return result
}

func priorityQueueDecision(
	demand coreadmission.TPSRequestDemand,
	reason coreadmission.Reason,
) admissionDecision {
	scope := coreadmission.ProtectionLoad
	if reason == coreadmission.ReasonPriorityQueueCanceled || reason == coreadmission.ReasonClosed {
		scope = coreadmission.ProtectionAvailability
	}
	return admissionDecision{Record: coreadmission.DecisionRecord{
		Action: coreadmission.ActionProtect,
		Reason: reason,
		Scope:  scope,
		Demand: demand,
	}}
}

func stopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func timerChannel(timer *time.Timer) <-chan time.Time {
	if timer == nil {
		return nil
	}
	return timer.C
}
