package admission

import (
	"testing"
	"time"
)

const testRuntimeIdentity = "test-runtime-identity"

func testControllerWithObservation(
	t *testing.T,
	observation BackendObservation,
) *AdmissionController {
	t.Helper()
	return testControllerWithTPSObservation(t, 0, observation)
}

func testControllerWithTPSObservation(
	t *testing.T,
	reference float64,
	observation BackendObservation,
) *AdmissionController {
	t.Helper()
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		TPS:             TPSPolicyConfig{Reference: reference},
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, observation)
	return controller
}

func publishObservation(
	t *testing.T,
	controller *AdmissionController,
	observation BackendObservation,
) PublicationResult {
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

func testObservation(
	at time.Time,
	running, waiting int64,
	generation, preemptions uint64,
) BackendObservation {
	return BackendObservation{
		RuntimeIdentity:       testRuntimeIdentity,
		ObservedAt:            at,
		MaximumAge:            5 * time.Second,
		Running:               running,
		Waiting:               waiting,
		GenerationTokensTotal: generation,
		PreemptionsTotal:      preemptions,
	}
}

func testDemand(sequences int64) TPSRequestDemand {
	return NewTPSRequestDemand(sequences)
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
