package requestaware

import (
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestSimulationSchedulerRunningAndWaitingAreDisjoint(t *testing.T) {
	runner := newSchedulerInvariantRunner(1)
	runner.arrive(0, schedulerInvariantRequest("first", 100))
	runner.arrive(0, schedulerInvariantRequest("second", 100))
	runner.poll(0)

	if runner.observed.running != 1 || runner.observed.waiting != 1 {
		t.Fatalf(
			"observed running/waiting=%d/%d want=1/1",
			runner.observed.running,
			runner.observed.waiting,
		)
	}
	if got := runner.observed.running + runner.observed.waiting; got != len(runner.active) {
		t.Fatalf("observed unfinished=%d active=%d", got, len(runner.active))
	}
}

func TestSimulationSchedulerWaitingRequestReceivesNoDecodeService(t *testing.T) {
	runner := newSchedulerInvariantRunner(1)
	runner.arrive(0, schedulerInvariantRequest("first", 100))
	runner.arrive(0, schedulerInvariantRequest("second", 100))

	runner.advance(simulationTick, simulationTick)

	if first := runner.active["first"]; first == nil || first.generated <= 0 {
		t.Fatalf("scheduled request did not receive Decode service: %+v", first)
	}
	if second := runner.active["second"]; second == nil || second.generated != 0 {
		t.Fatalf("waiting request received Decode service: %+v", second)
	}
}

func TestSimulationSchedulerWaitingRequestReceivesNoPrefillService(t *testing.T) {
	runner := newSchedulerInvariantRunner(1)
	first := schedulerInvariantRequest("first", 100)
	first.actualInput = 1_000
	second := schedulerInvariantRequest("second", 100)
	second.actualInput = 1_000
	runner.arrive(0, first)
	runner.arrive(0, second)

	runner.advance(simulationTick, simulationTick)

	if firstActive := runner.active["first"]; firstActive == nil || firstActive.prefillRemaining != 0 {
		t.Fatalf("scheduled request did not receive Prefill service: %+v", firstActive)
	}
	if secondActive := runner.active["second"]; secondActive == nil || secondActive.prefillRemaining != 1_000 {
		t.Fatalf("waiting request received Prefill service: %+v", secondActive)
	}
}

func TestSimulationSchedulerWaitingRequestDoesNotMaterializeKV(t *testing.T) {
	runner := newSchedulerInvariantRunner(1)
	first := schedulerInvariantRequest("first", 100)
	first.actualInput = 5_000
	second := schedulerInvariantRequest("second", 100)
	second.actualInput = 6_000
	runner.arrive(0, first)
	runner.arrive(0, second)

	runner.advance(simulationTick, simulationTick)

	if got := runner.trueKVTokens(); got != 5_000 {
		t.Fatalf("materialized KV=%d want=5000", got)
	}
}

func TestSimulationSchedulerPromotesWaitingRequestAfterSlotRelease(t *testing.T) {
	runner := newSchedulerInvariantRunner(1)
	runner.arrive(0, schedulerInvariantRequest("first", 1))
	runner.arrive(0, schedulerInvariantRequest("second", 100))

	runner.advance(simulationTick, simulationTick)
	if _, exists := runner.active["first"]; exists {
		t.Fatal("first request did not complete")
	}
	if second := runner.active["second"]; second == nil || second.generated != 0 {
		t.Fatalf("waiting request ran before the scheduler slot was released: %+v", second)
	}

	runner.advance(2*simulationTick, simulationTick)
	if second := runner.active["second"]; second == nil || second.generated <= 0 {
		t.Fatalf("waiting request was not promoted after slot release: %+v", second)
	}
}

func TestSimulationSchedulerPublishesDisjointCountsToController(t *testing.T) {
	runner := newCandidateSchedulerInvariantRunner(t, 1)
	runner.arrive(0, schedulerInvariantRequest("first", 100))
	runner.arrive(0, schedulerInvariantRequest("second", 100))

	runner.poll(0)

	snapshot := runner.controller.Snapshot(time.Unix(0, 0))
	if snapshot.State.RawRunning != 1 || snapshot.State.RawWaiting != 1 {
		t.Fatalf(
			"Controller running/waiting=%d/%d want=1/1",
			snapshot.State.RawRunning,
			snapshot.State.RawWaiting,
		)
	}
}

func TestSimulationSchedulerMaximumRunningCountsScheduledOnly(t *testing.T) {
	runner := newSchedulerInvariantRunner(1)
	runner.arrive(0, schedulerInvariantRequest("first", 100))
	runner.arrive(0, schedulerInvariantRequest("second", 100))

	if runner.metrics.MaximumRunning != 1 {
		t.Fatalf("maximum running=%d want=1", runner.metrics.MaximumRunning)
	}
}

func newSchedulerInvariantRunner(maximumNoWait int) *scenarioRunner {
	return &scenarioRunner{
		spec: scenarioSpec{
			duration:      time.Second,
			maximumNoWait: maximumNoWait,
		},
		policyName: PolicyNoAdmission,
		profile: runtimepredictive.BackendCapabilityProfile{
			KVHardLimitTokens: 1_000_000,
		},
		active: make(map[string]*activeRequest),
	}
}

func newCandidateSchedulerInvariantRunner(t *testing.T, maximumNoWait int) *scenarioRunner {
	t.Helper()
	spec := scenarioSpec{
		duration:       time.Second,
		maximumNoWait:  maximumNoWait,
		capacityTokens: 1_000_000,
	}
	profile, err := simulationCapabilityProfile(spec, 650*1024)
	if err != nil {
		t.Fatalf("construct scheduler capability: %v", err)
	}
	controller, err := coreadmission.NewAdmissionController(simulationAdmissionCapability(profile))
	if err != nil {
		t.Fatalf("construct scheduler Controller: %v", err)
	}
	runner := &scenarioRunner{
		spec:              spec,
		policyName:        PolicyCandidate,
		profile:           profile,
		controller:        controller,
		controllerHandles: make(map[string]coreadmission.ReservationHandle),
		active:            make(map[string]*activeRequest),
	}
	runner.publishControllerObservation(0, true)
	t.Cleanup(controller.Close)
	return runner
}

func schedulerInvariantRequest(id string, output float64) requestSpec {
	return requestSpec{
		id:             id,
		selectionInput: 64,
		safetyInput:    64,
		actualOutput:   output,
	}
}
