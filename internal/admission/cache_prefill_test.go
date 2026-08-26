package admission

import (
	"testing"
	"time"
)

func TestControllerUsesBoundedRecentCacheCreditOnlyForPrefillCompute(t *testing.T) {
	now := time.Unix(20_000, 0)
	capability := testCapability()
	initial := cacheObservation(capability, now, 10_000, 5_000)
	controller := testControllerWithObservation(t, capability, initial)

	first := controller.Admit(now.Add(time.Millisecond), testEstimate(32*1024, 40*1024, 256)).Decision
	if first.Work.PrefillComputeTokens != 32*1024 {
		t.Fatalf("first cache counter sample received credit: %+v", first)
	}
	if !first.Admitted() {
		t.Fatalf("first cache counter sample was not admitted: %+v", first)
	}
	firstHandle := ReservationHandle{controller: controller, runtimeEpoch: first.RuntimeEpoch, id: first.ReservationID}
	if !firstHandle.Terminate(TerminalCancel) {
		t.Fatalf("first admission lifecycle=%+v", first)
	}

	next := cacheObservation(capability, now.Add(time.Second), 10_000+32*1024, 5_000+24*1024)
	publishObservation(t, controller, next)
	hot := controller.Admit(now.Add(time.Second+time.Millisecond), testEstimate(32*1024, 40*1024, 256)).Decision
	if hot.Work.PrefillComputeTokens != 8*1024 {
		t.Fatalf("bounded 75%% cache credit compute=%d want %d decision=%+v", hot.Work.PrefillComputeTokens, 8*1024, hot)
	}
	if hot.Work.TotalKVTokens != first.Work.TotalKVTokens ||
		hot.Work.InputKVTokens != first.Work.InputKVTokens ||
		hot.PrefillClass != first.PrefillClass {
		t.Fatalf("cache credit changed KV or class: cold=%+v hot=%+v", first, hot)
	}
}

func TestControllerCacheCreditDoesNotDowngradeLongInputClasses(t *testing.T) {
	now := time.Unix(21_000, 0)
	capability := testCapability()
	initial := cacheObservation(capability, now, 10_000, 9_000)
	initial.Running = 4
	controller := testControllerWithObservation(t, capability, initial)
	hot := cacheObservation(capability, now.Add(time.Second), 10_000+128*1024, 9_000+128*1024)
	hot.Running = 4
	publishObservation(t, controller, hot)

	weighted := controller.Admit(now.Add(time.Second+time.Millisecond), testEstimate(128*1024, 128*1024, 256)).Decision
	if !weighted.Admitted() || weighted.PrefillClass != PrefillWeighted ||
		weighted.Work.PrefillComputeTokens != 64*1024 || weighted.Work.InputKVTokens < 128*1024 {
		t.Fatalf("weighted cache charge/class=%+v", weighted)
	}
	weightedHandle := ReservationHandle{controller: controller, runtimeEpoch: weighted.RuntimeEpoch, id: weighted.ReservationID}
	if !weightedHandle.Terminate(TerminalCancel) {
		t.Fatalf("weighted cache admission lifecycle=%+v", weighted)
	}

	exclusive := controller.Admit(now.Add(time.Second+2*time.Millisecond), testEstimate(300*1024, 300*1024, 256)).Decision
	if exclusive.Admitted() || exclusive.PrefillClass != PrefillExclusive || exclusive.Work.TotalKVTokens < 300*1024 {
		t.Fatalf("exclusive cache class/KV=%+v", exclusive)
	}

	quiescent := controller.Admit(now.Add(time.Second+3*time.Millisecond), testEstimate(600*1024, 600*1024, 256)).Decision
	if quiescent.Admitted() || quiescent.PrefillClass != PrefillQuiescent || quiescent.Work.TotalKVTokens < 600*1024 {
		t.Fatalf("quiescent cache class/KV=%+v", quiescent)
	}
}

