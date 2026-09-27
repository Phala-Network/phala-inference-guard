package admission

import (
	"math"
	"sync"
	"testing"
	"time"
)

func TestControllerRejectsInvalidConfiguration(t *testing.T) {
	if _, err := NewAdmissionController(ControllerConfig{}); err == nil {
		t.Fatal("empty runtime identity constructed a Controller")
	}
	for _, reference := range []float64{-1, math.NaN(), math.Inf(1), 1_000_000.001} {
		if _, err := NewAdmissionController(ControllerConfig{
			RuntimeIdentity: testRuntimeIdentity,
			TPS:             TPSPolicyConfig{Reference: reference},
		}); err == nil {
			t.Fatalf("invalid TPS reference %v constructed a Controller", reference)
		}
	}
	for _, config := range []ControllerConfig{
		{RuntimeIdentity: testRuntimeIdentity, WindowConcurrency: -1},
		{RuntimeIdentity: testRuntimeIdentity, WindowConcurrency: maximumTPSReservations + 1},
		{RuntimeIdentity: testRuntimeIdentity, RunningLimit: -1},
		{
			RuntimeIdentity: testRuntimeIdentity, RunningLimit: maximumTPSReservations + 1,
			RunningLimitSource: RunningLimitSourceEnvironment,
		},
	} {
		if _, err := NewAdmissionController(config); err == nil {
			t.Fatalf("invalid admission bounds constructed a Controller: %+v", config)
		}
	}
}

func TestControllerTPSReferenceChangesPreForwardDecision(t *testing.T) {
	now := time.Unix(9_000, 0)
	strict := testControllerWithTPSObservation(t, 25, testObservation(now, 5, 0, 0, 0))
	permissive := testControllerWithTPSObservation(t, 15, testObservation(now, 5, 0, 0, 0))
	for step := 1; step <= 4; step++ {
		observation := testObservation(
			now.Add(time.Duration(step)*time.Second),
			5,
			0,
			uint64(step*100),
			0,
		)
		publishObservation(t, strict, observation)
		publishObservation(t, permissive, observation)
	}

	strictDecision := strict.Admit(now.Add(4*time.Second+time.Millisecond), testDemand(1)).Decision
	permissiveDecision := permissive.Admit(now.Add(4*time.Second+time.Millisecond), testDemand(1)).Decision
	if strictDecision.Reason != ReasonTPSReference ||
		strictDecision.TPSDecisionSubreason != TPSDecisionSubreasonBelowReference ||
		strictDecision.ReservationID != 0 {
		t.Fatalf("strict TPS decision=%+v", strictDecision)
	}
	if !permissiveDecision.Admitted() ||
		permissiveDecision.TPSDecisionSubreason != TPSDecisionSubreasonHealthyWindow ||
		permissiveDecision.ProjectedRunning != 6 {
		t.Fatalf("permissive TPS decision=%+v", permissiveDecision)
	}
}

