package admission

import (
	"math"
	"sync"
	"testing"
	"time"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestControllerRetainsCompletionBeforePollDebtUntilCoveringSample(t *testing.T) {
	now := time.Unix(1_000, 0)
	controller := testControllerWithObservation(t, testCapability(), testObservation(testCapability(), now, 0, 0, 0, 100, 2))
	estimate := testEstimate(1_024, 1_536, 256)
	result := controller.Admit(now.Add(time.Millisecond), estimate)
	if !result.Decision.Admitted() || result.Decision.ReservationID == 0 {
		t.Fatalf("admission=%+v handle=%+v", result.Decision, result.Handle)
	}
	if !result.Handle.MarkForwarded() || !result.Handle.Terminate(TerminalSuccess) {
		t.Fatal("forward/terminal transition failed")
	}
	before := controller.Snapshot(now.Add(2 * time.Millisecond))
	if before.State.ReservationKVTokens != result.Decision.Work.TotalKVTokens ||
		before.State.PendingPrefillTokens != 0 {
		t.Fatalf("completion debt state=%+v work=%+v", before.State, result.Decision.Work)
	}

	publishObservation(t, controller, testObservation(testCapability(), now.Add(3*time.Millisecond), 0, 0, 0, 101, 2))
	after := controller.Snapshot(now.Add(4 * time.Millisecond))
	if after.State.ReservationKVTokens != 0 || !after.Available {
		t.Fatalf("state after covering sample=%+v snapshot=%+v", after.State, after)
	}
}

func TestControllerRejectsInvalidTPSPolicyConfiguration(t *testing.T) {
	for _, reference := range []float64{-1, math.NaN(), math.Inf(1), 1_000_000.001} {
		if _, err := NewAdmissionController(ControllerConfig{
			Capability: testCapability(),
			TPS:        TPSPolicyConfig{Reference: reference},
		}); err == nil {
			t.Fatalf("invalid TPS reference %v constructed a Controller", reference)
		}
	}
}

func TestControllerCoveringFirstByteKeepsOnlyFutureKV(t *testing.T) {
	now := time.Unix(2_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 1_000, 0, 0, 100, 0))
	result := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !result.Decision.Admitted() || !result.Handle.MarkForwarded() || !result.Handle.MarkFirstByte() {
		t.Fatalf("lifecycle admission=%+v", result.Decision)
	}
	before := controller.Snapshot(now.Add(2 * time.Millisecond))
	if before.State.ReservationKVTokens != result.Decision.Work.TotalKVTokens {
		t.Fatalf("uncovered active Decode state=%+v", before.State)
	}
	publishObservation(t, controller, testObservation(capability, now.Add(3*time.Millisecond), 1_000, 1, 0, 110, 0))
	after := controller.Snapshot(now.Add(4 * time.Millisecond))
	if after.State.ReservationKVTokens != result.Decision.Work.FutureKVTokens ||
		after.State.LocalActiveDecode != 1 {
		t.Fatalf("covered active Decode state=%+v work=%+v", after.State, result.Decision.Work)
	}
}

