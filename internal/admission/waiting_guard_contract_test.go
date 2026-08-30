package admission

import (
	"testing"
	"time"
)

func TestWaitingRequiresConfirmationWhenTPSReferenceIsDisabled(t *testing.T) {
	now := time.Unix(20_000, 0)
	controller := testControllerWithObservation(t, testObservation(now, 2, 1, 1, 0))

	transient := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if !transient.Decision.Admitted() ||
		transient.Decision.TPSDecisionSubreason == TPSDecisionSubreasonWaiting {
		t.Fatalf("first waiting observation was not treated as transient: %+v", transient.Decision)
	}
	if !transient.Handle.Terminate(TerminalCancel) {
		t.Fatal("transient waiting admission cleanup failed")
	}

	confirmedAt := now.Add(500 * time.Millisecond)
	publishObservation(t, controller, testObservation(confirmedAt, 2, 1, 2, 0))
	confirmed := controller.Admit(confirmedAt.Add(time.Millisecond), testDemand(1)).Decision
	if confirmed.Admitted() || confirmed.Reason != ReasonTPSReference ||
		confirmed.TPSDecisionResult != TPSDecisionResultProtect ||
		confirmed.TPSDecisionSubreason != TPSDecisionSubreasonWaiting ||
		confirmed.ReservationID != 0 {
		t.Fatalf("confirmed waiting decision=%+v", confirmed)
	}

	clearedAt := now.Add(time.Second)
	publishObservation(t, controller, testObservation(clearedAt, 2, 0, 3, 0))
	cleared := controller.Admit(clearedAt.Add(time.Millisecond), testDemand(1))
	if !cleared.Decision.Admitted() {
		t.Fatalf("first zero-waiting observation did not reopen intake: %+v", cleared.Decision)
	}
	if !cleared.Handle.Terminate(TerminalCancel) {
		t.Fatal("cleared waiting admission cleanup failed")
	}
}

func TestWaitingAtWindowConcurrencyProtectsImmediately(t *testing.T) {
	now := time.Unix(20_250, 0)
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:   testRuntimeIdentity,
		WindowConcurrency: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(now, 1, 4, 1, 0))

	decision := controller.Admit(now.Add(time.Millisecond), testDemand(1)).Decision
	if decision.Admitted() || decision.Reason != ReasonTPSReference ||
		decision.TPSDecisionSubreason != TPSDecisionSubreasonWaiting {
		t.Fatalf("window-sized waiting burst did not protect immediately: %+v", decision)
	}
}

func TestWaitingConfirmationResetsAcrossBackendRuntimeReset(t *testing.T) {
	now := time.Unix(20_375, 0)
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:   testRuntimeIdentity,
		WindowConcurrency: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := testObservation(now, 1, 1, 10, 0)
	first.RuntimeStartTime = 100
	publishObservation(t, controller, first)

	resetAt := now.Add(500 * time.Millisecond)
	reset := testObservation(resetAt, 1, 1, 1, 0)
	reset.RuntimeStartTime = 200
	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("reset sample window unavailable")
	}
	publication := controller.PublishObservation(window, reset)
	if !publication.Accepted || !publication.RuntimeReset {
		t.Fatalf("runtime reset publication=%+v", publication)
	}
	decision := controller.Admit(resetAt.Add(time.Millisecond), testDemand(1))
	if !decision.Decision.Admitted() || decision.Decision.State.PreviousRawWaiting != 0 {
		t.Fatalf("runtime reset retained waiting confirmation: %+v", decision.Decision)
	}
	if !decision.Handle.Terminate(TerminalCancel) {
		t.Fatal("runtime-reset admission cleanup failed")
	}
}

func TestWaitingConfirmationRequiresAdjacentFreshObservations(t *testing.T) {
	now := time.Unix(20_437, 0)
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:   testRuntimeIdentity,
		WindowConcurrency: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	first := testObservation(now, 1, 1, 1, 0)
	first.MaximumAge = 500 * time.Millisecond
	publishObservation(t, controller, first)

	afterGap := now.Add(2 * time.Second)
	second := testObservation(afterGap, 1, 1, 2, 0)
	second.MaximumAge = 500 * time.Millisecond
	publishObservation(t, controller, second)
	transient := controller.Admit(afterGap.Add(time.Millisecond), testDemand(1))
	if !transient.Decision.Admitted() || transient.Decision.State.ObservationIntervalValid {
		t.Fatalf("non-adjacent waiting observations confirmed pressure: %+v", transient.Decision)
	}
	if !transient.Handle.Terminate(TerminalCancel) {
		t.Fatal("non-adjacent admission cleanup failed")
	}

	confirmedAt := afterGap.Add(500 * time.Millisecond)
	third := testObservation(confirmedAt, 1, 1, 3, 0)
	third.MaximumAge = 500 * time.Millisecond
	publishObservation(t, controller, third)
	confirmed := controller.Admit(confirmedAt.Add(time.Millisecond), testDemand(1)).Decision
	if confirmed.Admitted() || confirmed.TPSDecisionSubreason != TPSDecisionSubreasonWaiting {
		t.Fatalf("adjacent fresh waiting observation did not confirm pressure: %+v", confirmed)
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

func TestStaleObservationCannotExpirePendingFirstByteLease(t *testing.T) {
	now := time.Unix(22_000, 0)
	clock := &manualAdmissionClock{at: now}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:   testRuntimeIdentity,
		WindowConcurrency: 1,
		Now:               clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := testObservation(now, 0, 0, 1, 0)
	initial.MaximumAge = 500 * time.Millisecond
	publishObservation(t, controller, initial)

	result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	clock.Set(now.Add(time.Millisecond))
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() {
		t.Fatalf("lease admission=%+v", result.Decision)
	}

	clock.Set(now.Add(3 * time.Second))
	stale := testObservation(now.Add(2*time.Second), 0, 0, 2, 0)
	stale.MaximumAge = 500 * time.Millisecond
	publishObservation(t, controller, stale)
	staleSnapshot := controller.Snapshot(now.Add(3 * time.Second))
	if staleSnapshot.State.UnobservedSequences != 1 ||
		staleSnapshot.MinimumDecision.Reason != ReasonObservationStale {
		t.Fatalf("stale observation expired or opened lease: %+v", staleSnapshot)
	}

	clock.Set(now.Add(3100 * time.Millisecond))
	fresh := testObservation(now.Add(3100*time.Millisecond), 0, 0, 3, 0)
	fresh.MaximumAge = 500 * time.Millisecond
	publishObservation(t, controller, fresh)
	if after := controller.Snapshot(now.Add(3101 * time.Millisecond)); after.State.UnobservedSequences != 0 || !after.Available {
		t.Fatalf("fresh observation did not expire lease: %+v", after)
	}

	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("lease cleanup failed")
	}
}
