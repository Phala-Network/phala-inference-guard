package server

import (
	"context"
	"sync"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

type priorityTestAdmissionService struct {
	mu          sync.Mutex
	priorities  []coreadmission.RequestPriority
	recorded    []coreadmission.DecisionRecord
	decision    admissionDecision
	decideBlock <-chan struct{}
	closed      bool
}

func (s *priorityTestAdmissionService) Decide(
	ctx context.Context,
	demand coreadmission.TPSRequestDemand,
) admissionDecision {
	s.mu.Lock()
	s.priorities = append(s.priorities, demand.Priority)
	decision := s.decision
	if decision.Record.Demand.DecodeSequences == 0 {
		decision.Record.Demand = demand
	}
	s.mu.Unlock()
	if s.decideBlock != nil {
		select {
		case <-s.decideBlock:
		case <-ctx.Done():
			return priorityQueueDecision(demand, coreadmission.ReasonPriorityQueueCanceled)
		}
	}
	return decision
}

func (*priorityTestAdmissionService) Snapshot(time.Time) admissionTelemetrySnapshot {
	return admissionTelemetrySnapshot{}
}

func (s *priorityTestAdmissionService) RecordExternalDecision(
	_ time.Time,
	decision coreadmission.DecisionRecord,
) {
	s.mu.Lock()
	s.recorded = append(s.recorded, decision)
	s.mu.Unlock()
}

func (s *priorityTestAdmissionService) Close() error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

func (s *priorityTestAdmissionService) order() []coreadmission.RequestPriority {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]coreadmission.RequestPriority(nil), s.priorities...)
}

func (s *priorityTestAdmissionService) records() []coreadmission.DecisionRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]coreadmission.DecisionRecord(nil), s.recorded...)
}

func admittedPriorityDecision() admissionDecision {
	return admissionDecision{
		Record: coreadmission.DecisionRecord{
			Action: coreadmission.ActionAdmit,
			Reason: coreadmission.ReasonOpen,
		},
		Reservation: &priorityTestReservation{},
	}
}

type priorityTestReservation struct{}

func (*priorityTestReservation) MarkForwarded() bool { return true }
func (*priorityTestReservation) MarkFirstByte() bool { return true }
func (*priorityTestReservation) Terminate(coreadmission.TerminalCause) bool {
	return true
}

func TestPriorityAdmissionDispatchesPremiumBeforeBasicWithinBatch(t *testing.T) {
	delegate := &priorityTestAdmissionService{decision: admittedPriorityDecision()}
	service := newPriorityAdmissionServiceWithConfig(delegate, 8, 5*time.Millisecond, 100*time.Millisecond, time.Now)
	defer service.Close()

	start := make(chan struct{})
	var group sync.WaitGroup
	results := make(chan admissionDecision, 2)
	for _, priority := range []coreadmission.RequestPriority{
		coreadmission.RequestPriorityBasic,
		coreadmission.RequestPriorityPremium,
	} {
		group.Add(1)
		go func(priority coreadmission.RequestPriority) {
			defer group.Done()
			<-start
			results <- service.Decide(context.Background(), coreadmission.NewTPSRequestDemand(1).WithPriority(priority))
		}(priority)
	}
	close(start)
	group.Wait()
	close(results)
	for range results {
	}

	order := delegate.order()
	if len(order) != 2 || order[0] != coreadmission.RequestPriorityPremium ||
		order[1] != coreadmission.RequestPriorityBasic {
		t.Fatalf("dispatch order=%v want [premium basic]", order)
	}
}

func TestPriorityAdmissionStillReturnsDelegateTPSProtection(t *testing.T) {
	delegate := &priorityTestAdmissionService{decision: admissionDecision{
		Record: coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonTPSReference,
			Scope:  coreadmission.ProtectionLoad,
		},
	}}
	service := newPriorityAdmissionServiceWithConfig(delegate, 8, 0, 100*time.Millisecond, time.Now)
	defer service.Close()
	result := service.Decide(context.Background(), coreadmission.NewTPSRequestDemand(1).WithPriority(
		coreadmission.RequestPriorityPremium,
	))
	if result.Record.Admitted() || result.Record.Reason != coreadmission.ReasonTPSReference {
		t.Fatalf("delegate TPS protection was bypassed: %+v", result.Record)
	}
}

