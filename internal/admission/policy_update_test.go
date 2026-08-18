package admission

import (
	"errors"
	"math"
	"sync"
	"testing"
	"time"
)

func TestV01215TPSPolicyUpdateResetsEvidenceAndExcludesPreRevisionSample(t *testing.T) {
	start := time.Unix(12_000, 0)
	clock := &manualAdmissionClock{at: start}
	capability := testCapability()
	controller, err := NewAdmissionController(ControllerConfig{
		Capability: capability, WorkProfile: testRequestWorkProfile(),
		TPS: TPSPolicyConfig{Reference: 20}, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(capability, start, 0, 2, 0, 0, 0))
	for step := 1; step <= 4; step++ {
		at := start.Add(time.Duration(step) * 2 * time.Second)
		clock.Set(at)
		publishObservation(t, controller, testObservation(capability, at, 0, 2, 0, uint64(step*80), 0))
	}
	before := controller.Snapshot(clock.Now())
	if !before.State.TPS.Ready || before.State.TPS.Reference != 20 ||
		before.Policy.Revision != 1 || before.Policy.Reference != 20 {
		t.Fatalf("pre-update state=%+v", before)
	}

	straddling, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("start straddling sample")
	}
	updatedAt := start.Add(8250 * time.Millisecond)
	clock.Set(updatedAt)
	update, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1,
		Reference:        25,
		UpdatedAt:        updatedAt,
	})
	if err != nil {
		t.Fatalf("update policy: %v", err)
	}
	if update.PreviousReference != 20 || !update.WindowReset ||
		update.Policy.Revision != 2 || update.Policy.Reference != 25 ||
		!update.Policy.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("update=%+v", update)
	}

	clock.Set(start.Add(8500 * time.Millisecond))
	publication := controller.PublishObservation(straddling, testObservation(
		capability, clock.Now(), 0, 2, 0, 180, 0,
	))
	if !publication.Accepted {
		t.Fatalf("straddling publication=%+v", publication)
	}
	afterStraddling := controller.Snapshot(clock.Now())
	if afterStraddling.State.TPS.Reference != 25 || afterStraddling.State.TPS.Ready ||
		afterStraddling.State.TPS.QualifiedSamples != 0 ||
		afterStraddling.State.TPS.QualifiedSequenceSeconds != 0 {
		t.Fatalf("pre-revision sample refilled new window: %+v", afterStraddling.State.TPS)
	}

	clock.Set(start.Add(9 * time.Second))
	publishObservation(t, controller, testObservation(capability, clock.Now(), 0, 2, 0, 200, 0))
	afterFresh := controller.Snapshot(clock.Now())
	if afterFresh.State.TPS.Reference != 25 || afterFresh.State.TPS.QualifiedSamples != 1 ||
		afterFresh.State.TPS.QualifiedSequenceSamples != 1 {
		t.Fatalf("first post-revision sample=%+v", afterFresh.State.TPS)
	}
}

func TestV01215TPSPolicyUpdateIsCASValidatedAndEqualWriteDoesNotResetEvidence(t *testing.T) {
	start := time.Unix(12_100, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(
		t, capability, 20, testObservation(capability, start, 0, 2, 0, 0, 0),
	)
	publishObservation(t, controller, testObservation(capability, start.Add(time.Second), 0, 2, 0, 40, 0))

	invalid, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1, Reference: math.NaN(), UpdatedAt: start.Add(time.Second),
	})
	if !errors.Is(err, ErrTPSPolicyInvalid) || invalid.Policy.Revision != 1 ||
		controller.Snapshot(start.Add(time.Second)).State.TPS.QualifiedSamples != 1 {
		t.Fatalf("invalid update=%+v err=%v", invalid, err)
	}

	first, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1, Reference: 20, UpdatedAt: start.Add(2 * time.Second),
	})
	if err != nil || first.Policy.Revision != 2 || first.WindowReset ||
		controller.Snapshot(start.Add(2*time.Second)).State.TPS.QualifiedSamples != 1 {
		t.Fatalf("equal update=%+v err=%v", first, err)
	}
	conflict, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1, Reference: 30, UpdatedAt: start.Add(3 * time.Second),
	})
	if !errors.Is(err, ErrTPSPolicyRevisionConflict) || conflict.Policy.Revision != 2 ||
		conflict.Policy.Reference != 20 {
		t.Fatalf("conflict update=%+v err=%v", conflict, err)
	}
}