func TestV01215ControllerBoundsCacheCreditByRecentEvidence(t *testing.T) {
	now := time.Unix(21_500, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, cacheObservation(capability, now, 10_000, 5_000))
	publishObservation(t, controller, cacheObservation(capability, now.Add(time.Second), 10_000+32*1024, 5_000+32*1024))

	const (
		inputTokens          = int64(16 * 1024)
		recentCreditTokens   = int64(24 * 1024)
		expectedAdmitted     = 17
		maximumAdmissionLoop = 100
	)
	var pendingCreditTokens int64
	admitted := 0
	for index := 0; index < maximumAdmissionLoop; index++ {
		result := controller.Admit(now.Add(time.Second+time.Duration(index+1)*time.Millisecond), testEstimate(inputTokens, inputTokens, 256))
		if !result.Decision.Admitted() {
			break
		}
		admitted++
		pendingCreditTokens += inputTokens - result.Decision.Work.PrefillComputeTokens
	}

	if pendingCreditTokens > recentCreditTokens {
		t.Fatalf("pending cache credit=%d exceeds recent evidence budget=%d after %d admissions", pendingCreditTokens, recentCreditTokens, admitted)
	}
	if admitted != expectedAdmitted {
		t.Fatalf("cache-aware admissions=%d want %d without exceeding recent evidence", admitted, expectedAdmitted)
	}
	snapshot := controller.Snapshot(now.Add(2 * time.Second))
	if snapshot.State.CacheCreditBudgetTokens != recentCreditTokens ||
		snapshot.State.CacheCreditSpentTokens != pendingCreditTokens ||
		snapshot.State.PendingCacheCreditTokens != pendingCreditTokens ||
		snapshot.State.PendingPrefillInputTokens != int64(admitted)*inputTokens {
		t.Fatalf("bounded cache-credit state=%+v", snapshot.State)
	}
}

func TestV01215ControllerRefundsCacheCreditCancelledBeforeForward(t *testing.T) {
	now := time.Unix(21_700, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, cacheObservation(capability, now, 10_000, 5_000))
	publishObservation(t, controller, cacheObservation(capability, now.Add(time.Second), 10_000+16*1024, 5_000+16*1024))

	first := controller.Admit(now.Add(time.Second+time.Millisecond), testEstimate(16*1024, 16*1024, 256))
	if !first.Decision.Admitted() || first.Decision.Work.PrefillComputeTokens != 4*1024 ||
		!first.Handle.Terminate(TerminalCancel) {
		t.Fatalf("pre-forward cancellation=%+v", first.Decision)
	}
	second := controller.Admit(now.Add(time.Second+2*time.Millisecond), testEstimate(16*1024, 16*1024, 256))
	if !second.Decision.Admitted() || second.Decision.Work.PrefillComputeTokens != 4*1024 {
		t.Fatalf("pre-forward cancellation did not refund cache credit: %+v", second.Decision)
	}
	_ = second.Handle.Terminate(TerminalCancel)
}

func TestV01215ControllerDoesNotRefundRotatedCacheLeaseWithEqualTimestamp(t *testing.T) {
	now := time.Unix(21_725, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, cacheObservation(capability, now, 10_000, 5_000))
	hot := cacheObservation(capability, now.Add(time.Second), 10_000+16*1024, 5_000+16*1024)
	publishObservation(t, controller, hot)

	first := controller.Admit(now.Add(time.Second), testEstimate(16*1024, 16*1024, 256))
	if !first.Decision.Admitted() || first.Decision.Work.PrefillComputeTokens != 4*1024 {
		t.Fatalf("first cache lease admission=%+v", first.Decision)
	}
	rotated := cacheObservation(capability, hot.ObservedAt, 10_000+32*1024, 5_000+32*1024)
	publishObservation(t, controller, rotated)
	if !first.Handle.Terminate(TerminalCancel) {
		t.Fatal("old cache lease cancellation failed")
	}

	second := controller.Admit(now.Add(time.Second+time.Microsecond), testEstimate(16*1024, 16*1024, 256))
	if !second.Decision.Admitted() || second.Decision.Work.PrefillComputeTokens != 4*1024 {
		t.Fatalf("old cancellation changed replacement cache lease: %+v", second.Decision)
	}
	_ = second.Handle.Terminate(TerminalCancel)
}