func TestControllerPremiumUsesTPSOnlyAndKeepsReservationAccounting(t *testing.T) {
	now := time.Unix(9_250, 0)
	controller := testControllerWithBounds(t, ControllerConfig{
		RuntimeIdentity:    testRuntimeIdentity,
		TPS:                TPSPolicyConfig{Reference: 25},
		WindowConcurrency:  1,
		RunningLimit:       1,
		RunningLimitSource: RunningLimitSourceAdmin,
	}, testObservation(now, 1, 1, 0, 0))

	demand := testDemand(1).WithPriority(RequestPriorityPremium)
	result := controller.Admit(now.Add(time.Millisecond), demand)
	if !result.Decision.Admitted() || result.Decision.Demand.Priority != RequestPriorityPremium ||
		result.Decision.ProjectedRunning != 2 || result.Decision.ProjectedWindowSequences != 1 {
		t.Fatalf("premium was blocked by a non-TPS gate: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("premium reservation did not terminate")
	}
	state := controller.Snapshot(now.Add(time.Millisecond)).State
	if state.LiveReservations != 0 || state.ResidualDebts != 0 {
		t.Fatalf("premium reservation accounting leaked: %+v", state)
	}
}

func TestControllerPremiumStillRespectsTPSReference(t *testing.T) {
	now := time.Unix(9_350, 0)
	controller := testControllerWithTPSObservation(t, 25, testObservation(now, 0, 0, 0, 0))
	for step := 1; step <= 4; step++ {
		publishObservation(t, controller, testObservation(
			now.Add(time.Duration(step)*time.Second),
			2, 0, uint64(step*10), 0,
		))
	}
	result := controller.Admit(now.Add(4*time.Second+time.Millisecond),
		testDemand(1).WithPriority(RequestPriorityPremium))
	if result.Decision.Admitted() || result.Decision.Reason != ReasonTPSReference ||
		result.Decision.TPSDecisionSubreason != TPSDecisionSubreasonBelowReference {
		t.Fatalf("premium bypassed TPS reference: %+v", result.Decision)
	}
}

func TestControllerWarmingReservationsAreAtomic(t *testing.T) {
	now := time.Unix(9_500, 0)
	controller := testControllerWithTPSObservation(t, 20, testObservation(now, 0, 0, 0, 0))
	const callers = 32
	results := make(chan AdmissionResult, callers)
	start := make(chan struct{})
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- controller.Admit(now.Add(time.Millisecond), testDemand(1))
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var admittedSequences int64
	for result := range results {
		if result.Decision.Admitted() {
			admittedSequences += result.Decision.Demand.DecodeSequences
			if !result.Handle.Terminate(TerminalCancel) {
				t.Fatal("admitted reservation did not terminate")
			}
			continue
		}
		if result.Decision.Reason != ReasonWindowConcurrency || result.Decision.ReservationID != 0 {
			t.Fatalf("unexpected concurrent protection: %+v", result.Decision)
		}
	}
	if admittedSequences != DefaultWindowConcurrency {
		t.Fatalf("same observation admitted sequences=%d want=%d", admittedSequences, DefaultWindowConcurrency)
	}
}

func TestControllerReservesCompleteBatchMultiplicity(t *testing.T) {
	now := time.Unix(9_750, 0)
	controller := testControllerWithBounds(t, ControllerConfig{
		TPS:               TPSPolicyConfig{Reference: 20},
		WindowConcurrency: 2,
	}, testObservation(now, 0, 0, 0, 0))

	first := controller.Admit(now.Add(time.Millisecond), testDemand(2))
	if !first.Decision.Admitted() || first.Decision.ProjectedWindowSequences != 2 {
		t.Fatalf("batch admission=%+v", first.Decision)
	}
	second := controller.Admit(now.Add(time.Millisecond), testDemand(1)).Decision
	if second.Admitted() || second.Reason != ReasonWindowConcurrency ||
		second.ProjectedWindowSequences != 3 || second.ReservationID != 0 {
		t.Fatalf("batch reservation was not atomic: %+v", second)
	}
	if !first.Handle.Terminate(TerminalCancel) {
		t.Fatal("batch reservation rollback failed")
	}
}

func TestControllerCounterResetClearsWindowAndFencesHandles(t *testing.T) {
	now := time.Unix(10_000, 0)
	controller := testControllerWithTPSObservation(t, 20, testObservation(now, 0, 0, 100, 0))
	result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if !result.Decision.Admitted() {
		t.Fatalf("pre-reset admission=%+v", result.Decision)
	}
	for step := 1; step <= 4; step++ {
		publishObservation(t, controller, testObservation(
			now.Add(time.Duration(step)*time.Second),
			2,
			0,
			uint64(100+step*40),
			0,
		))
	}
	if before := controller.Snapshot(now.Add(4*time.Second + time.Millisecond)); !before.State.TPS.Ready {
		t.Fatalf("TPS window did not warm before reset: %+v", before.State.TPS)
	}
	oldEpoch := result.Decision.RuntimeEpoch

	reset := publishObservation(t, controller, testObservation(now.Add(5*time.Second), 0, 0, 1, 0))
	if !reset.RuntimeReset || reset.RuntimeEpoch == oldEpoch {
		t.Fatalf("counter reset publication=%+v old_epoch=%d", reset, oldEpoch)
	}
	if result.Handle.MarkForwarded() || result.Handle.Terminate(TerminalCancel) {
		t.Fatal("runtime reset accepted an old handle")
	}
	after := controller.Snapshot(now.Add(5*time.Second + time.Millisecond))
	if after.State.TPS.Ready ||
		after.State.TPS.QualifiedSamples != 0 ||
		after.State.SequenceLiabilities != 0 {
		t.Fatalf("runtime reset retained prior state: %+v", after.State)
	}
}

func TestControllerDecodeWorkerRestartPreservesInflightReservations(t *testing.T) {
	now := time.Unix(10_250, 0)
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity:    testRuntimeIdentity,
		PDDecode:           true,
		WindowConcurrency:  4,
		RunningLimit:       2,
		RunningLimitSource: RunningLimitSourceEnvironment,
	})
	if err != nil {
		t.Fatal(err)
	}
	initial := testObservation(now, 0, 0, 100, 0)
	initial.RuntimeStartTime = 1_000
	initial.RuntimeEpochIdentity = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	publishObservation(t, controller, initial)

	healthyDecodeRequest := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if !healthyDecodeRequest.Decision.Admitted() || !healthyDecodeRequest.Handle.MarkForwarded() ||
		!healthyDecodeRequest.Handle.MarkFirstByte() {
		t.Fatalf("healthy Decode reservation did not enter response lifecycle: %+v", healthyDecodeRequest.Decision)
	}
	pendingDecodeRequest := controller.Admit(now.Add(2*time.Millisecond), testDemand(1))
	if !pendingDecodeRequest.Decision.Admitted() || !pendingDecodeRequest.Handle.MarkForwarded() {
		t.Fatalf("pending Decode reservation did not forward: %+v", pendingDecodeRequest.Decision)
	}
	staleWindow, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("pre-reset sample window unavailable")
	}

	reset := testObservation(now.Add(time.Second), 1, 0, 1, 0)
	reset.RuntimeStartTime = 1_000
	reset.RuntimeEpochIdentity = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	publication := publishObservation(t, controller, reset)
	if !publication.RuntimeReset || publication.RuntimeEpoch == healthyDecodeRequest.Decision.RuntimeEpoch {
		t.Fatalf("partial Decode restart did not advance runtime epoch: %+v", publication)
	}
	if stale := controller.PublishObservation(staleWindow, reset); stale.Accepted || stale.Reason != ReasonObservationInvalid {
		t.Fatalf("pre-reset sample window crossed the new runtime epoch: %+v", stale)
	}

	after := controller.Snapshot(now.Add(time.Second + time.Millisecond))
	if after.State.RawRunning != 1 || after.State.UnobservedSequences != 1 || after.State.LiveReservations != 2 {
		t.Fatalf("partial Decode restart dropped in-flight reservation liability: %+v", after.State)
	}
	if after.MinimumDecision.Reason != ReasonRunningLimit || after.MinimumDecision.ProjectedRunning != 3 {
		t.Fatalf("partial Decode restart admitted against forgotten in-flight demand: %+v", after.MinimumDecision)
	}
	if !pendingDecodeRequest.Handle.MarkFirstByte() {
		t.Fatal("pre-reset in-flight handle could not publish its first byte after partial restart")
	}
	if !healthyDecodeRequest.Handle.Terminate(TerminalSuccess) || !pendingDecodeRequest.Handle.Terminate(TerminalSuccess) {
		t.Fatal("pre-reset in-flight handles could not terminate after partial restart")
	}
	assertAggregateMatchesSlow(t, controller)
}

