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

	next := cacheObservation(capability, now.Add(time.Second), 20_000, 12_500)
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
	controller := testControllerWithObservation(t, capability, cacheObservation(capability, now, 10_000, 9_000))
	publishObservation(t, controller, cacheObservation(capability, now.Add(time.Second), 20_000, 18_900))

	weighted := controller.Admit(now.Add(time.Second+time.Millisecond), testEstimate(128*1024, 128*1024, 256)).Decision
	if weighted.PrefillClass != PrefillWeighted || weighted.Work.PrefillComputeTokens != 64*1024 {
		t.Fatalf("weighted cache charge/class=%+v", weighted)
	}
	if weighted.Admitted() {
		weightedHandle := ReservationHandle{controller: controller, runtimeEpoch: weighted.RuntimeEpoch, id: weighted.ReservationID}
		_ = weightedHandle.Terminate(TerminalCancel)
	}

	exclusive := controller.Admit(now.Add(time.Second+2*time.Millisecond), testEstimate(300*1024, 300*1024, 256)).Decision
	if exclusive.PrefillClass != PrefillExclusive || exclusive.Work.TotalKVTokens < 300*1024 {
		t.Fatalf("exclusive cache class/KV=%+v", exclusive)
	}
	if exclusive.Admitted() {
		exclusiveHandle := ReservationHandle{controller: controller, runtimeEpoch: exclusive.RuntimeEpoch, id: exclusive.ReservationID}
		_ = exclusiveHandle.Terminate(TerminalCancel)
	}

	quiescent := controller.Admit(now.Add(time.Second+3*time.Millisecond), testEstimate(600*1024, 600*1024, 256)).Decision
	if quiescent.PrefillClass != PrefillQuiescent || quiescent.Work.TotalKVTokens < 600*1024 {
		t.Fatalf("quiescent cache class/KV=%+v", quiescent)
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

func cacheObservation(capability Capability, at time.Time, queries, hits uint64) BackendObservation {
	observation := testObservation(capability, at, 0, 0, 0, uint64(at.Unix()), 0)
	observation.CacheCountersValid = true
	observation.CacheQueryTokensTotal = queries
	observation.CacheHitTokensTotal = hits
	return observation
}
