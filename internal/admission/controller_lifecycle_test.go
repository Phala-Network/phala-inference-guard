package admission

import (
	"testing"
	"time"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestControllerInFlightSampleCannotEraseLaterTerminalDebt(t *testing.T) {
	now := time.Unix(8_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 10, 0))
	result := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() {
		t.Fatalf("admission=%+v", result.Decision)
	}
	inFlight, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("sample window unavailable")
	}
	if !result.Handle.Terminate(TerminalSuccess) {
		t.Fatal("terminal transition failed")
	}
	first := controller.PublishObservation(inFlight, testObservation(capability, now.Add(2*time.Millisecond), 0, 0, 0, 11, 0))
	if !first.Accepted {
		t.Fatalf("first publication=%+v", first)
	}
	stillReserved := controller.Snapshot(now.Add(3 * time.Millisecond))
	if stillReserved.State.ResidualDebts != 1 ||
		stillReserved.State.ReservationKVTokens != result.Decision.Work.TotalKVTokens {
		t.Fatalf("in-flight sample erased later terminal: %+v", stillReserved.State)
	}
	publishObservation(t, controller, testObservation(capability, now.Add(4*time.Millisecond), 0, 0, 0, 12, 0))
	released := controller.Snapshot(now.Add(5 * time.Millisecond))
	if released.State.ResidualDebts != 0 || released.State.ReservationKVTokens != 0 {
		t.Fatalf("covering sample did not release debt: %+v", released.State)
	}
}

func TestControllerLifecycleIsMonotonicIdempotentAndAggregateExact(t *testing.T) {
	now := time.Unix(9_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testEstimate(2_048, 3_072, 256))
	if !result.Decision.Admitted() {
		t.Fatalf("admission=%+v", result.Decision)
	}
	assertAggregateMatchesSlow(t, controller)
	if !result.Handle.MarkForwarded() || result.Handle.MarkForwarded() {
		t.Fatal("forward transition was not exactly once")
	}
	assertAggregateMatchesSlow(t, controller)
	if !result.Handle.MarkFirstByte() || result.Handle.MarkFirstByte() || result.Handle.MarkForwarded() {
		t.Fatal("first-byte transition was not monotonic")
	}
	assertAggregateMatchesSlow(t, controller)
	if !result.Handle.Terminate(TerminalError) || result.Handle.Terminate(TerminalSuccess) || result.Handle.MarkFirstByte() {
		t.Fatal("terminal transition was not exactly once")
	}
	assertAggregateMatchesSlow(t, controller)
	publishObservation(t, controller, testObservation(capability, now.Add(2*time.Millisecond), 0, 0, 0, 2, 0))
	assertAggregateMatchesSlow(t, controller)
}

