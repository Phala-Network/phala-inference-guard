package requestaware

import (
	"fmt"
	"testing"
	"time"
)

func TestSimulationWorkerPoolImmediatelyReleasesAfterReject(t *testing.T) {
	requests := make([]requestSpec, 0, 3)
	for index := 0; index < 3; index++ {
		requests = append(requests, requestSpec{
			id:             fmt.Sprintf("rejected-%d", index),
			selectionInput: 1_000,
			safetyInput:    1_000,
			decodeHorizon:  256,
			actualInput:    1_000,
			actualOutput:   1,
		})
	}
	scenario := scenarioSpec{
		name: "worker-reject-release", category: "test", duration: time.Second,
		initialKVTokens: 89_900,
		workerPools:     []workerPoolSpec{{at: 100 * time.Millisecond, concurrency: 1, requests: requests}},
	}
	profile, policy, err := simulationCapabilityPolicy(scenario, 650*1024)
	if err != nil {
		t.Fatal(err)
	}
	metrics, _, err := runScenario(scenario, PolicyV0122, profile, policy)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Arrivals != 3 || metrics.Admitted != 0 || metrics.Rejected != 3 {
		t.Fatalf("worker reject release metrics=%+v", metrics)
	}
}

func TestSustainedReplacementWaveUsesC21CapabilityGeometry(t *testing.T) {
	scenario := newSustainedReplacementWaveScenario()
	profile, _, err := simulationCapabilityPolicy(scenario, scenarioMaxModelLen(scenario))
	if err != nil {
		t.Fatal(err)
	}
	if profile.PrefillRegularTokens != 64*1024 || profile.PrefillExclusiveTokens != 256*1024 ||
		profile.PrefillQuiescentTokens != 512*1024 ||
		profile.MaximumAdmissibleInputTokens != 256*1024-256 {
		t.Fatalf("sustained replacement profile=%+v, want fixed bands with c21 reachability", profile)
	}
}

func TestSimulationWorkerPoolImmediatelyReleasesAfterCompletion(t *testing.T) {
	requests := []requestSpec{
		{id: "first", selectionInput: 64, safetyInput: 64, actualInput: 1, actualOutput: 1},
		{id: "second", selectionInput: 64, safetyInput: 64, actualInput: 1, actualOutput: 1},
	}
	scenario := scenarioSpec{
		name: "worker-completion-release", category: "test", duration: time.Second,
		workerPools: []workerPoolSpec{{at: 100 * time.Millisecond, concurrency: 1, requests: requests}},
	}
	profile, policy, err := simulationCapabilityPolicy(scenario, 650*1024)
	if err != nil {
		t.Fatal(err)
	}
	metrics, _, err := runScenario(scenario, PolicyNoAdmission, profile, policy)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Arrivals != 2 || metrics.Admitted != 2 || metrics.Completed != 2 {
		t.Fatalf("worker completion release metrics=%+v", metrics)
	}
}

func TestSimulationWorkerPoolDoesNotReleaseAtScenarioEnd(t *testing.T) {
	requests := []requestSpec{
		{id: "finishes-at-end", selectionInput: 1, safetyInput: 1, actualInput: 1, actualOutput: 1},
		{id: "must-not-start", selectionInput: 1, safetyInput: 1, actualInput: 1, actualOutput: 1},
	}
	scenario := scenarioSpec{
		name: "worker-end-boundary", category: "test", duration: 200 * time.Millisecond,
		workerPools: []workerPoolSpec{{at: 100 * time.Millisecond, concurrency: 1, requests: requests}},
	}
	profile, policy, err := simulationCapabilityPolicy(scenario, 650*1024)
	if err != nil {
		t.Fatal(err)
	}
	metrics, _, err := runScenario(scenario, PolicyNoAdmission, profile, policy)
	if err != nil {
		t.Fatal(err)
	}
	if metrics.Arrivals != 1 || metrics.Admitted != 1 || metrics.Completed != 1 {
		t.Fatalf("worker end-boundary metrics=%+v", metrics)
	}
}
