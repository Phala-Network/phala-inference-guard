package kvshadow

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
)

func managerBackend(now time.Time, used int64, generation uint64) kvadmission.BackendSnapshot {
	return kvadmission.BackendSnapshot{
		Name:              "a",
		Kind:              kvadmission.BackendVLLM,
		CapacityTokens:    100000,
		UsedTokens:        used,
		Updated:           now,
		GenerationTokens:  generation,
		TokenMetricsValid: true,
	}
}

func managerCost(high int64) kvadmission.Cost {
	return kvadmission.Cost{Supported: true, EstimatedInputLow: high / 2, EstimatedInputHigh: high}
}

func TestDecideAndReserveIsAtomicAcrossBurst(t *testing.T) {
	now := time.Unix(100, 0)
	policy := kvadmission.DefaultPolicy()
	policy.DecodeDriftTokens = 0
	manager := New(policy)
	backend := managerBackend(now, 80000, 100)

	var wg sync.WaitGroup
	var mu sync.Mutex
	fit := 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			decision, reserved := manager.DecideAndReserve(now, fmt.Sprintf("r%d", id), managerCost(1000), []kvadmission.BackendSnapshot{backend})
			if decision.Reason == kvadmission.ReasonFit && reserved {
				mu.Lock()
				fit++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	snapshot := manager.Snapshot()
	if fit != 8 {
		t.Fatalf("fit=%d want exactly 8 up to 88%% hard budget", fit)
	}
	if snapshot.Reservations != fit || snapshot.UnabsorbedTokens != int64(fit*1000) {
		t.Fatalf("snapshot=%#v want bounded atomic reservations", snapshot)
	}
}

func TestObserveAbsorbsReservationsAndReleaseIsIdempotent(t *testing.T) {
	now := time.Unix(100, 0)
	policy := kvadmission.DefaultPolicy()
	policy.DecodeDriftTokens = 0
	manager := New(policy)
	backend := managerBackend(now, 50000, 100)
	if _, ok := manager.DecideAndReserve(now, "r1", managerCost(2000), []kvadmission.BackendSnapshot{backend}); !ok {
		t.Fatal("reservation not created")
	}
	backend.UsedTokens += 1500
	backend.Updated = backend.Updated.Add(time.Second)
	backend.GenerationTokens += 100
	manager.Observe(now.Add(time.Second), []kvadmission.BackendSnapshot{backend})
	if got := manager.Snapshot().UnabsorbedTokens; got != 500 {
		t.Fatalf("unabsorbed=%d want 500", got)
	}
	if !manager.Release("r1") || manager.Release("r1") {
		t.Fatal("release must succeed once and be idempotent")
	}
	if got := manager.Snapshot().Reservations; got != 0 {
		t.Fatalf("reservations=%d want 0", got)
	}
}

func TestObserveCounterResetAndCapacityChangeClearReservations(t *testing.T) {
	now := time.Unix(100, 0)
	policy := kvadmission.DefaultPolicy()
	policy.DecodeDriftTokens = 0
	manager := New(policy)
	backend := managerBackend(now, 50000, 100)
	if _, ok := manager.DecideAndReserve(now, "r1", managerCost(1000), []kvadmission.BackendSnapshot{backend}); !ok {
		t.Fatal("reservation not created")
	}
	backend.Updated = backend.Updated.Add(time.Second)
	backend.GenerationTokens = 1
	manager.Observe(now.Add(time.Second), []kvadmission.BackendSnapshot{backend})
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.Backends[0].ResetTotal != 1 {
		t.Fatalf("counter reset snapshot=%#v", snapshot)
	}
	if _, ok := manager.DecideAndReserve(now.Add(2*time.Second), "r2", managerCost(1000), []kvadmission.BackendSnapshot{backend}); !ok {
		t.Fatal("second reservation not created")
	}
	backend.Updated = backend.Updated.Add(time.Second)
	backend.CapacityTokens = 90000
	manager.Observe(now.Add(3*time.Second), []kvadmission.BackendSnapshot{backend})
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 || snapshot.Backends[0].ResetTotal != 2 {
		t.Fatalf("capacity reset snapshot=%#v", snapshot)
	}
}

func TestSweepBoundsReservationMemory(t *testing.T) {
	now := time.Unix(100, 0)
	policy := kvadmission.DefaultPolicy()
	policy.DecodeDriftTokens = 0
	policy.ReservationTTL = time.Second
	manager := New(policy)
	if _, ok := manager.DecideAndReserve(now, "r1", managerCost(1000), []kvadmission.BackendSnapshot{managerBackend(now, 50000, 100)}); !ok {
		t.Fatal("reservation not created")
	}
	manager.Sweep(now.Add(time.Second))
	snapshot := manager.Snapshot()
	if snapshot.Reservations != 0 || snapshot.ExpiredTotal != 1 {
		t.Fatalf("snapshot=%#v want expired reservation removed", snapshot)
	}
}

func TestPreemptionCooldownPersistsAcrossSamples(t *testing.T) {
	now := time.Unix(100, 0)
	policy := kvadmission.DefaultPolicy()
	policy.DecodeDriftTokens = 0
	manager := New(policy)
	backend := managerBackend(now, 50000, 100)
	backend.PreemptionDelta = 1
	backend.PreemptionDeltaValid = true
	decision, _ := manager.DecideAndReserve(now, "r1", managerCost(100), []kvadmission.BackendSnapshot{backend})
	if decision.Reason != kvadmission.ReasonPreemptionCooldown {
		t.Fatalf("reason=%s want cooldown", decision.Reason)
	}
	backend.Updated = backend.Updated.Add(time.Second)
	backend.PreemptionDelta = 0
	decision, _ = manager.DecideAndReserve(now.Add(time.Second), "r2", managerCost(100), []kvadmission.BackendSnapshot{backend})
	if decision.Reason != kvadmission.ReasonPreemptionCooldown {
		t.Fatalf("reason=%s want persisted cooldown", decision.Reason)
	}
}

func BenchmarkDecisionReservation(b *testing.B) {
	now := time.Unix(100, 0)
	policy := kvadmission.DefaultPolicy()
	policy.DecodeDriftTokens = 0
	backend := managerBackend(now, 1000, 100)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		manager := New(policy)
		id := fmt.Sprintf("r%d", i)
		decision, ok := manager.DecideAndReserve(now, id, managerCost(100), []kvadmission.BackendSnapshot{backend})
		if decision.Reason != kvadmission.ReasonFit || !ok {
			b.Fatal("not reserved")
		}
		manager.Release(id)
	}
}