func TestControllerTerminalBeforeForwardReleasesImmediately(t *testing.T) {
	now := time.Unix(10_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !result.Decision.Admitted() || !result.Handle.Terminate(TerminalCancel) {
		t.Fatalf("admission=%+v", result.Decision)
	}
	snapshot := controller.Snapshot(now.Add(2 * time.Millisecond))
	if snapshot.State.LiveReservations != 0 || snapshot.State.ResidualDebts != 0 ||
		snapshot.State.ReservationKVTokens != 0 {
		t.Fatalf("pre-forward terminal leaked reservation: %+v", snapshot.State)
	}
	assertAggregateMatchesSlow(t, controller)
}

func TestControllerCoveredTerminalReliesOnObservedKVWithoutResidual(t *testing.T) {
	now := time.Unix(11_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() || !result.Handle.MarkFirstByte() {
		t.Fatalf("admission=%+v", result.Decision)
	}
	publishObservation(t, controller, testObservation(capability, now.Add(2*time.Millisecond), result.Decision.Work.InputKVTokens, 1, 0, 2, 0))
	if !result.Handle.Terminate(TerminalSuccess) {
		t.Fatal("covered terminal failed")
	}
	snapshot := controller.Snapshot(now.Add(3 * time.Millisecond))
	if snapshot.State.ObservedKVTokens != result.Decision.Work.InputKVTokens ||
		snapshot.State.ReservationKVTokens != 0 || snapshot.State.ResidualDebts != 0 {
		t.Fatalf("covered terminal state=%+v", snapshot.State)
	}
	assertAggregateMatchesSlow(t, controller)
}

func TestControllerPreemptionContentionExpiresWithNextSample(t *testing.T) {
	now := time.Unix(12_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 100, 5))
	publishObservation(t, controller, testObservation(capability, now.Add(time.Millisecond), 0, 0, 0, 110, 6))
	weighted := testEstimate(96*1024, 144*1024, 256)
	protected := controller.Admit(now.Add(2*time.Millisecond), weighted).Decision
	if protected.Reason != ReasonPrefillContention || protected.Scope != ProtectionRequest {
		t.Fatalf("fresh preemption decision=%+v", protected)
	}
	publishObservation(t, controller, testObservation(capability, now.Add(3*time.Millisecond), 0, 0, 0, 120, 6))
	admitted := controller.Admit(now.Add(4*time.Millisecond), weighted).Decision
	if !admitted.Admitted() {
		t.Fatalf("expired preemption still protected=%+v", admitted)
	}
}

func TestV01215ControllerReservesAndReleasesMultiSequenceDemandAtomically(t *testing.T) {
	now := time.Unix(13_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
	estimate := testEstimate(128, 192, 256)
	estimate.DecodeSequences = 4

	result := controller.Admit(now.Add(time.Millisecond), estimate)
	if !result.Decision.Admitted() || result.Decision.TPSPostAdmitSequences != 0 {
		t.Fatalf("multi-sequence admission=%+v", result.Decision)
	}
	reserved := controller.Snapshot(now.Add(2 * time.Millisecond)).State
	if reserved.PendingPrefillSequences != 4 || reserved.UnobservedSequences != 4 ||
		reserved.SequenceLiabilities != 4 || reserved.LiveReservations != 1 ||
		reserved.ReservationKVTokens != result.Decision.Work.TotalKVTokens {
		t.Fatalf("multi-sequence reserved state=%+v", reserved)
	}
	if !result.Handle.MarkForwarded() {
		t.Fatal("multi-sequence lifecycle did not enter forwarded Prefill")
	}
	publishObservation(t, controller, testObservation(capability, now.Add(2*time.Millisecond), 0, 1, 0, 2, 0))
	observedPrefill := controller.Snapshot(now.Add(3 * time.Millisecond)).State
	current, _, valid := projectedTPSSequences(observedPrefill, 1)
	if !valid || observedPrefill.PendingPrefillSequences != 4 ||
		observedPrefill.UnobservedSequences != 0 || current != 4 {
		t.Fatalf("partially materialized multi-sequence Prefill state=%+v current=%d/%t", observedPrefill, current, valid)
	}
	if !result.Handle.MarkFirstByte() {
		t.Fatal("multi-sequence lifecycle did not enter Decode")
	}
	decode := controller.Snapshot(now.Add(4 * time.Millisecond)).State
	if decode.PendingPrefillSequences != 3 || decode.PendingPrefillTokens == 0 ||
		decode.LocalActiveDecode != 1 ||
		decode.UnobservedSequences != 0 || decode.SequenceLiabilities != 4 {
		t.Fatalf("multi-sequence Decode state=%+v", decode)
	}
	if !result.Handle.Terminate(TerminalSuccess) {
		t.Fatal("multi-sequence lifecycle did not terminate")
	}
	terminal := controller.Snapshot(now.Add(5 * time.Millisecond)).State
	if terminal.LiveReservations != 0 || terminal.ResidualDebts != 1 ||
		terminal.UnobservedSequences != 0 || terminal.SequenceLiabilities != 4 {
		t.Fatalf("multi-sequence terminal state=%+v", terminal)
	}
	publishObservation(t, controller, testObservation(capability, now.Add(time.Second), 0, 0, 0, 3, 0))
	released := controller.Snapshot(now.Add(time.Second + time.Millisecond)).State
	if released.SequenceLiabilities != 0 || released.ResidualDebts != 0 ||
		released.ReservationKVTokens != 0 {
		t.Fatalf("multi-sequence release state=%+v", released)
	}
}

func TestV01215VLLMFirstByteRetainsOtherChildPrefillAndInputKV(t *testing.T) {
	now := time.Unix(13_500, 0)
	capability := testCapability()
	controller, err := NewAdmissionController(ControllerConfig{
		Capability: capability,
		WorkProfile: predictive.BackendExecutionProfile{
			PrefillExecution:  predictive.PrefillExecutionIndependentSequences,
			InputKVSharing:    predictive.InputKVSharingIndependentSequences,
			FirstByteCoverage: predictive.FirstByteCoverageOneSequence,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(capability, now, 0, 0, 0, 1, 0))
	estimate := testEstimate(63, 63, 1)
	estimate.DecodeSequences = 2

	result := controller.Admit(now.Add(time.Millisecond), estimate)
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() ||
		!result.Handle.MarkFirstByte() {
		t.Fatalf("vLLM parallel lifecycle=%+v", result.Decision)
	}
	firstByte := controller.Snapshot(now.Add(2 * time.Millisecond)).State
	if firstByte.PendingPrefillSequences == 0 || firstByte.PendingPrefillTokens == 0 ||
		firstByte.ReservationKVTokens != result.Decision.Work.TotalKVTokens {
		t.Fatalf("first byte released other vLLM child work: state=%+v work=%+v", firstByte, result.Decision.Work)
	}

	publishObservation(t, controller, testObservation(
		capability,
		now.Add(3*time.Millisecond),
		capability.KVBlockSize,
		1,
		0,
		2,
		0,
	))
	covered := controller.Snapshot(now.Add(4 * time.Millisecond)).State
	if covered.PendingPrefillSequences == 0 || covered.PendingPrefillTokens == 0 ||
		covered.ReservationKVTokens <= result.Decision.Work.FutureKVTokens {
		t.Fatalf("one materialized child covered all vLLM liabilities: state=%+v work=%+v", covered, result.Decision.Work)
	}
}

func TestV01215ForwardedCancellationRetainsPrefillDebtUntilCoveringPoll(t *testing.T) {
	now := time.Unix(13_750, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, 0, 0, 0, 1, 0),
	)
	estimate := testEstimate(300*1024, 300*1024, 256)

	result := controller.Admit(now.Add(time.Millisecond), estimate)
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() ||
		!result.Handle.Terminate(TerminalCancel) {
		t.Fatalf("forwarded cancellation lifecycle=%+v", result.Decision)
	}
	debt := controller.Snapshot(now.Add(2 * time.Millisecond)).State
	if debt.ResidualDebts != 1 ||
		debt.PendingPrefillInputTokens != result.Decision.Work.PrefillInputTokens ||
		debt.PendingPrefillTokens != result.Decision.Work.PrefillComputeTokens ||
		debt.PendingPrefillSequences != result.Decision.Work.Estimate.DecodeSequences ||
		debt.PendingExclusiveSequences != 1 || debt.UnobservedSequences != 1 {
		t.Fatalf("forwarded cancellation dropped Prefill debt: state=%+v work=%+v", debt, result.Decision.Work)
	}

	protected := controller.Admit(
		now.Add(3*time.Millisecond),
		testEstimate(1, 1, capability.MinimumDecodeHorizonTokens),
	).Decision
	if protected.Admitted() || protected.Reason != ReasonPrefillExclusive {
		t.Fatalf("forwarded cancellation admitted through Prefill debt: %+v", protected)
	}

	publishObservation(t, controller, testObservation(capability, now.Add(4*time.Millisecond), 0, 0, 0, 2, 0))
	reopened := controller.Admit(
		now.Add(5*time.Millisecond),
		testEstimate(1, 1, capability.MinimumDecodeHorizonTokens),
	)
	if !reopened.Decision.Admitted() || reopened.Decision.State.ResidualDebts != 0 ||
		reopened.Decision.State.PendingPrefillSequences != 0 {
		t.Fatalf("covering poll did not release forwarded cancellation debt: %+v", reopened.Decision)
	}
	_ = reopened.Handle.Terminate(TerminalCancel)
}

func TestV01215DecodeCancellationRetainsRemainingExecutionDebtUntilCoveringPoll(t *testing.T) {
	now := time.Unix(13_875, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, 0, 0, 0, 1, 0),
	)
	estimate := testEstimate(63, 63, 1)
	estimate.DecodeSequences = 2

	result := controller.Admit(now.Add(time.Millisecond), estimate)
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() ||
		!result.Handle.MarkFirstByte() || !result.Handle.Terminate(TerminalDisconnect) {
		t.Fatalf("Decode cancellation lifecycle=%+v", result.Decision)
	}
	debt := controller.Snapshot(now.Add(2 * time.Millisecond)).State
	if debt.ResidualDebts != 1 ||
		debt.PendingPrefillInputTokens != result.Decision.Work.FirstBytePendingPrefillInputTokens ||
		debt.PendingPrefillTokens != result.Decision.Work.FirstBytePendingPrefillComputeTokens ||
		debt.PendingPrefillSequences != result.Decision.Work.FirstBytePendingPrefillSequences ||
		debt.LocalActiveDecode != 1 || debt.UnobservedSequences != 2 {
		t.Fatalf("Decode cancellation dropped execution debt: state=%+v work=%+v", debt, result.Decision.Work)
	}

	publishObservation(t, controller, testObservation(capability, now.Add(3*time.Millisecond), 0, 0, 0, 2, 0))
	released := controller.Snapshot(now.Add(4 * time.Millisecond)).State
	if released.ResidualDebts != 0 || released.PendingPrefillSequences != 0 ||
		released.LocalActiveDecode != 0 || released.UnobservedSequences != 0 {
		t.Fatalf("covering poll did not release Decode cancellation debt: %+v", released)
	}
}
