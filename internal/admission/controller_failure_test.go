package admission

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestControllerAdmissionAndPublicationNeverMixObservationSequences(t *testing.T) {
	now := time.Unix(12_500, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 100, 0, 0, 1, 0))
	previousSequence := uint64(1)
	previousUsed := int64(100)
	for iteration := 1; iteration <= 200; iteration++ {
		window, ok := controller.StartSampleWindow()
		if !ok {
			t.Fatal("sample window unavailable")
		}
		newUsed := int64(100 + iteration)
		observation := testObservation(
			capability,
			now.Add(time.Duration(iteration)*time.Millisecond),
			newUsed,
			0,
			0,
			uint64(iteration+1),
			0,
		)
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var publication PublicationResult
		var decision DecisionRecord
		go func() {
			defer group.Done()
			<-start
			publication = controller.PublishObservation(window, observation)
		}()
		go func() {
			defer group.Done()
			<-start
			decision = controller.Admit(
				now.Add(time.Duration(iteration+1)*time.Millisecond),
				testEstimate(900_000, 900_000, 256),
			).Decision
		}()
		close(start)
		group.Wait()
		if !publication.Accepted {
			t.Fatalf("iteration %d publication=%+v", iteration, publication)
		}
		switch decision.ObservationSequence {
		case previousSequence:
			if decision.State.ObservedKVTokens != previousUsed {
				t.Fatalf("iteration %d old sequence/new state decision=%+v", iteration, decision)
			}
		case publication.ObservationSequence:
			if decision.State.ObservedKVTokens != newUsed {
				t.Fatalf("iteration %d new sequence/old state decision=%+v", iteration, decision)
			}
		default:
			t.Fatalf("iteration %d unexpected observation sequence decision=%+v publication=%+v", iteration, decision, publication)
		}
		previousSequence = publication.ObservationSequence
		previousUsed = newUsed
	}
}

func TestControllerRejectsOutOfOrderAndInvalidSamplesWithoutReplacingObservation(t *testing.T) {
	now := time.Unix(13_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 123, 0, 0, 1, 0))
	older, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("older sample window unavailable")
	}
	newer, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("newer sample window unavailable")
	}
	newerResult := controller.PublishObservation(newer, testObservation(capability, now.Add(2*time.Millisecond), 456, 0, 0, 2, 0))
	if !newerResult.Accepted {
		t.Fatalf("newer publication=%+v", newerResult)
	}
	olderResult := controller.PublishObservation(older, testObservation(capability, now.Add(time.Millisecond), 999, 0, 0, 3, 0))
	if olderResult.Accepted || olderResult.Reason != ReasonObservationInvalid {
		t.Fatalf("older publication=%+v", olderResult)
	}
	invalidWindow, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("invalid sample window unavailable")
	}
	invalid := testObservation(capability, now.Add(3*time.Millisecond), 0, 0, 0, 3, 0)
	invalid.UsedKVTokens = capability.KVCapacityTokens + 1
	invalidResult := controller.PublishObservation(invalidWindow, invalid)
	if invalidResult.Accepted || invalidResult.Reason != ReasonObservationInvalid {
		t.Fatalf("invalid publication=%+v", invalidResult)
	}
	snapshot := controller.Snapshot(now.Add(4 * time.Millisecond))
	if snapshot.State.ObservedKVTokens != 456 || snapshot.ObservationSequence != newerResult.ObservationSequence {
		t.Fatalf("invalid sample replaced coherent state: %+v", snapshot)
	}
}

