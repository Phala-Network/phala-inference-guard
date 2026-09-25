package admission

import (
	"testing"
	"time"
)

func TestPDTransferDebtUsesRunningLimitWithoutWaitingProtection(t *testing.T) {
	at := time.Unix(27_000, 0)
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:    testRuntimeIdentity,
		TPS:                TPSPolicyConfig{Reference: 1},
		WindowConcurrency:  32,
		RunningLimit:       128,
		RunningLimitSource: RunningLimitSourceAdmin,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	observation := testObservation(at, 1, 0, 1, 0)
	observation.DecodePending = 124
	publishObservation(t, controller, observation)
	first := controller.Admit(at.Add(time.Millisecond), testDemand(1))
	if !first.Decision.Admitted() || first.Decision.ProjectedRunning != 126 ||
		first.Decision.State.RawWaiting != 0 || first.Decision.State.RawDecodePending != 124 {
		t.Fatalf("transfer-only burst blocked or lost capacity debt: %+v", first.Decision)
	}
	if !first.Handle.Terminate(TerminalCancel) {
		t.Fatal("first reservation cleanup failed")
	}
	observation = testObservation(at.Add(500*time.Millisecond), 1, 0, 2, 0)
	observation.DecodePending = 126
	publishObservation(t, controller, observation)
	atLimit := controller.Admit(at.Add(501*time.Millisecond), testDemand(1))
	if !atLimit.Decision.Admitted() || atLimit.Decision.ProjectedRunning != 128 {
		t.Fatalf("128 debt should admit exactly one more: %+v", atLimit.Decision)
	}
	overLimit := controller.Admit(at.Add(502*time.Millisecond), testDemand(1)).Decision
	if overLimit.Admitted() || overLimit.Reason != ReasonRunningLimit || overLimit.ReservationID != 0 {
		t.Fatalf("PD transfer debt bypassed running limit: %+v", overLimit)
	}
	if !atLimit.Handle.Terminate(TerminalCancel) {
		t.Fatal("limit reservation cleanup failed")
	}
}

func TestPDTransferKeepsPendingFirstByteLeaseUntilTransferDrains(t *testing.T) {
	at := time.Unix(28_000, 0)
	clock := &manualAdmissionClock{at: at}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:               testRuntimeIdentity,
		WindowConcurrency:             1,
		PendingFirstByteLeaseDuration: time.Second,
		Now:                           clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	publishObservation(t, controller, testObservation(at, 0, 0, 1, 0))
	lease := controller.Admit(at.Add(time.Millisecond), testDemand(1))
	clock.Set(at.Add(time.Millisecond))
	if !lease.Decision.Admitted() || !lease.Handle.MarkForwarded() {
		t.Fatalf("lease setup failed: %+v", lease.Decision)
	}
	clock.Set(at.Add(2 * time.Second))
	transferring := testObservation(at.Add(2*time.Second), 0, 0, 2, 0)
	transferring.DecodePending = 1
	publishObservation(t, controller, transferring)
	underTransfer := controller.Admit(at.Add(2*time.Second+time.Millisecond), testDemand(1)).Decision
	if underTransfer.Admitted() || underTransfer.Reason != ReasonWindowConcurrency ||
		underTransfer.State.UnobservedSequences != 1 {
		t.Fatalf("transfer prematurely released first-byte lease: %+v", underTransfer)
	}
	clock.Set(at.Add(2500 * time.Millisecond))
	publishObservation(t, controller, testObservation(at.Add(2500*time.Millisecond), 0, 0, 3, 0))
	replacement := controller.Admit(at.Add(2501*time.Millisecond), testDemand(1))
	if !replacement.Decision.Admitted() || replacement.Decision.State.UnobservedSequences != 0 {
		t.Fatalf("drained transfer retained expired lease: %+v", replacement.Decision)
	}
	if !lease.Handle.Terminate(TerminalCancel) || !replacement.Handle.Terminate(TerminalCancel) {
		t.Fatal("lease cleanup failed")
	}
}