func TestV01215ControllerDoesNotReuseCacheEvidenceAcrossZeroDeltaPolls(t *testing.T) {
	now := time.Unix(21_625, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, cacheObservation(capability, now, 10_000, 5_000))
	publishObservation(t, controller, cacheObservation(capability, now.Add(time.Second), 10_000+32*1024, 5_000+32*1024))

	const inputTokens = int64(16 * 1024)
	handles := make([]ReservationHandle, 0, 2)
	for index := 0; index < 2; index++ {
		result := controller.Admit(
			now.Add(time.Second+time.Duration(index+1)*time.Millisecond),
			testEstimate(inputTokens, inputTokens, 256),
		)
		if !result.Decision.Admitted() || result.Decision.Work.PrefillComputeTokens != 4*1024 ||
			!result.Handle.MarkForwarded() || !result.Handle.MarkFirstByte() {
			t.Fatalf("cache-credit admission %d=%+v", index, result.Decision)
		}
		handles = append(handles, result.Handle)
	}

	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("start zero-delta sample window")
	}
	covered := cacheObservation(capability, now.Add(1500*time.Millisecond), 10_000+32*1024, 5_000+32*1024)
	covered.UsedKVTokens = 2 * inputTokens
	covered.Running = 2
	if publication := controller.PublishObservation(window, covered); !publication.Accepted {
		t.Fatalf("publish zero-delta covering sample=%+v", publication)
	}

	third := controller.Admit(
		now.Add(1500*time.Millisecond+time.Microsecond),
		testEstimate(inputTokens, inputTokens, 256),
	)
	if !third.Decision.Admitted() || third.Decision.Work.PrefillComputeTokens != inputTokens {
		t.Fatalf("zero-delta poll reused exhausted cache evidence: %+v", third.Decision)
	}
	_ = third.Handle.Terminate(TerminalCancel)
	for _, handle := range handles {
		_ = handle.Terminate(TerminalSuccess)
	}
}

func TestV01215ControllerColdTransitionStopsNewCreditWithoutInvalidatingPendingWork(t *testing.T) {
	now := time.Unix(21_750, 0)
	capability := testCapability()
	initial := cacheObservation(capability, now, 10_000, 5_000)
	controller := testControllerWithObservation(t, capability, initial)
	hot := cacheObservation(capability, now.Add(time.Second), 10_000+32*1024, 5_000+32*1024)
	publishObservation(t, controller, hot)

	first := controller.Admit(now.Add(time.Second+time.Millisecond), testEstimate(16*1024, 16*1024, 256)).Decision
	if !first.Admitted() || first.Work.PrefillComputeTokens != 4*1024 {
		t.Fatalf("hot admission=%+v", first)
	}

	cold := cacheObservation(capability, now.Add(2*time.Second), 10_000+64*1024, 5_000+32*1024)
	publishObservation(t, controller, cold)
	second := controller.Admit(now.Add(2*time.Second+time.Millisecond), testEstimate(16*1024, 16*1024, 256)).Decision
	if !second.Admitted() || second.Work.PrefillComputeTokens != 16*1024 {
		t.Fatalf("cold transition admission=%+v", second)
	}
	if second.State.CacheCreditBudgetTokens != 0 || second.State.PendingCacheCreditTokens != 12*1024 {
		t.Fatalf("cold transition lost pending work or retained new credit=%+v", second.State)
	}
}

func TestControllerCacheFallbacksNeverCloseLowFlowAdmission(t *testing.T) {
	now := time.Unix(22_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, cacheObservation(capability, now, 10_000, 5_000))

	lowEvidence := cacheObservation(capability, now.Add(time.Second), 11_000, 5_999)
	publishObservation(t, controller, lowEvidence)
	low := controller.Admit(now.Add(time.Second+time.Millisecond), testEstimate(16*1024, 16*1024, 256)).Decision
	if !low.Admitted() || low.Work.PrefillComputeTokens != 16*1024 {
		t.Fatalf("low cache evidence locked or discounted admission=%+v", low)
	}
	lowHandle := ReservationHandle{controller: controller, runtimeEpoch: low.RuntimeEpoch, id: low.ReservationID}
	_ = lowHandle.Terminate(TerminalCancel)

	reset := cacheObservation(capability, now.Add(2*time.Second), 100, 50)
	publishObservation(t, controller, reset)
	afterReset := controller.Admit(now.Add(2*time.Second+time.Millisecond), testEstimate(16*1024, 16*1024, 256)).Decision
	if !afterReset.Admitted() || afterReset.Work.PrefillComputeTokens != 16*1024 {
		t.Fatalf("cache reset locked or discounted admission=%+v", afterReset)
	}
}