func TestControllerCloseFencesHandlesAndClearsBoundedState(t *testing.T) {
	now := time.Unix(14_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !result.Decision.Admitted() {
		t.Fatalf("admission=%+v", result.Decision)
	}
	controller.Close()
	if result.Handle.MarkForwarded() || result.Handle.Terminate(TerminalShutdown) {
		t.Fatal("closed Controller accepted old handle")
	}
	snapshot := controller.Snapshot(now.Add(2 * time.Millisecond))
	if snapshot.MinimumDecision.Reason != ReasonClosed || snapshot.MinimumDecision.Scope != ProtectionAvailability {
		t.Fatalf("closed snapshot=%+v", snapshot)
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if len(controller.reservations) != 0 || controller.overlay != (reservationOverlay{}) {
		t.Fatalf("closed state reservations=%d overlay=%+v", len(controller.reservations), controller.overlay)
	}
}

func TestControllerBusyInitializationProtectsThenFreshCapacityReopens(t *testing.T) {
	now := time.Unix(14_500, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(
		capability,
		now,
		capability.KVHardLimitTokens+1,
		100,
		20,
		1,
		0,
	))
	protected := controller.Admit(now.Add(time.Millisecond), testEstimate(1, 1, capability.MinimumDecodeHorizonTokens)).Decision
	if protected.Reason != ReasonKVCapacity || protected.Scope != ProtectionLoad {
		t.Fatalf("busy initialization decision=%+v", protected)
	}
	publishObservation(t, controller, testObservation(capability, now.Add(2*time.Millisecond), 1_000, 10, 0, 2, 0))
	reopened := controller.Admit(now.Add(3*time.Millisecond), testEstimate(1, 1, capability.MinimumDecodeHorizonTokens)).Decision
	if !reopened.Admitted() {
		t.Fatalf("fresh capacity did not reopen Controller=%+v", reopened)
	}
}

func TestControllerEveryCapabilityGeometryDriftCloses(t *testing.T) {
	now := time.Unix(14_750, 0)
	capability := testCapability()
	mutations := map[string]func(*BackendObservation){
		"fingerprint": func(observation *BackendObservation) { observation.CapabilityFingerprint = "other" },
		"context":     func(observation *BackendObservation) { observation.MaxModelLenTokens++ },
		"capacity":    func(observation *BackendObservation) { observation.KVCapacityTokens++ },
		"block":       func(observation *BackendObservation) { observation.KVBlockSize++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
			window, ok := controller.StartSampleWindow()
			if !ok {
				t.Fatal("sample window unavailable")
			}
			observation := testObservation(capability, now.Add(time.Millisecond), 0, 0, 0, 2, 0)
			mutate(&observation)
			publication := controller.PublishObservation(window, observation)
			if !publication.CapabilityDrift || publication.Reason != ReasonCapabilityDrift {
				t.Fatalf("drift publication=%+v", publication)
			}
		})
	}
}

func TestControllerStaleDecisionRetainsReadOnlyObservedState(t *testing.T) {
	now := time.Unix(13_500, 0)
	capability := testCapability()
	observation := testObservation(capability, now, 321, 2, 1, 1, 0)
	observation.MaximumAge = 100 * time.Millisecond
	controller := testControllerWithObservation(t, capability, observation)
	decision := controller.Admit(now.Add(time.Second), testEstimate(1_024, 1_536, 256)).Decision
	if decision.Reason != ReasonObservationStale || decision.State.ObservedKVTokens != 321 ||
		decision.State.RawRunning != 2 || decision.State.RawWaiting != 1 {
		t.Fatalf("stale decision lost read-only observation: %+v", decision)
	}
}

func TestControllerSequenceOverflowFailsClosedWithoutReuse(t *testing.T) {
	now := time.Unix(15_000, 0)
	capability := testCapability()

	t.Run("event", func(t *testing.T) {
		controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
		controller.mu.Lock()
		controller.eventSequence = math.MaxUint64
		controller.mu.Unlock()
		decision := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256)).Decision
		if decision.Reason != ReasonCounterOverflow || decision.Scope != ProtectionAvailability {
			t.Fatalf("event overflow decision=%+v", decision)
		}
	})

	t.Run("reservation", func(t *testing.T) {
		controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
		controller.mu.Lock()
		controller.nextReservationID = math.MaxUint64
		controller.mu.Unlock()
		decision := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256)).Decision
		if decision.Reason != ReasonCounterOverflow || decision.Scope != ProtectionAvailability {
			t.Fatalf("reservation overflow decision=%+v", decision)
		}
	})

	t.Run("sample", func(t *testing.T) {
		controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
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
		controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
		window, ok := controller.StartSampleWindow()
		if !ok {
			t.Fatal("sample window unavailable")
		}
		controller.mu.Lock()
		controller.observationSequence = math.MaxUint64
		controller.mu.Unlock()
		publication := controller.PublishObservation(window, testObservation(capability, now.Add(time.Millisecond), 0, 0, 0, 2, 0))
		if publication.Accepted || publication.Reason != ReasonCounterOverflow {
			t.Fatalf("observation overflow publication=%+v", publication)
		}
	})
}
