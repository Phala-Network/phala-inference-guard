package admission

import (
	"math"
	"testing"
	"time"
)

func TestControllerRejectsOutOfOrderAndInvalidSamplesWithoutReplacement(t *testing.T) {
	now := time.Unix(13_000, 0)
	controller := testControllerWithObservation(t, testObservation(now, 1, 0, 1, 0))
	older, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("older sample window unavailable")
	}
	newer, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("newer sample window unavailable")
	}
	newerResult := controller.PublishObservation(newer, testObservation(now.Add(2*time.Millisecond), 4, 0, 2, 0))
	if !newerResult.Accepted {
		t.Fatalf("newer publication=%+v", newerResult)
	}
	olderResult := controller.PublishObservation(older, testObservation(now.Add(time.Millisecond), 9, 0, 3, 0))
	if olderResult.Accepted || olderResult.Reason != ReasonObservationInvalid {
		t.Fatalf("older publication=%+v", olderResult)
	}
	invalidWindow, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("invalid sample window unavailable")
	}
	invalid := testObservation(now.Add(3*time.Millisecond), -1, 0, 3, 0)
	invalidResult := controller.PublishObservation(invalidWindow, invalid)
	if invalidResult.Accepted || invalidResult.Reason != ReasonObservationInvalid {
		t.Fatalf("invalid publication=%+v", invalidResult)
	}
	snapshot := controller.Snapshot(now.Add(4 * time.Millisecond))
	if snapshot.Observation.Running != 4 ||
		snapshot.ObservationSequence != newerResult.ObservationSequence {
		t.Fatalf("invalid sample replaced coherent state: %+v", snapshot)
	}
}

func TestControllerStaleDecisionRetainsReadOnlyState(t *testing.T) {
	now := time.Unix(13_500, 0)
	observation := testObservation(now, 2, 1, 1, 0)
	observation.MaximumAge = 100 * time.Millisecond
	controller := testControllerWithObservation(t, observation)
	decision := controller.Admit(now.Add(time.Second), testDemand(1)).Decision
	if decision.Reason != ReasonObservationStale ||
		decision.State.RawRunning != 2 ||
		decision.State.RawWaiting != 1 {
		t.Fatalf("stale decision lost observed state: %+v", decision)
	}
}

func TestControllerCloseFencesHandlesAndClearsState(t *testing.T) {
	now := time.Unix(14_000, 0)
	controller := testControllerWithObservation(t, testObservation(now, 0, 0, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if !result.Decision.Admitted() {
		t.Fatalf("admission=%+v", result.Decision)
	}
	controller.Close()
	if result.Handle.MarkForwarded() || result.Handle.Terminate(TerminalShutdown) {
		t.Fatal("closed Controller accepted old handle")
	}
	snapshot := controller.Snapshot(now.Add(2 * time.Millisecond))
	if snapshot.MinimumDecision.Reason != ReasonClosed ||
		snapshot.MinimumDecision.Scope != ProtectionAvailability {
		t.Fatalf("closed snapshot=%+v", snapshot)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if len(controller.reservations) != 0 || controller.overlay != (reservationOverlay{}) {
		t.Fatalf("closed state reservations=%d overlay=%+v", len(controller.reservations), controller.overlay)
	}
}

func TestControllerSequenceOverflowFailsClosedWithoutReuse(t *testing.T) {
	now := time.Unix(15_000, 0)

	t.Run("event", func(t *testing.T) {
		controller := testControllerWithObservation(t, testObservation(now, 0, 0, 1, 0))
		controller.mu.Lock()
		controller.eventSequence = math.MaxUint64
		controller.mu.Unlock()
		decision := controller.Admit(now.Add(time.Millisecond), testDemand(1)).Decision
		if decision.Reason != ReasonCounterOverflow {
			t.Fatalf("event overflow decision=%+v", decision)
		}
	})
	t.Run("reservation", func(t *testing.T) {
		controller := testControllerWithObservation(t, testObservation(now, 0, 0, 1, 0))
		controller.mu.Lock()
		controller.nextReservationID = math.MaxUint64
		controller.mu.Unlock()
		decision := controller.Admit(now.Add(time.Millisecond), testDemand(1)).Decision
		if decision.Reason != ReasonCounterOverflow {
			t.Fatalf("reservation overflow decision=%+v", decision)
		}
	})
	t.Run("sample", func(t *testing.T) {
		controller := testControllerWithObservation(t, testObservation(now, 0, 0, 1, 0))
		controller.mu.Lock()
		controller.sampleSequence = math.MaxUint64
		controller.mu.Unlock()
		if _, ok := controller.StartSampleWindow(); ok {
			t.Fatal("sample sequence wrapped")
		}
		if snapshot := controller.Snapshot(now); snapshot.MinimumDecision.Reason != ReasonCounterOverflow {
			t.Fatalf("sample overflow snapshot=%+v", snapshot)
		}
	})
	t.Run("observation", func(t *testing.T) {
		controller := testControllerWithObservation(t, testObservation(now, 0, 0, 1, 0))
		window, ok := controller.StartSampleWindow()
		if !ok {
			t.Fatal("sample window unavailable")
		}
		controller.mu.Lock()
		controller.observationSequence = math.MaxUint64
		controller.mu.Unlock()
		publication := controller.PublishObservation(window, testObservation(now.Add(time.Millisecond), 0, 0, 2, 0))
		if publication.Accepted || publication.Reason != ReasonCounterOverflow {
			t.Fatalf("observation overflow publication=%+v", publication)
		}
	})
}