func TestV01222ControllerAccumulatesCoherentSubthresholdCacheDeltas(t *testing.T) {
	now := time.Unix(22_500, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		cacheObservation(capability, now, 10_000, 5_000),
	)

	firstPoll := cacheObservation(capability, now.Add(500*time.Millisecond), 10_000+2*1024, 5_000+1536)
	publishObservation(t, controller, firstPoll)
	beforeMinimum := controller.Admit(
		now.Add(500*time.Millisecond+time.Microsecond),
		testEstimate(4*1024, 4*1024, 256),
	)
	if !beforeMinimum.Decision.Admitted() || beforeMinimum.Decision.Work.PrefillComputeTokens != 4*1024 {
		t.Fatalf("subthreshold cache delta changed or blocked admission: %+v", beforeMinimum.Decision)
	}
	if !beforeMinimum.Handle.Terminate(TerminalCancel) {
		t.Fatalf("subthreshold admission lifecycle=%+v", beforeMinimum.Decision)
	}

	secondPoll := cacheObservation(capability, now.Add(time.Second), 10_000+4*1024, 5_000+3072)
	publishObservation(t, controller, secondPoll)
	qualified := controller.Admit(
		now.Add(time.Second+time.Microsecond),
		testEstimate(4*1024, 4*1024, 256),
	).Decision
	if !qualified.Admitted() || qualified.Work.PrefillComputeTokens != 1024 {
		t.Fatalf("coherent cache deltas did not accumulate into bounded credit: %+v", qualified)
	}
	if qualified.State.CacheEvidenceTokens != 4*1024 ||
		qualified.State.CacheCreditBudgetTokens != 3072 ||
		qualified.Work.InputKVTokens != qualified.Estimate.KVReservationInputTokens ||
		qualified.Work.TotalKVTokens != qualified.Work.InputKVTokens+qualified.Work.FutureKVTokens {
		t.Fatalf("accumulated cache evidence changed KV or has the wrong budget: %+v", qualified)
	}
}

func TestV01222ControllerAccumulatesHotAndColdDeltasWithoutOvercredit(t *testing.T) {
	now := time.Unix(22_600, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		cacheObservation(capability, now, 10_000, 5_000),
	)

	publishObservation(t, controller, cacheObservation(
		capability,
		now.Add(500*time.Millisecond),
		10_000+2*1024,
		5_000+2*1024,
	))
	publishObservation(t, controller, cacheObservation(
		capability,
		now.Add(time.Second),
		10_000+4*1024,
		5_000+2*1024,
	))

	result := controller.Admit(
		now.Add(time.Second+time.Microsecond),
		testEstimate(4*1024, 4*1024, 256),
	).Decision
	if !result.Admitted() || result.Work.PrefillComputeTokens != 2*1024 ||
		result.State.CacheHitFraction != 0.5 || result.State.CacheCreditBudgetTokens != 2*1024 ||
		result.Work.InputKVTokens != result.Estimate.KVReservationInputTokens {
		t.Fatalf("mixed hot/cold cache evidence was overcredited or changed KV: %+v", result)
	}
}

func TestV01222ControllerZeroDeltaDoesNotExtendCacheAccumulation(t *testing.T) {
	now := time.Unix(22_700, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		cacheObservation(capability, now, 10_000, 5_000),
	)

	publishObservation(t, controller, cacheObservation(
		capability,
		now.Add(500*time.Millisecond),
		10_000+2*1024,
		5_000+1536,
	))
	publishObservation(t, controller, cacheObservation(
		capability,
		now.Add(time.Second),
		10_000+2*1024,
		5_000+1536,
	))
	publishObservation(t, controller, cacheObservation(
		capability,
		now.Add(1500*time.Millisecond),
		10_000+2*1024,
		5_000+1536,
	))
	publishObservation(t, controller, cacheObservation(
		capability,
		now.Add(2*time.Second),
		10_000+4*1024,
		5_000+3072,
	))

	result := controller.Admit(
		now.Add(2*time.Second+time.Microsecond),
		testEstimate(4*1024, 4*1024, 256),
	).Decision
	if !result.Admitted() || result.Work.PrefillComputeTokens != 4*1024 ||
		result.State.CacheObservationValid {
		t.Fatalf("zero-delta polls extended expired cache accumulation: %+v", result)
	}
}

func TestV01222ControllerCacheCounterRollbackClearsAccumulation(t *testing.T) {
	now := time.Unix(22_800, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		cacheObservation(capability, now, 10_000, 5_000),
	)

	publishObservation(t, controller, cacheObservation(
		capability,
		now.Add(500*time.Millisecond),
		10_000+2*1024,
		5_000+1536,
	))
	publishObservation(t, controller, cacheObservation(
		capability,
		now.Add(time.Second),
		100,
		50,
	))
	publishObservation(t, controller, cacheObservation(
		capability,
		now.Add(1500*time.Millisecond),
		100+2*1024,
		50+1536,
	))

	result := controller.Admit(
		now.Add(1500*time.Millisecond+time.Microsecond),
		testEstimate(4*1024, 4*1024, 256),
	).Decision
	if !result.Admitted() || result.Work.PrefillComputeTokens != 4*1024 ||
		result.State.CacheObservationValid {
		t.Fatalf("cache counter rollback retained pre-reset accumulation: %+v", result)
	}
}

