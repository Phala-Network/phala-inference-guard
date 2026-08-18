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
	terminalDebt := controller.Snapshot(clock.Now()).State
	if terminalDebt.QoSBudgetLeases != 1 || terminalDebt.LiveReservations != 0 ||
		terminalDebt.ResidualDebts != 1 {
		t.Fatalf("terminal policy-crossing debt=%+v", terminalDebt)
	}
	clock.Set(start.Add(3 * time.Second))
	publishObservation(t, controller, testObservation(capability, clock.Now(), 0, 7, 0, 450, 0))
	terminal := controller.Snapshot(clock.Now()).State
	if terminal.QoSBudgetLeases != 0 || terminal.LiveReservations != 0 || terminal.ResidualDebts != 0 {
		t.Fatalf("covered terminal policy-crossing lifecycle=%+v", terminal)
	}
}

func TestV01215TPSPolicyUpdatePreservesEveryReservationPhase(t *testing.T) {
	tests := []struct {
		name         string
		advance      func(t *testing.T, handle ReservationHandle)
		terminal     TerminalCause
		wantResidual int64
	}{
		{
			name:     "reserved cancel",
			advance:  func(*testing.T, ReservationHandle) {},
			terminal: TerminalCancel,
		},
		{
			name: "forwarded prefill cancel",
			advance: func(t *testing.T, handle ReservationHandle) {
				t.Helper()
				if !handle.MarkForwarded() {
					t.Fatal("reservation did not reach forwarded Prefill")
				}
			},
			terminal:     TerminalCancel,
			wantResidual: 1,
		},
		{
			name: "active decode timeout",
			advance: func(t *testing.T, handle ReservationHandle) {
				t.Helper()
				if !handle.MarkForwarded() || !handle.MarkFirstByte() {
					t.Fatal("reservation did not reach active Decode")
				}
			},
			terminal:     TerminalTimeout,
			wantResidual: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			start := time.Unix(12_400, 0)
			capability := testCapability()
			controller := testControllerWithTPSObservation(
				t, capability, 0, testObservation(capability, start, 0, 0, 0, 100, 0),
			)
			admitted := controller.Admit(start.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
			if !admitted.Decision.Admitted() {
				t.Fatalf("admission=%+v", admitted.Decision)
			}
			test.advance(t, admitted.Handle)

			updatedAt := start.Add(2 * time.Millisecond)
			before := controller.Snapshot(updatedAt).State
			update, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
				ExpectedRevision: 1, Reference: 25, UpdatedAt: updatedAt,
			})
			if err != nil || !update.WindowReset || update.Policy.Revision != 2 {
				t.Fatalf("policy update=%+v err=%v", update, err)
			}
			afterSnapshot := controller.Snapshot(updatedAt)
			after := afterSnapshot.State
			before.TPS = TPSSnapshot{}
			after.TPS = TPSSnapshot{}
			if before != after || afterSnapshot.Policy.Revision != 2 || afterSnapshot.Policy.Reference != 25 {
				t.Fatalf("policy update changed reservation phase: before=%+v after=%+v policy=%+v", before, after, afterSnapshot.Policy)
			}

			if !admitted.Handle.Terminate(test.terminal) || admitted.Handle.Terminate(test.terminal) {
				t.Fatal("terminal event was not exact-once")
			}
			terminal := controller.Snapshot(updatedAt).State
			if terminal.LiveReservations != 0 || terminal.ResidualDebts != test.wantResidual {
				t.Fatalf("terminal state=%+v want_residual=%d", terminal, test.wantResidual)
			}
			publishObservation(t, controller, testObservation(
				capability, start.Add(3*time.Millisecond), 0, 0, 0, 100, 0,
			))
			covered := controller.Snapshot(start.Add(3 * time.Millisecond)).State
			if covered.LiveReservations != 0 || covered.ResidualDebts != 0 ||
				covered.ReservationKVTokens != 0 || covered.SequenceLiabilities != 0 {
				t.Fatalf("covering observation retained lifecycle debt: %+v", covered)
			}
		})
	}
}

func TestV01215TPSPolicyUpdateThenBackendResetFencesOldHandle(t *testing.T) {
	start := time.Unix(12_500, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(
		t, capability, 0, testObservation(capability, start, 0, 0, 0, 100, 0),
	)
	admitted := controller.Admit(start.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !admitted.Decision.Admitted() || !admitted.Handle.MarkForwarded() {
		t.Fatalf("admission=%+v", admitted.Decision)
	}
	update, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1, Reference: 25, UpdatedAt: start.Add(2 * time.Millisecond),
	})
	if err != nil || update.Policy.Revision != 2 {
		t.Fatalf("policy update=%+v err=%v", update, err)
	}
	reset := publishObservation(t, controller, testObservation(
		capability, start.Add(3*time.Millisecond), 0, 0, 0, 1, 0,
	))
	if !reset.RuntimeReset || admitted.Handle.MarkFirstByte() || admitted.Handle.Terminate(TerminalSuccess) {
		t.Fatalf("runtime reset did not fence pre-update handle: %+v", reset)
	}
	snapshot := controller.Snapshot(start.Add(3 * time.Millisecond))
	if snapshot.State.LiveReservations != 0 || snapshot.State.ResidualDebts != 0 ||
		snapshot.State.ReservationKVTokens != 0 || snapshot.Policy.Revision != 2 ||
		snapshot.Policy.Reference != 25 {
		t.Fatalf("post-reset policy/lifecycle=%+v/%+v", snapshot.Policy, snapshot.State)
	}
}

func TestV01215TPSPolicyUpdateThenCloseClearsStateAndFencesOldHandle(t *testing.T) {
	start := time.Unix(12_600, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(
		t, capability, 0, testObservation(capability, start, 0, 0, 0, 100, 0),
	)
	admitted := controller.Admit(start.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !admitted.Decision.Admitted() || !admitted.Handle.MarkForwarded() {
		t.Fatalf("admission=%+v", admitted.Decision)
	}
	if _, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 1, Reference: 25, UpdatedAt: start.Add(2 * time.Millisecond),
	}); err != nil {
		t.Fatalf("policy update: %v", err)
	}
	controller.Close()
	if admitted.Handle.MarkFirstByte() || admitted.Handle.Terminate(TerminalShutdown) {
		t.Fatal("closed Controller accepted pre-update handle")
	}
	if _, err := controller.UpdateTPSPolicy(TPSPolicyUpdate{
		ExpectedRevision: 2, Reference: 30, UpdatedAt: start.Add(3 * time.Millisecond),
	}); !errors.Is(err, ErrTPSPolicyUnavailable) {
		t.Fatalf("closed policy update error=%v", err)
	}
	snapshot := controller.Snapshot(start.Add(3 * time.Millisecond))
	if snapshot.MinimumDecision.Reason != ReasonClosed || snapshot.State.LiveReservations != 0 ||
		snapshot.State.ResidualDebts != 0 || snapshot.State.ReservationKVTokens != 0 {
		t.Fatalf("closed policy/lifecycle=%+v/%+v", snapshot.Policy, snapshot.State)
	}
}
