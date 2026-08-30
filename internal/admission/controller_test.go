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