func TestControllerSameCapabilityCounterResetRecoversAndFencesOldHandle(t *testing.T) {
	now := time.Unix(3_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 500, 1, 0, 10_000, 7))
	old := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !old.Decision.Admitted() || !old.Handle.MarkForwarded() {
		t.Fatalf("old admission=%+v", old.Decision)
	}
	publication := publishObservation(t, controller, testObservation(capability, now.Add(2*time.Millisecond), 0, 0, 0, 3, 0))
	if !publication.RuntimeReset || publication.RuntimeEpoch != old.Decision.RuntimeEpoch+1 {
		t.Fatalf("reset publication=%+v old=%+v", publication, old.Decision)
	}
	if old.Handle.MarkFirstByte() || old.Handle.Terminate(TerminalSuccess) {
		t.Fatal("old-epoch handle mutated reset Controller")
	}
	fresh := controller.Admit(now.Add(3*time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !fresh.Decision.Admitted() || fresh.Decision.RuntimeEpoch != publication.RuntimeEpoch {
		t.Fatalf("post-reset admission=%+v", fresh.Decision)
	}
}

func TestControllerRuntimeEpochChangeResetsEvenWhenCountersIncrease(t *testing.T) {
	now := time.Unix(3_500, 0)
	capability := testCapability()
	initial := testObservation(capability, now, 500, 1, 0, 10, 0)
	initial.RuntimeStartTime = 100.25
	controller := testControllerWithObservation(t, capability, initial)
	old := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !old.Decision.Admitted() || !old.Handle.MarkForwarded() {
		t.Fatalf("old runtime-epoch admission=%+v", old.Decision)
	}

	reset := testObservation(capability, now.Add(2*time.Millisecond), 0, 0, 0, 11, 0)
	reset.RuntimeStartTime = 200.5
	publication := publishObservation(t, controller, reset)
	if !publication.RuntimeReset || publication.RuntimeEpoch != old.Decision.RuntimeEpoch+1 {
		t.Fatalf("runtime-epoch publication=%+v old=%+v", publication, old.Decision)
	}
	if old.Handle.MarkFirstByte() || old.Handle.Terminate(TerminalSuccess) {
		t.Fatal("old handle mutated Controller after process epoch change")
	}
}

func TestControllerDoesNotForgetKnownRuntimeEpoch(t *testing.T) {
	now := time.Unix(3_750, 0)
	capability := testCapability()
	initial := testObservation(capability, now, 0, 0, 0, 10, 0)
	initial.RuntimeStartTime = 100.25
	controller := testControllerWithObservation(t, capability, initial)
	missing := testObservation(capability, now.Add(time.Millisecond), 0, 0, 0, 11, 0)
	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("sample window unavailable")
	}
	publication := controller.PublishObservation(window, missing)
	if publication.Accepted || publication.Reason != ReasonObservationInvalid {
		t.Fatalf("missing known runtime epoch publication=%+v", publication)
	}
	snapshot := controller.Snapshot(now.Add(2 * time.Millisecond))
	if !snapshot.Available || snapshot.Observation.RuntimeStartTime != initial.RuntimeStartTime ||
		snapshot.Observation.GenerationTokensTotal != initial.GenerationTokensTotal {
		t.Fatalf("missing runtime epoch overwrote coherent observation: %+v", snapshot)
	}
}

func TestControllerCapabilityDriftClosesPermanently(t *testing.T) {
	now := time.Unix(4_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
	drift := testObservation(capability, now.Add(time.Millisecond), 0, 0, 0, 2, 0)
	drift.CapabilityFingerprint = "different-capability"
	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("sample window unavailable")
	}
	publication := controller.PublishObservation(window, drift)
	if !publication.CapabilityDrift || publication.Accepted {
		t.Fatalf("drift publication=%+v", publication)
	}
	decision := controller.Admit(now.Add(2*time.Millisecond), testEstimate(1_024, 1_536, 256)).Decision
	if decision.Action != ActionProtect || decision.Reason != ReasonCapabilityDrift ||
		decision.Scope != ProtectionAvailability {
		t.Fatalf("post-drift decision=%+v", decision)
	}
	if _, ok := controller.StartSampleWindow(); ok {
		t.Fatal("capability drift reopened without a new Controller")
	}
}

func TestControllerConcurrentNearKVAdmissionIsAtomic(t *testing.T) {
	now := time.Unix(5_000, 0)
	capability := testCapability()
	capability.KVCapacityTokens = 4_096
	capability.KVHardLimitTokens = 2_048
	capability.MaximumInputTokens = 1_024
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 1_664, 0, 0, 1, 0))
	estimate := testEstimate(100, 300, 20)

	const arrivals = 32
	start := make(chan struct{})
	results := make(chan AdmissionResult, arrivals)
	var group sync.WaitGroup
	group.Add(arrivals)
	for range arrivals {
		go func() {
			defer group.Done()
			<-start
			results <- controller.Admit(now.Add(time.Millisecond), estimate)
		}()
	}
	close(start)
	group.Wait()
	close(results)
	admitted := 0
	for result := range results {
		if result.Decision.Admitted() {
			admitted++
		}
	}
	if admitted != 1 {
		t.Fatalf("admitted=%d want 1", admitted)
	}
	snapshot := controller.Snapshot(now.Add(2 * time.Millisecond))
	if snapshot.State.EffectiveKVTokens > capability.KVHardLimitTokens {
		t.Fatalf("counterfactual KV exceeded hard limit: %+v", snapshot.State)
	}
	assertAggregateMatchesSlow(t, controller)
}