func TestControllerRuntimeIdentityDriftFailsClosed(t *testing.T) {
	now := time.Unix(10_500, 0)
	controller := testControllerWithObservation(t, testObservation(now, 0, 0, 1, 0))
	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("sample window unavailable")
	}
	observation := testObservation(now.Add(time.Millisecond), 0, 0, 2, 0)
	observation.RuntimeIdentity = "other-runtime"
	publication := controller.PublishObservation(window, observation)
	if publication.Accepted ||
		!publication.RuntimeIdentityDrift ||
		publication.Reason != ReasonRuntimeIdentityDrift {
		t.Fatalf("identity drift publication=%+v", publication)
	}
	decision := controller.Admit(now.Add(2*time.Millisecond), testDemand(1)).Decision
	if decision.Reason != ReasonRuntimeIdentityDrift || decision.Scope != ProtectionAvailability {
		t.Fatalf("identity drift did not fail closed: %+v", decision)
	}
}

func TestControllerSnapshotIsOneCoherentObservation(t *testing.T) {
	now := time.Unix(11_000, 0)
	controller := testControllerWithObservation(t, testObservation(now, 3, 1, 100, 4))
	second := testObservation(now.Add(500*time.Millisecond), 5, 2, 170, 5)
	publication := publishObservation(t, controller, second)

	snapshot := controller.Snapshot(now.Add(600 * time.Millisecond))
	if snapshot.Observation != second ||
		snapshot.ObservationSequence != publication.ObservationSequence ||
		snapshot.State.RawRunning != 5 ||
		snapshot.State.RawWaiting != 2 ||
		snapshot.State.GenerationDelta != 70 ||
		snapshot.State.PreemptionDelta != 1 ||
		snapshot.State.PreviousRawRunning != 3 ||
		snapshot.State.PreviousRawWaiting != 1 ||
		snapshot.State.ObservationInterval != 500*time.Millisecond {
		t.Fatalf("incoherent snapshot=%+v", snapshot)
	}
}
