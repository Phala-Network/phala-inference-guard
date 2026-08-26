package admission

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestTPSPolicyUpdateResetsWindowAndExcludesStraddlingSample(t *testing.T) {
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
	straddling, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("straddling sample unavailable")
	}
	updateAt := clock.Now().Add(100 * time.Millisecond)
	update, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1,
		Reference:        25,
		UpdatedAt:        updateAt,
	})
	if err != nil || !update.WindowReset || update.Policy.Revision != 2 {
		t.Fatalf("policy update=%+v err=%v", update, err)
	}
	clock.Set(clock.Now().Add(time.Second))
	publication := controller.PublishObservation(
		straddling,
		testObservation(clock.Now(), 2, 0, 200, 0),
	)
	if !publication.Accepted {
		t.Fatalf("straddling publication=%+v", publication)
	}
	snapshot := controller.Snapshot(clock.Now().Add(time.Millisecond))
	if snapshot.Policy.Reference != 25 ||
		snapshot.State.TPS.QualifiedSamples != 0 ||
		snapshot.State.TPS.QualifiedSequenceSeconds != 0 {
		t.Fatalf("pre-update evidence entered new window: %+v", snapshot)
	}
}

func TestTPSPolicyUpdateCASAndValidation(t *testing.T) {
	start := time.Unix(71_000, 0)
	controller := testControllerWithTPSObservation(t, 20, testObservation(start, 1, 0, 0, 0))
	publishObservation(t, controller, testObservation(start.Add(time.Second), 1, 0, 40, 0))

	for _, update := range []TPSPolicyUpdate{
		{ExpectedRevision: 0, Reference: 20, UpdatedAt: start.Add(2 * time.Second)},
		{ExpectedRevision: 1, Reference: -1, UpdatedAt: start.Add(2 * time.Second)},
		{ExpectedRevision: 1, Reference: 20},
	} {
		if _, err := controller.UpdateTPSPolicy(update); !errors.Is(err, ErrTPSPolicyInvalid) {
			t.Fatalf("invalid update=%+v err=%v", update, err)
		}
	}
	equal, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1,
		Reference:        20,
		UpdatedAt:        start.Add(2 * time.Second),
	})
	if err != nil || equal.WindowReset || equal.Policy.Revision != 2 {
		t.Fatalf("equal update=%+v err=%v", equal, err)
	}
	if _, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1,
		Reference:        25,
		UpdatedAt:        start.Add(3 * time.Second),
	}); !errors.Is(err, ErrTPSPolicyRevisionConflict) {
		t.Fatalf("stale CAS err=%v", err)
	}
	snapshot := controller.Snapshot(start.Add(1500 * time.Millisecond))
	if snapshot.State.TPS.QualifiedSamples != 1 {
		t.Fatalf("equal update reset evidence: %+v", snapshot.State.TPS)
	}
}

func TestTPSPolicyConcurrentWritersHaveOneWinner(t *testing.T) {
	start := time.Unix(72_000, 0)
	controller := testControllerWithTPSObservation(t, 20, testObservation(start, 1, 0, 0, 0))
	const writers = 16
	results := make(chan error, writers)
	begin := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < writers; index++ {
		group.Add(1)
		go func(reference float64) {
			defer group.Done()
			<-begin
			_, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
				ExpectedRevision: 1,
				Reference:        reference,
				UpdatedAt:        start.Add(time.Second),
			})
			results <- err
		}(float64(21 + index))
	}
	close(begin)
	group.Wait()
	close(results)
	var winners, conflicts int
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrTPSPolicyRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected writer error=%v", err)
		}
	}
	if winners != 1 || conflicts != writers-1 {
		t.Fatalf("winners/conflicts=%d/%d", winners, conflicts)
	}
}

func TestTPSPolicyUpdatePreservesReservationLifecycle(t *testing.T) {
	start := time.Unix(73_000, 0)
	clock := &manualAdmissionClock{at: start}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(start, 0, 0, 100, 0))
	result := controller.Admit(start.Add(time.Millisecond), testDemand(3))
	if !result.Decision.Admitted() {
		t.Fatalf("admission=%+v", result.Decision)
	}
	clock.Set(start.Add(time.Millisecond))
	if !result.Handle.MarkForwarded() {
		t.Fatal("forward failed")
	}
	update, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1,
		Reference:        25,
		UpdatedAt:        start.Add(2 * time.Millisecond),
	})
	if err != nil || !update.WindowReset {
		t.Fatalf("policy update=%+v err=%v", update, err)
	}
	afterUpdate := controller.Snapshot(start.Add(3 * time.Millisecond))
	if afterUpdate.State.LiveReservations != 1 ||
		afterUpdate.State.UnobservedSequences != 3 ||
		afterUpdate.State.SequenceLiabilities != 3 {
		t.Fatalf("policy update changed reservation: %+v", afterUpdate.State)
	}
	clock.Set(start.Add(3 * time.Millisecond))
	if !result.Handle.MarkFirstByte() ||
		!result.Handle.Terminate(TerminalError) {
		t.Fatal("post-update lifecycle failed")
	}
	publishObservation(t, controller, testObservation(start.Add(4*time.Millisecond), 0, 0, 101, 0))
	released := controller.Snapshot(start.Add(5 * time.Millisecond))
	if released.State.ResidualDebts != 0 ||
		released.State.SequenceLiabilities != 0 ||
		released.Policy.Revision != 2 {
		t.Fatalf("post-update release=%+v", released)
	}
}

func TestTPSPolicyUpdateThenRuntimeResetFencesOldHandle(t *testing.T) {
	start := time.Unix(74_000, 0)
	controller := testControllerWithTPSObservation(t, 0, testObservation(start, 0, 0, 100, 0))
	admitted := controller.Admit(start.Add(time.Millisecond), testDemand(1))
	if !admitted.Decision.Admitted() {
		t.Fatalf("admission=%+v", admitted.Decision)
	}
	if _, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1,
		Reference:        25,
		UpdatedAt:        start.Add(2 * time.Millisecond),
	}); err != nil {
		t.Fatal(err)
	}
	reset := publishObservation(t, controller, testObservation(start.Add(3*time.Millisecond), 0, 0, 1, 0))
	if !reset.RuntimeReset || admitted.Handle.MarkForwarded() || admitted.Handle.Terminate(TerminalCancel) {
		t.Fatalf("runtime reset did not fence old handle: %+v", reset)
	}
	snapshot := controller.Snapshot(start.Add(4 * time.Millisecond))
	if snapshot.State.SequenceLiabilities != 0 || snapshot.Policy.Revision != 2 {
		t.Fatalf("runtime reset state=%+v", snapshot)
	}
}