func TestControllerManySmallPrefillsRecoverImmediately(t *testing.T) {
	now := time.Unix(6_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 1, 0, 1, 0))
	estimate := testEstimate(16*1024, 24*1024, 256)
	handles := make([]ReservationHandle, 0, 4)
	for index := 0; index < 4; index++ {
		result := controller.Admit(now.Add(time.Millisecond), estimate)
		if !result.Decision.Admitted() {
			t.Fatalf("admission %d=%+v", index, result.Decision)
		}
		handles = append(handles, result.Handle)
	}
	protected := controller.Admit(now.Add(time.Millisecond), estimate).Decision
	if protected.Action != ActionProtect || protected.Reason != ReasonPrefillBudget ||
		protected.Scope != ProtectionLoad {
		t.Fatalf("budget protection=%+v", protected)
	}
	if !handles[0].MarkForwarded() || !handles[0].MarkFirstByte() {
		t.Fatal("failed to advance one Prefill")
	}
	reopened := controller.Admit(now.Add(2*time.Millisecond), estimate).Decision
	if !reopened.Admitted() {
		t.Fatalf("capacity did not reopen after lifecycle state change: %+v", reopened)
	}
	assertAggregateMatchesSlow(t, controller)
}

func TestControllerLargeBudgetProtectionDoesNotReserveOrBlockFollowingSmallRequest(t *testing.T) {
	now := time.Unix(6_500, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 1_000, 20, 0, 1, 0))
	large := controller.Admit(now.Add(time.Millisecond), testEstimate(96*1024, 144*1024, 256))
	if large.Decision.Reason != ReasonPrefillBudget || large.Decision.Scope != ProtectionRequest ||
		large.Decision.ReservationID != 0 || large.Handle.MarkForwarded() {
		t.Fatalf("large protection=%+v", large)
	}
	small := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_536, 256))
	if !small.Decision.Admitted() || !small.Handle.MarkForwarded() {
		t.Fatalf("small admission after large protection=%+v", small.Decision)
	}
	assertAggregateMatchesSlow(t, controller)
}

func TestControllerUnknownWaitingStillAdmitsFittingMinimumRequest(t *testing.T) {
	now := time.Unix(6_750, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 1_000, 0, 50, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testEstimate(1, 1, capability.MinimumDecodeHorizonTokens))
	if !result.Decision.Admitted() || result.Decision.PrefillClass != PrefillRegular {
		t.Fatalf("minimum request under unknown waiting=%+v", result.Decision)
	}
}

func TestControllerStaleObservationReopensOnFreshSample(t *testing.T) {
	now := time.Unix(7_000, 0)
	capability := testCapability()
	observation := testObservation(capability, now, 0, 0, 0, 1, 0)
	observation.MaximumAge = 500 * time.Millisecond
	controller := testControllerWithObservation(t, capability, observation)
	stale := controller.Admit(now.Add(time.Second), testEstimate(1_024, 1_536, 256)).Decision
	if stale.Reason != ReasonObservationStale || stale.Scope != ProtectionAvailability {
		t.Fatalf("stale decision=%+v", stale)
	}
	publishObservation(t, controller, testObservation(capability, now.Add(time.Second), 0, 0, 0, 2, 0))
	fresh := controller.Admit(now.Add(time.Second+time.Millisecond), testEstimate(1_024, 1_536, 256)).Decision
	if !fresh.Admitted() {
		t.Fatalf("fresh decision=%+v", fresh)
	}
}

func TestControllerSnapshotPublishesOneCoherentObservationRecord(t *testing.T) {
	now := time.Unix(8_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, 1_000, 3, 1, 100, 4),
	)
	second := testObservation(capability, now.Add(500*time.Millisecond), 1_500, 5, 2, 170, 5)
	publishObservation(t, controller, second)

	snapshot := controller.Snapshot(now.Add(600 * time.Millisecond))
	if !snapshot.IntakeOpen || !snapshot.HasObservation || snapshot.Observation != second {
		t.Fatalf("snapshot observation=%+v", snapshot)
	}
	if snapshot.State.GenerationDelta != 70 || snapshot.State.PreemptionDelta != 1 ||
		snapshot.State.ObservationInterval != 500*time.Millisecond ||
		snapshot.State.PreviousRawRunning != 3 {
		t.Fatalf("snapshot diagnostic state=%+v", snapshot.State)
	}
}

