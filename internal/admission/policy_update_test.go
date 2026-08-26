package admission

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestPolicyUpdateResetsTPSWindowOnlyWhenReferenceChanges(t *testing.T) {
	start := time.Unix(70_000, 0)
	clock := &manualAdmissionClock{at: start}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		TPS:             TPSPolicyConfig{Reference: 20},
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(start, 2, 0, 0, 0))
	for step := 1; step <= 4; step++ {
		clock.Set(start.Add(time.Duration(step) * time.Second))
		publishObservation(t, controller, testObservation(clock.Now(), 2, 0, uint64(step*80), 0))
	}
	if before := controller.Snapshot(clock.Now().Add(time.Millisecond)); !before.State.TPS.Ready {
		t.Fatalf("window did not warm: %+v", before.State.TPS)
	}

	window := int64(48)
	updated, err := controller.UpdatePolicy(PolicyUpdate{
		ExpectedRevision:  1,
		WindowConcurrency: &window,
		UpdatedAt:         clock.Now().Add(time.Millisecond),
	})
	if err != nil || updated.TPSWindowReset || updated.Policy.WindowConcurrency != 48 {
		t.Fatalf("bound update=%+v err=%v", updated, err)
	}
	if snapshot := controller.Snapshot(clock.Now().Add(time.Millisecond)); !snapshot.State.TPS.Ready {
		t.Fatalf("bound update reset TPS evidence: %+v", snapshot.State.TPS)
	}

	reference := 25.0
	updated, err = controller.UpdatePolicy(PolicyUpdate{
		ExpectedRevision: 2,
		TPSReference:     &reference,
		UpdatedAt:        clock.Now().Add(2 * time.Millisecond),
	})
	if err != nil || !updated.TPSWindowReset || updated.Policy.TPSReference != 25 {
		t.Fatalf("TPS update=%+v err=%v", updated, err)
	}
	if snapshot := controller.Snapshot(clock.Now().Add(3 * time.Millisecond)); snapshot.State.TPS.Ready || snapshot.State.TPS.QualifiedSamples != 0 {
		t.Fatalf("TPS update retained old evidence: %+v", snapshot.State.TPS)
	}
}

func TestPolicyUpdateCASAndValidation(t *testing.T) {
	start := time.Unix(71_000, 0)
	controller := testControllerWithObservation(t, testObservation(start, 1, 0, 0, 0))
	negative := -1.0
	zeroWindow := int64(0)
	for _, update := range []PolicyUpdate{
		{ExpectedRevision: 0, TPSReference: floatPointer(20), UpdatedAt: start.Add(time.Second)},
		{ExpectedRevision: 1, TPSReference: &negative, UpdatedAt: start.Add(time.Second)},
		{ExpectedRevision: 1, WindowConcurrency: &zeroWindow, UpdatedAt: start.Add(time.Second)},
		{ExpectedRevision: 1, UpdatedAt: start.Add(time.Second)},
	} {
		if _, err := controller.UpdatePolicy(update); !errors.Is(err, ErrPolicyInvalid) {
			t.Fatalf("invalid update=%+v err=%v", update, err)
		}
	}
	window := int64(64)
	if _, err := controller.UpdatePolicy(PolicyUpdate{
		ExpectedRevision:  1,
		WindowConcurrency: &window,
		UpdatedAt:         start.Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := controller.UpdatePolicy(PolicyUpdate{
		ExpectedRevision: 1,
		TPSReference:     floatPointer(25),
		UpdatedAt:        start.Add(2 * time.Second),
	}); !errors.Is(err, ErrPolicyRevisionConflict) {
		t.Fatalf("stale CAS err=%v", err)
	}
}

func TestPolicyConcurrentWritersHaveOneWinner(t *testing.T) {
	start := time.Unix(72_000, 0)
	controller := testControllerWithObservation(t, testObservation(start, 1, 0, 0, 0))
	const writers = 16
	results := make(chan error, writers)
	begin := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func(window int64) {
			defer group.Done()
			<-begin
			_, err := controller.UpdatePolicy(PolicyUpdate{
				ExpectedRevision:  1,
				WindowConcurrency: &window,
				UpdatedAt:         start.Add(time.Second),
			})
			results <- err
		}(int64(33 + index))
	}
	close(begin)
	group.Wait()
	close(results)
	var winners, conflicts int
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrPolicyRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected writer error=%v", err)
		}
	}
	if winners != 1 || conflicts != writers-1 {
		t.Fatalf("winners/conflicts=%d/%d", winners, conflicts)
	}
}

func TestPolicyUpdatePreservesReservationLifecycle(t *testing.T) {
	start := time.Unix(73_000, 0)
	controller := testControllerWithObservation(t, testObservation(start, 0, 0, 100, 0))
	result := controller.Admit(start.Add(time.Millisecond), testDemand(3))
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() {
		t.Fatalf("admission=%+v", result.Decision)
	}
	window := int64(48)
	update, err := controller.UpdatePolicy(PolicyUpdate{
		ExpectedRevision:  1,
		WindowConcurrency: &window,
		UpdatedAt:         start.Add(2 * time.Millisecond),
	})
	if err != nil || update.TPSWindowReset {
		t.Fatalf("policy update=%+v err=%v", update, err)
	}
	after := controller.Snapshot(start.Add(3 * time.Millisecond))
	if after.State.LiveReservations != 1 || after.State.UnobservedSequences != 3 ||
		after.State.SequenceLiabilities != 3 {
		t.Fatalf("policy update changed reservation=%+v", after.State)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("post-update terminal failed")
	}
}

func floatPointer(value float64) *float64 { return &value }
