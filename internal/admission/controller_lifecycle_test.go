package admission

import (
	"testing"
	"time"
)

func TestControllerLifecycleIsMonotonicIdempotentAndAggregateExact(t *testing.T) {
	now := time.Unix(8_000, 0)
	clock := &manualAdmissionClock{at: now}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(now, 0, 0, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if !result.Decision.Admitted() {
		t.Fatalf("admission=%+v", result.Decision)
	}
	assertAggregateMatchesSlow(t, controller)
	clock.Set(now.Add(time.Millisecond))
	if !result.Handle.MarkForwarded() || result.Handle.MarkForwarded() {
		t.Fatal("forward transition was not exactly once")
	}
	assertAggregateMatchesSlow(t, controller)
	clock.Set(now.Add(2 * time.Millisecond))
	if !result.Handle.MarkFirstByte() || result.Handle.MarkFirstByte() || result.Handle.MarkForwarded() {
		t.Fatal("first-byte transition was not monotonic")
	}
	assertAggregateMatchesSlow(t, controller)
	clock.Set(now.Add(3 * time.Millisecond))
	if !result.Handle.Terminate(TerminalError) ||
		result.Handle.Terminate(TerminalSuccess) ||
		result.Handle.MarkFirstByte() {
		t.Fatal("terminal transition was not exactly once")
	}
	assertAggregateMatchesSlow(t, controller)
	clock.Set(now.Add(4 * time.Millisecond))
	publishObservation(t, controller, testObservation(now.Add(4*time.Millisecond), 0, 0, 2, 0))
	assertAggregateMatchesSlow(t, controller)
}

func TestControllerInFlightSampleCannotEraseLaterTerminalDebt(t *testing.T) {
	now := time.Unix(8_500, 0)
	clock := &manualAdmissionClock{at: now}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(now, 0, 0, 10, 0))
	result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	clock.Set(now.Add(time.Millisecond))
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() {
		t.Fatalf("admission=%+v", result.Decision)
	}
	inFlight, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("sample window unavailable")
	}
	clock.Set(now.Add(2 * time.Millisecond))
	if !result.Handle.Terminate(TerminalError) {
		t.Fatal("terminal transition failed")
	}
	first := controller.PublishObservation(inFlight, testObservation(now.Add(2*time.Millisecond), 0, 0, 11, 0))
	if !first.Accepted {
		t.Fatalf("first publication=%+v", first)
	}
	stillReserved := controller.Snapshot(now.Add(3 * time.Millisecond))
	if stillReserved.State.ResidualDebts != 1 ||
		stillReserved.State.SequenceLiabilities != 1 {
		t.Fatalf("in-flight sample erased later terminal: %+v", stillReserved.State)
	}
	clock.Set(now.Add(4 * time.Millisecond))
	publishObservation(t, controller, testObservation(now.Add(4*time.Millisecond), 0, 0, 12, 0))
	released := controller.Snapshot(now.Add(5 * time.Millisecond))
	if released.State.ResidualDebts != 0 ||
		released.State.SequenceLiabilities != 0 {
		t.Fatalf("covering sample did not release debt: %+v", released.State)
	}
}

func TestControllerTerminalBeforeForwardReleasesImmediately(t *testing.T) {
	now := time.Unix(9_000, 0)
	controller := testControllerWithObservation(t, testObservation(now, 0, 0, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if !result.Decision.Admitted() || !result.Handle.Terminate(TerminalCancel) {
		t.Fatalf("admission=%+v", result.Decision)
	}
	snapshot := controller.Snapshot(now.Add(2 * time.Millisecond))
	if snapshot.State.LiveReservations != 0 ||
		snapshot.State.ResidualDebts != 0 ||
		snapshot.State.SequenceLiabilities != 0 ||
		snapshot.State.UnobservedSequences != 0 {
		t.Fatalf("pre-forward terminal leaked reservation: %+v", snapshot.State)
	}
	assertAggregateMatchesSlow(t, controller)
}

func TestControllerMultiSequenceLifecycleReconcilesOnce(t *testing.T) {
	now := time.Unix(9_500, 0)
	clock := &manualAdmissionClock{at: now}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(now, 0, 0, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testDemand(4))
	if !result.Decision.Admitted() {
		t.Fatalf("multi-sequence admission=%+v", result.Decision)
	}
	reserved := controller.Snapshot(now.Add(time.Millisecond)).State
	if reserved.UnobservedSequences != 4 ||
		reserved.SequenceLiabilities != 4 ||
		reserved.LiveReservations != 1 {
		t.Fatalf("multi-sequence reserved state=%+v", reserved)
	}
	clock.Set(now.Add(time.Millisecond))
	if !result.Handle.MarkForwarded() {
		t.Fatal("multi-sequence forward failed")
	}
	clock.Set(now.Add(2 * time.Millisecond))
	publishObservation(t, controller, testObservation(now.Add(2*time.Millisecond), 4, 0, 2, 0))
	pending := controller.Snapshot(now.Add(3 * time.Millisecond)).State
	if pending.UnobservedSequences != 4 || pending.SequenceLiabilities != 4 {
		t.Fatalf("ordinary poll released pending first byte=%+v", pending)
	}
	clock.Set(now.Add(3 * time.Millisecond))
	if !result.Handle.MarkFirstByte() {
		t.Fatal("multi-sequence first byte failed")
	}
	covered := controller.Snapshot(now.Add(3 * time.Millisecond)).State
	if covered.UnobservedSequences != 0 || covered.SequenceLiabilities != 4 {
		t.Fatalf("multi-sequence first-byte state=%+v", covered)
	}
	if !result.Handle.Terminate(TerminalError) {
		t.Fatal("multi-sequence terminal failed")
	}
	terminal := controller.Snapshot(now.Add(3 * time.Millisecond)).State
	if terminal.LiveReservations != 0 ||
		terminal.ResidualDebts != 1 ||
		terminal.SequenceLiabilities != 4 {
		t.Fatalf("multi-sequence terminal state=%+v", terminal)
	}
	clock.Set(now.Add(4 * time.Millisecond))
	publishObservation(t, controller, testObservation(now.Add(4*time.Millisecond), 0, 0, 3, 0))
	released := controller.Snapshot(now.Add(5 * time.Millisecond)).State
	if released.ResidualDebts != 0 || released.SequenceLiabilities != 0 {
		t.Fatalf("multi-sequence release state=%+v", released)
	}
}