func TestControllerTPSReferenceAloneChangesPreForwardDecision(t *testing.T) {
	now := time.Unix(9_000, 0)
	capability := testCapability()
	strict := testControllerWithTPSObservation(t, capability, 20, testObservation(capability, now, 0, 5, 0, 0, 0))
	permissive := testControllerWithTPSObservation(t, capability, 15, testObservation(capability, now, 0, 5, 0, 0, 0))
	for step := 1; step <= 4; step++ {
		observation := testObservation(capability, now.Add(time.Duration(step)*time.Second), 0, 5, 0, uint64(step*100), 0)
		publishObservation(t, strict, observation)
		publishObservation(t, permissive, observation)
	}
	estimate := testEstimate(1, 1, capability.MinimumDecodeHorizonTokens)
	strictDecision := strict.Admit(now.Add(4*time.Second+time.Millisecond), estimate).Decision
	permissiveDecision := permissive.Admit(now.Add(4*time.Second+time.Millisecond), estimate).Decision
	if strictDecision.Reason != ReasonTPSReference || strictDecision.Scope != ProtectionLoad ||
		strictDecision.TPSSequenceLimit != 5 || strictDecision.TPSCurrentSequences != 5 || strictDecision.TPSPostAdmitSequences != 6 {
		t.Fatalf("strict TPS decision=%+v", strictDecision)
	}
	if !permissiveDecision.Admitted() || permissiveDecision.TPSSequenceLimit != 6 ||
		permissiveDecision.TPSCurrentSequences != 5 || permissiveDecision.TPSPostAdmitSequences != 6 {
		t.Fatalf("permissive TPS decision=%+v", permissiveDecision)
	}
}