func TestV01215ControllerCacheCreditExpiresAtRequestTime(t *testing.T) {
	now := time.Unix(23_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, cacheObservation(capability, now, 10_000, 5_000))
	hotCounters := cacheObservation(capability, now.Add(time.Second), 10_000+32*1024, 5_000+24*1024)
	publishObservation(t, controller, hotCounters)

	atLifetime := cacheObservation(capability, now.Add(2*time.Second), 10_000+32*1024, 5_000+24*1024)
	publishObservation(t, controller, atLifetime)
	carried := controller.Admit(now.Add(2*time.Second), testEstimate(32*1024, 40*1024, 256)).Decision
	if !carried.Admitted() || carried.Work.PrefillComputeTokens != 8*1024 {
		t.Fatalf("cache credit was not available at its exact lifetime: %+v", carried)
	}
	carriedHandle := ReservationHandle{controller: controller, runtimeEpoch: carried.RuntimeEpoch, id: carried.ReservationID}
	_ = carriedHandle.Terminate(TerminalCancel)

	expired := controller.Admit(now.Add(2*time.Second+time.Millisecond), testEstimate(32*1024, 40*1024, 256)).Decision
	if !expired.Admitted() || expired.Work.PrefillComputeTokens != 32*1024 {
		t.Fatalf("request-time-expired cache credit still changed Prefill compute: %+v", expired)
	}
}

func TestControllerCacheCreditIsSuppressedByCurrentPreemption(t *testing.T) {
	now := time.Unix(24_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, cacheObservation(capability, now, 10_000, 5_000))

	preempted := cacheObservation(capability, now.Add(time.Second), 20_000, 12_500)
	preempted.PreemptionsTotal = 1
	publishObservation(t, controller, preempted)
	decision := controller.Admit(now.Add(time.Second+time.Millisecond), testEstimate(32*1024, 40*1024, 256)).Decision
	if !decision.Admitted() || decision.Work.PrefillComputeTokens != 32*1024 {
		t.Fatalf("current preemption sample received cache credit: %+v", decision)
	}
}

func TestControllerRuntimeEpochResetClearsCacheCredit(t *testing.T) {
	now := time.Unix(25_000, 0)
	capability := testCapability()
	initial := cacheObservation(capability, now, 10_000, 5_000)
	initial.RuntimeStartTime = 100
	controller := testControllerWithObservation(t, capability, initial)

	hotCounters := cacheObservation(capability, now.Add(time.Second), 10_000+32*1024, 5_000+24*1024)
	hotCounters.RuntimeStartTime = 100
	publishObservation(t, controller, hotCounters)
	hot := controller.Admit(now.Add(time.Second+time.Millisecond), testEstimate(32*1024, 40*1024, 256)).Decision
	if !hot.Admitted() || hot.Work.PrefillComputeTokens != 8*1024 {
		t.Fatalf("cache credit did not mature before runtime reset: %+v", hot)
	}
	hotHandle := ReservationHandle{controller: controller, runtimeEpoch: hot.RuntimeEpoch, id: hot.ReservationID}
	_ = hotHandle.Terminate(TerminalCancel)

	reset := cacheObservation(capability, now.Add(2*time.Second), 100, 50)
	reset.RuntimeStartTime = 200
	publication := publishObservation(t, controller, reset)
	if !publication.RuntimeReset {
		t.Fatalf("backend epoch change was not classified as a reset: %+v", publication)
	}
	afterReset := controller.Admit(now.Add(2*time.Second+time.Millisecond), testEstimate(32*1024, 40*1024, 256)).Decision
	if !afterReset.Admitted() || afterReset.Work.PrefillComputeTokens != 32*1024 {
		t.Fatalf("cache credit survived backend epoch reset: %+v", afterReset)
	}
}

func cacheObservation(capability Capability, at time.Time, queries, hits uint64) BackendObservation {
	observation := testObservation(capability, at, 0, 0, 0, uint64(at.Unix()), 0)
	observation.CacheCountersValid = true
	observation.CacheQueryTokensTotal = queries
	observation.CacheHitTokensTotal = hits
	return observation
}
