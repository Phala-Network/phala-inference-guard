//go:build !race

package admission

import (
	"sort"
	"testing"
	"time"
)

func TestControllerHotPathIsConstantTimeAndAllocationFree(t *testing.T) {
	for _, reservations := range []int{256, 4_096} {
		t.Run(testReservationCountName(reservations), func(t *testing.T) {
			now := time.Unix(16_000, 0)
			controller := populatedController(t, reservations, now)
			protected := testEstimate(900_000, 900_000, 256)

			snapshotAllocations := testing.AllocsPerRun(1_000, func() {
				if snapshot := controller.Snapshot(now.Add(time.Millisecond)); snapshot.RuntimeEpoch == 0 {
					t.Fatal("invalid snapshot")
				}
			})
			admitAllocations := testing.AllocsPerRun(1_000, func() {
				if decision := controller.Admit(now.Add(time.Millisecond), protected).Decision; decision.Reason != ReasonInputLimit {
					t.Fatalf("protected decision=%+v", decision)
				}
			})
			if snapshotAllocations != 0 || admitAllocations != 0 {
				t.Fatalf("allocations snapshot=%g admit=%g", snapshotAllocations, admitAllocations)
			}

			const runs = 10_001
			snapshotDurations := make([]time.Duration, runs)
			admitDurations := make([]time.Duration, runs)
			for index := range snapshotDurations {
				started := time.Now()
				_ = controller.Snapshot(now.Add(time.Millisecond))
				snapshotDurations[index] = time.Since(started)
				started = time.Now()
				_ = controller.Admit(now.Add(time.Millisecond), protected)
				admitDurations[index] = time.Since(started)
			}
			sort.Slice(snapshotDurations, func(left, right int) bool { return snapshotDurations[left] < snapshotDurations[right] })
			sort.Slice(admitDurations, func(left, right int) bool { return admitDurations[left] < admitDurations[right] })
			snapshotP99 := snapshotDurations[(len(snapshotDurations)*99)/100]
			admitP99 := admitDurations[(len(admitDurations)*99)/100]
			t.Logf("reservations=%d snapshot_p99=%s admit_p99=%s snapshot_allocs=%g admit_allocs=%g", reservations, snapshotP99, admitP99, snapshotAllocations, admitAllocations)
			if snapshotP99 >= 100*time.Microsecond || admitP99 >= 100*time.Microsecond {
				t.Fatalf("reservations=%d snapshot/admit p99=%s/%s exceeds 100us", reservations, snapshotP99, admitP99)
			}
		})
	}
}

func populatedController(t *testing.T, reservations int, now time.Time) *AdmissionController {
	t.Helper()
	capability := testCapability()
	controller := testControllerWithObservation(t, capability, testObservation(capability, now, 0, 0, 0, 1, 0))
	estimate := testEstimate(1, 1, 0)
	for index := 0; index < reservations; index++ {
		result := controller.Admit(now.Add(time.Millisecond), estimate)
		if !result.Decision.Admitted() {
			t.Fatalf("populate admission %d=%+v", index, result.Decision)
		}
	}
	assertAggregateMatchesSlow(t, controller)
	return controller
}

func testReservationCountName(count int) string {
	if count == 256 {
		return "256"
	}
	return "4096"
}