func TestControllerTPSWarmingAtomicallyCapsSameSnapshotAtTwoSequences(t *testing.T) {
	now := time.Unix(9_500, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(
		t,
		capability,
		20,
		testObservation(capability, now, 0, 0, 0, 0, 0),
	)
	estimate := testEstimate(1, 1, capability.MinimumDecodeHorizonTokens)
	for admitted := int64(1); admitted <= tpsWarmingSequenceLimit; admitted++ {
		decision := controller.Admit(now.Add(time.Millisecond), estimate).Decision
		if !decision.Admitted() || decision.TPSSequenceLimit != tpsWarmingSequenceLimit ||
			decision.TPSPostAdmitSequences != admitted {
			t.Fatalf("warming admission %d=%+v", admitted, decision)
		}
	}
	protected := controller.Admit(now.Add(time.Millisecond), estimate).Decision
	if protected.Reason != ReasonTPSReference || protected.Scope != ProtectionLoad ||
		protected.TPSCurrentSequences != tpsWarmingSequenceLimit ||
		protected.TPSPostAdmitSequences != tpsWarmingSequenceLimit+1 ||
		protected.ReservationID != 0 {
		t.Fatalf("warming overflow=%+v", protected)
	}
}

func TestControllerTPSSameSnapshotBurstCannotExceedSequenceLimit(t *testing.T) {
	now := time.Unix(10_000, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(t, capability, 20, testObservation(capability, now, 0, 4, 0, 0, 0))
	for step := 1; step <= 4; step++ {
		running := int64(4)
		if step == 4 {
			running = 0
		}
		publishObservation(t, controller, testObservation(
			capability,
			now.Add(time.Duration(step)*time.Second),
			0,
			running,
			0,
			uint64(step*80),
			0,
		))
	}
	estimate := testEstimate(1, 1, capability.MinimumDecodeHorizonTokens)
	admitted := controller.Admit(now.Add(4*time.Second+time.Millisecond), estimate).Decision
	if !admitted.Admitted() || admitted.TPSSequenceLimit != 1 || admitted.TPSPostAdmitSequences != 1 {
		t.Fatalf("idle admission=%+v", admitted)
	}
	protected := controller.Admit(now.Add(4*time.Second+time.Millisecond), estimate).Decision
	if protected.Reason != ReasonTPSReference || protected.TPSSequenceLimit != 1 ||
		protected.TPSCurrentSequences != 1 || protected.TPSPostAdmitSequences != 2 ||
		protected.ReservationID != 0 {
		t.Fatalf("same-snapshot overflow=%+v", protected)
	}
}

func TestControllerTPSReservationAboveNonzeroRawCannotOvershootLimit(t *testing.T) {
	now := time.Unix(10_500, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(
		t,
		capability,
		20,
		testObservation(capability, now, 0, 4, 0, 0, 0),
	)
	for step := 1; step <= 4; step++ {
		publishObservation(t, controller, testObservation(
			capability,
			now.Add(time.Duration(step)*time.Second),
			0,
			4,
			0,
			uint64(step*100),
			0,
		))
	}

	estimate := testEstimate(1, 1, capability.MinimumDecodeHorizonTokens)
	first := controller.Admit(now.Add(4*time.Second+time.Millisecond), estimate)
	if !first.Decision.Admitted() || first.Decision.TPSSequenceLimit != 5 ||
		first.Decision.TPSCurrentSequences != 4 || first.Decision.TPSPostAdmitSequences != 5 {
		t.Fatalf("first same-poll admission=%+v", first.Decision)
	}
	if !first.Handle.MarkForwarded() {
		t.Fatal("first same-poll reservation did not reach the forwarded phase")
	}
	second := controller.Admit(now.Add(4*time.Second+time.Millisecond), estimate).Decision
	if second.Admitted() || second.Reason != ReasonTPSReference ||
		second.TPSSequenceLimit != 5 || second.TPSCurrentSequences != 5 ||
		second.TPSPostAdmitSequences != 6 || second.State.UnobservedSequences != 1 ||
		second.ReservationID != 0 {
		t.Fatalf("second same-poll decision overshot learned limit: %+v", second)
	}
}

func TestControllerTPSCoveringSampleDoesNotDoubleCountForwardedReservation(t *testing.T) {
	now := time.Unix(10_750, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(
		t,
		capability,
		20,
		testObservation(capability, now, 0, 4, 0, 0, 0),
	)
	for step := 1; step <= 4; step++ {
		publishObservation(t, controller, testObservation(
			capability,
			now.Add(time.Duration(step)*time.Second),
			0,
			4,
			0,
			uint64(step*120),
			0,
		))
	}

	estimate := testEstimate(1, 1, capability.MinimumDecodeHorizonTokens)
	first := controller.Admit(now.Add(4*time.Second+time.Millisecond), estimate)
	if !first.Decision.Admitted() || !first.Handle.MarkForwarded() {
		t.Fatalf("forwarded reservation=%+v", first.Decision)
	}
	publishObservation(t, controller, testObservation(
		capability,
		now.Add(5*time.Second),
		0,
		5,
		0,
		600,
		0,
	))
	second := controller.Admit(now.Add(5*time.Second+time.Millisecond), estimate).Decision
	if !second.Admitted() || second.TPSSequenceLimit != 6 ||
		second.TPSCurrentSequences != 5 || second.TPSPostAdmitSequences != 6 ||
		second.State.UnobservedSequences != 0 {
		t.Fatalf("covering sample double-counted forwarded reservation: %+v", second)
	}
}

func TestControllerTPSTerminalBeforeCoveringSampleRetainsUnobservedDemand(t *testing.T) {
	now := time.Unix(10_875, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(
		t,
		capability,
		20,
		testObservation(capability, now, 0, 4, 0, 0, 0),
	)
	for step := 1; step <= 4; step++ {
		publishObservation(t, controller, testObservation(
			capability,
			now.Add(time.Duration(step)*time.Second),
			0,
			4,
			0,
			uint64(step*100),
			0,
		))
	}

	estimate := testEstimate(1, 1, capability.MinimumDecodeHorizonTokens)
	forwarded := controller.Admit(now.Add(4*time.Second+time.Millisecond), estimate)
	if !forwarded.Decision.Admitted() || !forwarded.Handle.MarkForwarded() ||
		!forwarded.Handle.Terminate(TerminalCancel) {
		t.Fatalf("forwarded terminal lifecycle=%+v", forwarded.Decision)
	}
	protected := controller.Admit(now.Add(4*time.Second+2*time.Millisecond), estimate).Decision
	if protected.Admitted() || protected.Reason != ReasonTPSReference ||
		protected.State.ResidualDebts != 1 || protected.State.UnobservedSequences != 1 ||
		protected.TPSCurrentSequences != 5 || protected.TPSPostAdmitSequences != 6 {
		t.Fatalf("terminal blind window lost demand: %+v", protected)
	}

	publishObservation(t, controller, testObservation(
		capability,
		now.Add(5*time.Second),
		0,
		4,
		0,
		500,
		0,
	))
	reopened := controller.Admit(now.Add(5*time.Second+time.Millisecond), estimate).Decision
	if !reopened.Admitted() || reopened.State.ResidualDebts != 0 ||
		reopened.State.UnobservedSequences != 0 || reopened.TPSPostAdmitSequences != 5 {
		t.Fatalf("covering sample did not release terminal demand: %+v", reopened)
	}
}

func TestControllerTPSUnobservedSuccessDoesNotDelayReplacement(t *testing.T) {
	now := time.Unix(10_937, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(
		t,
		capability,
		20,
		testObservation(capability, now, 0, 4, 0, 0, 0),
	)
	for step := 1; step <= 4; step++ {
		publishObservation(t, controller, testObservation(
			capability,
			now.Add(time.Duration(step)*time.Second),
			0,
			4,
			0,
			uint64(step*100),
			0,
		))
	}

	estimate := testEstimate(1, 1, capability.MinimumDecodeHorizonTokens)
	completed := controller.Admit(now.Add(4*time.Second+time.Millisecond), estimate)
	if !completed.Decision.Admitted() || !completed.Handle.MarkForwarded() ||
		!completed.Handle.MarkFirstByte() || !completed.Handle.Terminate(TerminalSuccess) {
		t.Fatalf("successful lifecycle=%+v", completed.Decision)
	}
	replacement := controller.Admit(now.Add(4*time.Second+2*time.Millisecond), estimate).Decision
	if !replacement.Admitted() || replacement.State.ResidualDebts != 1 ||
		replacement.State.UnobservedSequences != 0 || replacement.TPSPostAdmitSequences != 5 {
		t.Fatalf("successful completion delayed replacement: %+v", replacement)
	}
}

func TestControllerRuntimeResetClearsTPSWindow(t *testing.T) {
	now := time.Unix(11_000, 0)
	capability := testCapability()
	controller := testControllerWithTPSObservation(t, capability, 20, testObservation(capability, now, 0, 2, 0, 100, 0))
	for step := 1; step <= 4; step++ {
		publishObservation(t, controller, testObservation(capability, now.Add(time.Duration(step)*time.Second), 0, 2, 0, uint64(100+step*40), 0))
	}
	if before := controller.Snapshot(now.Add(4*time.Second + time.Millisecond)); !before.State.TPS.Ready {
		t.Fatalf("TPS window did not warm before reset: %+v", before.State.TPS)
	}
	reset := testObservation(capability, now.Add(5*time.Second), 0, 0, 0, 1, 0)
	publication := publishObservation(t, controller, reset)
	if !publication.RuntimeReset {
		t.Fatalf("counter reset publication=%+v", publication)
	}
	after := controller.Snapshot(now.Add(5*time.Second + time.Millisecond))
	if after.State.TPS.Ready || after.State.TPS.QualifiedSamples != 0 || after.State.TPS.QualifiedSequenceSeconds != 0 {
		t.Fatalf("TPS window survived runtime reset: %+v", after.State.TPS)
	}
}

func testControllerWithObservation(t *testing.T, capability Capability, observation BackendObservation) *AdmissionController {
	t.Helper()
	controller, err := NewAdmissionController(ControllerConfig{Capability: capability})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, observation)
	return controller
}

func testControllerWithTPSObservation(
	t *testing.T,
	capability Capability,
	reference float64,
	observation BackendObservation,
) *AdmissionController {
	t.Helper()
	controller, err := NewAdmissionController(ControllerConfig{
		Capability: capability,
		TPS:        TPSPolicyConfig{Reference: reference},
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, observation)
	return controller
}

func publishObservation(t *testing.T, controller *AdmissionController, observation BackendObservation) PublicationResult {
	t.Helper()
	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("sample window unavailable")
	}
	result := controller.PublishObservation(window, observation)
	if !result.Accepted {
		t.Fatalf("observation publication=%+v", result)
	}
	return result
}

func testObservation(capability Capability, at time.Time, used, running, waiting int64, generation, preemptions uint64) BackendObservation {
	return BackendObservation{
		CapabilityFingerprint: capability.Fingerprint,
		MaxModelLenTokens:     capability.MaxModelLenTokens,
		KVCapacityTokens:      capability.KVCapacityTokens,
		KVBlockSize:           capability.KVBlockSize,
		ObservedAt:            at,
		MaximumAge:            5 * time.Second,
		UsedKVTokens:          used,
		Running:               running,
		Waiting:               waiting,
		GenerationTokensTotal: generation,
		PreemptionsTotal:      preemptions,
	}
}

func testEstimate(selection, reservation, decode int64) predictive.RequestEstimate {
	return predictive.RequestEstimate{
		SelectionInputTokens:       selection,
		MaximumSequenceInputTokens: selection,
		KVReservationInputTokens:   reservation,
		DecodeHorizonTokens:        decode,
		DecodeSequences:            1,
	}
}

func assertAggregateMatchesSlow(t *testing.T, controller *AdmissionController) {
	t.Helper()
	controller.mu.Lock()
	defer controller.mu.Unlock()
	want, ok := controller.slowOverlayLocked()
	if !ok || controller.overlay != want {
		t.Fatalf("aggregate=%+v slow=%+v valid=%t", controller.overlay, want, ok)
	}
}
