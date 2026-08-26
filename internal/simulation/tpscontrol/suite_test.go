package tpscontrol

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDefaultSuiteIsDeterministicAndDiagnosticOnly(t *testing.T) {
	first, err := RunDefaultSuite()
	if err != nil {
		t.Fatal(err)
	}
	second, err := RunDefaultSuite()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("default suite is not deterministic")
	}
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	if first.Contract != "diagnostic_only" ||
		strings.Contains(string(encoded), `"acceptance"`) ||
		strings.Contains(string(encoded), `"passed"`) {
		t.Fatalf("simulation report became a release oracle: %s", encoded)
	}
}

func TestDefaultSuiteExercisesRecoveryAtomicityAndLifecycle(t *testing.T) {
	suite, err := RunDefaultSuite()
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range suite.Scenarios {
		summary := result.Summary
		if summary.FinalLiveReservations != 0 || summary.FinalUnobserved != 0 ||
			summary.FinalLiabilities != 0 || summary.FinalResidualDebts != 0 {
			t.Fatalf("scenario %s leaked controller state: %+v", result.Name, summary)
		}
	}

	healthy := scenarioByName(t, suite, "healthy_window_throughput")
	requireArrival(t, healthy, "healthy-probe", "admit", "healthy_window", 3, 1, 32)

	batch := scenarioByName(t, suite, "same_snapshot_batch_reservation")
	requireArrival(t, batch, "batch-32", "admit", "healthy_window", 34, 32, 32)
	requireArrival(t, batch, "same-snapshot-extra", "protect", "healthy_window", 35, 33, 32)

	waiting := scenarioByName(t, suite, "waiting_clear")
	requireArrival(t, waiting, "waiting_clear-protected", "protect", "waiting", 0, 0, 32)
	requireArrival(t, waiting, "waiting_clear-recovered", "admit", "healthy_window", 4, 1, 32)

	preemption := scenarioByName(t, suite, "preemption_clear")
	requireArrival(t, preemption, "preemption_clear-protected", "protect", "preemption", 0, 0, 32)
	requireArrival(t, preemption, "preemption_clear-recovered", "admit", "healthy_window", 4, 1, 32)

	lowFlow := scenarioByName(t, suite, "low_flow_no_self_lock")
	if lowFlow.Summary.Arrivals != 2 || lowFlow.Summary.Admitted != 2 || lowFlow.Summary.Protected != 0 {
		t.Fatalf("low-flow scenario self-locked: %+v", lowFlow.Summary)
	}

	reset := scenarioByName(t, suite, "runtime_reset_fences_old_handle")
	if reset.Summary.RuntimeResets != 1 || reset.Summary.LifecycleRejected != 1 {
		t.Fatalf("runtime reset did not fence exactly one old handle: %+v", reset.Summary)
	}
}

func TestObservedDegradationAndStalenessDoNotGrantMarginalIntake(t *testing.T) {
	suite, err := RunDefaultSuite()
	if err != nil {
		t.Fatal(err)
	}
	degraded := scenarioByName(t, suite, "observed_output_degradation")
	for _, requestID := range []string{"zero-output-stall", "degraded-output"} {
		step := arrivalByID(t, degraded, requestID)
		if step.Action != "protect" || step.Reason != "tps_reference" || step.ReservationID != 0 {
			t.Fatalf("degradation arrival %s was not protected pre-forward: %+v", requestID, step)
		}
	}
	stale := scenarioByName(t, suite, "stale_observation_recovery")
	protected := arrivalByID(t, stale, "stale-protected")
	if protected.Action != "protect" || protected.Reason != "observation_stale" || protected.ReservationID != 0 {
		t.Fatalf("stale observation admission=%+v", protected)
	}
	recovered := arrivalByID(t, stale, "fresh-recovered")
	if recovered.Action != "admit" || recovered.ReservationID == 0 {
		t.Fatalf("fresh observation did not recover admission: %+v", recovered)
	}
}

func scenarioByName(t *testing.T, suite Suite, name string) ScenarioResult {
	t.Helper()
	for _, result := range suite.Scenarios {
		if result.Name == name {
			return result
		}
	}
	t.Fatalf("scenario %q is missing", name)
	return ScenarioResult{}
}

func arrivalByID(t *testing.T, result ScenarioResult, requestID string) StepResult {
	t.Helper()
	for _, step := range result.Steps {
		if step.Kind == string(EventArrival) && step.RequestID == requestID {
			return step
		}
	}
	t.Fatalf("scenario %s arrival %q is missing", result.Name, requestID)
	return StepResult{}
}

func requireArrival(
	t *testing.T,
	result ScenarioResult,
	requestID, action, subreason string,
	projectedRunning, projectedWindow, windowLimit int64,
) {
	t.Helper()
	step := arrivalByID(t, result, requestID)
	if step.Action != action || step.TPSSubreason != subreason ||
		step.ProjectedRunning != projectedRunning || step.ProjectedWindowSequences != projectedWindow ||
		step.WindowConcurrency != windowLimit || (action == "admit") != (step.ReservationID > 0) {
		t.Fatalf("scenario %s arrival %s=%+v", result.Name, requestID, step)
	}
}
