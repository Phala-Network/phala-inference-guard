package admission

import (
	"math/rand"
	"sort"
	"testing"
	"time"
)

type propertyHandle struct {
	handle    ReservationHandle
	forwarded bool
	active    bool
}

func TestControllerDeterministicLifecyclePropertyKeepsAggregateExact(t *testing.T) {
	now := time.Unix(17_000, 0)
	clock := &manualAdmissionClock{at: now}
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(now, 0, 0, 1, 0))
	random := rand.New(rand.NewSource(0x504947_0122))
	handles := make(map[uint64]propertyHandle)
	generation := uint64(1)

	for step := 0; step < 5_000; step++ {
		stepNow := now.Add(time.Duration(step+1) * time.Millisecond)
		clock.Set(stepNow)
		switch random.Intn(5) {
		case 0:
			result := controller.Admit(
				stepNow,
				testDemand(int64(random.Intn(4)+1)),
			)
			if result.Decision.Admitted() {
				handles[result.Decision.ReservationID] = propertyHandle{handle: result.Handle}
			}
		case 1:
			id, item, ok := randomPropertyHandle(random, handles)
			if ok && !item.forwarded {
				if !item.handle.MarkForwarded() {
					t.Fatalf("step %d forward failed id=%d", step, id)
				}
				item.forwarded = true
				handles[id] = item
			}
		case 2:
			id, item, ok := randomPropertyHandle(random, handles)
			if ok && item.forwarded && !item.active {
				if !item.handle.MarkFirstByte() {
					t.Fatalf("step %d first byte failed id=%d", step, id)
				}
				item.active = true
				handles[id] = item
			}
		case 3:
			id, item, ok := randomPropertyHandle(random, handles)
			if ok {
				causes := []TerminalCause{TerminalSuccess, TerminalError, TerminalCancel, TerminalDisconnect, TerminalTimeout}
				if !item.handle.Terminate(causes[random.Intn(len(causes))]) {
					t.Fatalf("step %d terminal failed id=%d", step, id)
				}
				delete(handles, id)
			}
		case 4:
			generation++
			publishObservation(t, controller, testObservation(stepNow, 0, 0, generation, 0))
		}
		assertAggregateMatchesSlow(t, controller)
		snapshot := controller.Snapshot(stepNow)
		if snapshot.State.UnobservedSequences > snapshot.State.SequenceLiabilities ||
			snapshot.State.QoSBudgetLeases > snapshot.State.LiveReservations+snapshot.State.ResidualDebts {
			t.Fatalf("step %d invalid aggregate state=%+v", step, snapshot.State)
		}
	}
}

func randomPropertyHandle(
	random *rand.Rand,
	handles map[uint64]propertyHandle,
) (uint64, propertyHandle, bool) {
	if len(handles) == 0 {
		return 0, propertyHandle{}, false
	}
	ids := make([]uint64, 0, len(handles))
	for id := range handles {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(left, right int) bool { return ids[left] < ids[right] })
	id := ids[random.Intn(len(ids))]
	return id, handles[id], true
}