func TestPriorityAdmissionQueueFullIsImmediateAndBounded(t *testing.T) {
	block := make(chan struct{})
	delegate := &priorityTestAdmissionService{
		decision:    admittedPriorityDecision(),
		decideBlock: block,
	}
	service := newPriorityAdmissionServiceWithConfig(delegate, 1, 0, time.Second, time.Now)
	defer func() {
		service.Close()
	}()

	firstDone := make(chan admissionDecision, 1)
	go func() {
		firstDone <- service.Decide(context.Background(), coreadmission.NewTPSRequestDemand(1))
	}()
	deadline := time.Now().Add(time.Second)
	for len(delegate.order()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	queued := make(chan admissionDecision, 1)
	go func() {
		queued <- service.Decide(context.Background(), coreadmission.NewTPSRequestDemand(1))
	}()
	deadline = time.Now().Add(time.Second)
	for service.QueueSnapshot().Depth < 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	result := service.Decide(context.Background(), coreadmission.NewTPSRequestDemand(1))
	if result.Record.Admitted() || result.Record.Reason != coreadmission.ReasonPriorityQueueFull {
		t.Fatalf("queue-full decision=%+v", result.Record)
	}
	close(block)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first queued request did not finish")
	}
	select {
	case <-queued:
	case <-time.After(time.Second):
		t.Fatal("second queued request did not finish")
	}
}

func TestPriorityAdmissionCancellationDoesNotCallDelegate(t *testing.T) {
	delegate := &priorityTestAdmissionService{decision: admittedPriorityDecision()}
	service := newPriorityAdmissionServiceWithConfig(delegate, 8, 10*time.Millisecond, 100*time.Millisecond, time.Now)
	defer service.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result := service.Decide(ctx, coreadmission.NewTPSRequestDemand(1))
	if result.Record.Admitted() || result.Record.Reason != coreadmission.ReasonPriorityQueueCanceled ||
		len(delegate.order()) != 0 {
		t.Fatalf("canceled request reached delegate: result=%+v order=%v", result.Record, delegate.order())
	}
	recorded := delegate.records()
	if len(recorded) != 1 || recorded[0].Reason != coreadmission.ReasonPriorityQueueCanceled {
		t.Fatalf("canceled request was not reported: %+v", recorded)
	}
}

func TestPriorityAdmissionCloseDrainsQueue(t *testing.T) {
	delegate := &priorityTestAdmissionService{decision: admittedPriorityDecision()}
	service := newPriorityAdmissionServiceWithConfig(delegate, 8, time.Second, time.Second, time.Now)
	result := make(chan admissionDecision, 1)
	go func() {
		result <- service.Decide(context.Background(), coreadmission.NewTPSRequestDemand(1))
	}()
	time.Sleep(time.Millisecond)
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case decision := <-result:
		if decision.Record.Admitted() || decision.Record.Reason != coreadmission.ReasonClosed {
			t.Fatalf("closed queue decision=%+v", decision.Record)
		}
	case <-time.After(time.Second):
		t.Fatal("close did not drain queue")
	}
}

func TestPriorityQueueDecisionIsOpenAIProtection(t *testing.T) {
	decision := priorityQueueDecision(
		coreadmission.NewTPSRequestDemand(1).WithPriority(coreadmission.RequestPriorityPremium),
		coreadmission.ReasonPriorityQueueTimeout,
	)
	if decision.Record.Admitted() || decision.Record.Reason != coreadmission.ReasonPriorityQueueTimeout ||
		decision.Record.Scope != coreadmission.ProtectionLoad || decision.Record.Demand.Priority != coreadmission.RequestPriorityPremium {
		t.Fatalf("queue timeout decision=%+v", decision.Record)
	}
}