func TestV01215TPSPolicyConcurrentWritersHaveOneWinner(t *testing.T) {
	start := time.Unix(12_200, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(
		t, capability, 20, testObservation(capability, start, 0, 2, 0, 0, 0),
	)

	var wait sync.WaitGroup
	wait.Add(2)
	errorsSeen := make(chan error, 2)
	for _, reference := range []float64{25, 30} {
		reference := reference
		go func() {
			defer wait.Done()
			_, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
				ExpectedRevision: 1, Reference: reference, UpdatedAt: start.Add(time.Second),
			})
			errorsSeen <- err
		}()
	}
	wait.Wait()
	close(errorsSeen)
	var applied, conflicts int
	for err := range errorsSeen {
		switch {
		case err == nil:
			applied++
		case errors.Is(err, ErrTPSPolicyRevisionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected update error: %v", err)
		}
	}
	policy := controller.Snapshot(start.Add(time.Second)).Policy
	if applied != 1 || conflicts != 1 || policy.Revision != 2 ||
		(policy.Reference != 25 && policy.Reference != 30) {
		t.Fatalf("applied=%d conflicts=%d policy=%+v", applied, conflicts, policy)
	}
}

func TestV01215TPSPolicyUpdatePreservesQoSBudgetLeaseLifecycle(t *testing.T) {
	start := time.Unix(12_300, 0)
	clock := &manualAdmissionClock{at: start}
	capability := testCapability()
	controller, err := NewAdmissionController(ControllerConfig{
		Capability: capability, WorkProfile: testRequestWorkProfile(),
		TPS: TPSPolicyConfig{Reference: 20}, Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(capability, start, 0, 7, 0, 0, 0))
	for step := 1; step <= 4; step++ {
		at := start.Add(time.Duration(step) * 500 * time.Millisecond)
		clock.Set(at)
		publishObservation(t, controller, testObservation(capability, at, 0, 7, 0, uint64(step*75), 0))
	}
	estimate := testEstimate(1, 1, 16)
	estimate.OutputLimitTokens = 16
	estimate.OutputLimitKnown = true
	admitted := controller.Admit(clock.Now(), estimate)
	if !admitted.Decision.Admitted() || !admitted.Decision.TPSQoSBudgeted ||
		!admitted.Handle.MarkForwarded() {
		t.Fatalf("QoS-budget fixture=%+v", admitted.Decision)
	}
	coveredAt := start.Add(2500 * time.Millisecond)
	clock.Set(coveredAt)
	publishObservation(t, controller, testObservation(capability, coveredAt, 0, 8, 0, 375, 0))

	update, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1, Reference: 25, UpdatedAt: clock.Now(),
	})
	if err != nil || !update.WindowReset {
		t.Fatalf("policy update=%+v err=%v", update, err)
	}
	state := controller.Snapshot(clock.Now()).State
	if state.QoSBudgetLeases != 1 || state.LiveReservations != 1 ||
		state.TPS.Reference != 25 || state.TPS.QualifiedSamples != 0 {
		t.Fatalf("policy update lost live lifecycle state: %+v", state)
	}
	if !admitted.Handle.Terminate(TerminalSuccess) {
		t.Fatal("pre-update handle did not terminate after policy update")
	}
	terminal := controller.Snapshot(clock.Now()).State
	if terminal.QoSBudgetLeases != 0 || terminal.LiveReservations != 0 || terminal.ResidualDebts != 0 {
		t.Fatalf("terminal policy-crossing lifecycle=%+v", terminal)
	}
}
