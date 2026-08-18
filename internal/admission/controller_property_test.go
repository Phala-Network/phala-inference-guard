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

func TestControllerDeterministicLifecyclePropertyKeepsAggregateAndKVExact(t *testing.T) {
	now := time.Unix(17_000, 0)
	clock := &manualAdmissionClock{at: now}
	capability := testCapability()
	controller, err := NewAdmissionController(ControllerConfig{
		Capability: capability, WorkProfile: testRequestWorkProfile(), Now: clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(capability, now, 0, 0, 0, 1, 0))
	random := rand.New(rand.NewSource(0x504947_0120))
	handles := make(map[uint64]propertyHandle)
	generation := uint64(1)

	for step := 0; step < 5_000; step++ {
		stepNow := now.Add(time.Duration(step+1) * time.Millisecond)
		clock.Set(stepNow)
		switch random.Intn(5) {
		case 0:
			selection := int64(random.Intn(8*1024) + 1)
			reservation := selection + selection/2 + 1
			result := controller.Admit(
				stepNow,
				testEstimate(selection, reservation, int64(random.Intn(257))),
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
				if !item.handle.Terminate(TerminalSuccess) {
					t.Fatalf("step %d terminal failed id=%d", step, id)
				}
				delete(handles, id)
			}
		case 4:
			generation++
			window, ok := controller.StartSampleWindow()
			if !ok {
				t.Fatalf("step %d sample window unavailable", step)
			}
			controller.mu.Lock()
			exposureDelta, exposureOK := window.exposure.subtract(controller.lastExposure)
			_, _, reconciliationOK := controller.reconciledOverlayLocked(window.eventSequence)
			ledgerBefore := controller.exposure
			lastExposureBefore := controller.lastExposure
			controller.mu.Unlock()
			publication := controller.PublishObservation(window, testObservation(
				capability,
				stepNow,
				0,
				0,
				0,
				generation,
				0,
			))
			if !publication.Accepted {
				t.Fatalf(
					"step %d publication=%+v exposure_ok=%t delta=%+v window=%+v last=%+v ledger=%+v reconciliation_ok=%t",
					step,
					publication,
					exposureOK,
					exposureDelta,
					window.exposure,
					lastExposureBefore,
					ledgerBefore,
					reconciliationOK,
				)
			}
		}
		assertAggregateMatchesSlow(t, controller)
		snapshot := controller.Snapshot(stepNow)
		if snapshot.State.EffectiveKVTokens > capability.KVHardLimitTokens {
			t.Fatalf("step %d effective KV=%d exceeds hard=%d", step, snapshot.State.EffectiveKVTokens, capability.KVHardLimitTokens)
		}
	}
}

func randomPropertyHandle(random *rand.Rand, handles map[uint64]propertyHandle) (uint64, propertyHandle, bool) {
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
