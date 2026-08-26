package admission

import (
	"testing"
	"time"
)

func TestWaitingProtectsWhenTPSReferenceIsDisabled(t *testing.T) {
	now := time.Unix(20_000, 0)
	controller := testControllerWithObservation(t, testObservation(now, 2, 1, 1, 0))

	decision := controller.Admit(now.Add(time.Millisecond), testDemand(1)).Decision
	if decision.Admitted() || decision.Reason != ReasonTPSReference ||
		decision.TPSDecisionResult != TPSDecisionResultProtect ||
		decision.TPSDecisionSubreason != TPSDecisionSubreasonWaiting ||
		decision.ReservationID != 0 {
		t.Fatalf("disabled-TPS waiting decision=%+v", decision)
	}
}

func TestPendingFirstByteSurvivesOrdinaryPollAndReleasesOnFirstByte(t *testing.T) {
	now := time.Unix(20_500, 0)
	clock := &manualAdmissionClock{at: now}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:   testRuntimeIdentity,
		WindowConcurrency: 2,
		Now:               clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(now, 0, 0, 1, 0))

	first := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	second := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	clock.Set(now.Add(time.Millisecond))
	if !first.Decision.Admitted() || !second.Decision.Admitted() ||
		!first.Handle.MarkForwarded() || !second.Handle.MarkForwarded() {
		t.Fatalf("initial admissions=%+v/%+v", first.Decision, second.Decision)
	}

	clock.Set(now.Add(500 * time.Millisecond))
	publishObservation(t, controller, testObservation(now.Add(500*time.Millisecond), 0, 0, 2, 0))
	protected := controller.Admit(now.Add(501*time.Millisecond), testDemand(1)).Decision
	if protected.Admitted() || protected.Reason != ReasonWindowConcurrency ||
		protected.State.UnobservedSequences != 2 || protected.ReservationID != 0 {
		t.Fatalf("ordinary poll released pending first byte: %+v", protected)
	}

	clock.Set(now.Add(502 * time.Millisecond))
	if !first.Handle.MarkFirstByte() {
		t.Fatal("first-byte transition failed")
	}
	replacement := controller.Admit(now.Add(503*time.Millisecond), testDemand(1))
	if !replacement.Decision.Admitted() || replacement.Decision.ProjectedWindowSequences != 2 {
		t.Fatalf("first byte did not release one slot: %+v", replacement.Decision)
	}

	for _, handle := range []ReservationHandle{first.Handle, second.Handle, replacement.Handle} {
		if !handle.Terminate(TerminalCancel) {
			t.Fatal("cleanup terminal failed")
		}
	}
}

func TestPendingFirstByteLeaseExpiresOnlyWithFreshZeroWaiting(t *testing.T) {
	now := time.Unix(21_000, 0)
	clock := &manualAdmissionClock{at: now}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:   testRuntimeIdentity,
		WindowConcurrency: 1,
		Now:               clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(now, 0, 0, 1, 0))

	lease := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	clock.Set(now.Add(time.Millisecond))
	if !lease.Decision.Admitted() || !lease.Handle.MarkForwarded() {
		t.Fatalf("lease admission=%+v", lease.Decision)
	}

	clock.Set(now.Add(2 * time.Second))
	publishObservation(t, controller, testObservation(now.Add(2*time.Second), 0, 1, 2, 0))
	underWaiting := controller.Snapshot(now.Add(2*time.Second + time.Millisecond))
	if underWaiting.State.UnobservedSequences != 1 || underWaiting.Available ||
		underWaiting.MinimumDecision.TPSDecisionSubreason != TPSDecisionSubreasonWaiting {
		t.Fatalf("waiting expired or opened pending lease: %+v", underWaiting)
	}

	clock.Set(now.Add(2500 * time.Millisecond))
	publishObservation(t, controller, testObservation(now.Add(2500*time.Millisecond), 0, 0, 3, 0))
	replacement := controller.Admit(now.Add(2501*time.Millisecond), testDemand(1))
	if !replacement.Decision.Admitted() || replacement.Decision.State.UnobservedSequences != 0 {
		t.Fatalf("fresh zero-waiting observation did not expire lease: %+v", replacement.Decision)
	}

	if !lease.Handle.Terminate(TerminalCancel) || !replacement.Handle.Terminate(TerminalCancel) {
		t.Fatal("lease cleanup failed")
	}
}

func TestPendingFirstByteTerminalReleasesImmediately(t *testing.T) {
	now := time.Unix(21_500, 0)
	clock := &manualAdmissionClock{at: now}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:   testRuntimeIdentity,
		WindowConcurrency: 1,
		Now:               clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(now, 0, 0, 1, 0))

	result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	clock.Set(now.Add(time.Millisecond))
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() ||
		!result.Handle.Terminate(TerminalTimeout) {
		t.Fatalf("terminal fixture=%+v", result.Decision)
	}
	after := controller.Snapshot(now.Add(2 * time.Millisecond))
	if after.State.UnobservedSequences != 0 || after.State.LiveReservations != 0 ||
		after.State.ResidualDebts != 1 {
		t.Fatalf("terminal retained pending-first-byte slot: %+v", after.State)
	}

	replacement := controller.Admit(now.Add(3*time.Millisecond), testDemand(1))
	if !replacement.Decision.Admitted() {
		t.Fatalf("terminal release did not reopen slot: %+v", replacement.Decision)
	}
	if !replacement.Handle.Terminate(TerminalCancel) {
		t.Fatal("replacement cleanup failed")
	}
}
