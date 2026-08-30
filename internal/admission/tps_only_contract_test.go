package admission

import (
	"testing"
	"time"
)

func TestTPSOnlyControllerAdmitsValidRequestDemand(t *testing.T) {
	now := time.Unix(30_000, 0)
	controller := testControllerWithObservation(t, testObservation(now, 0, 0, 1, 0))

	result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if !result.Decision.Admitted() {
		t.Fatalf("valid TPS request demand was protected: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("TPS request reservation rollback failed")
	}
}

func TestTPSOnlyControllerRejectsInvalidDemandWithoutReservation(t *testing.T) {
	now := time.Unix(30_100, 0)
	controller := testControllerWithObservation(t, testObservation(now, 0, 0, 1, 0))

	result := controller.Admit(now.Add(time.Millisecond), TPSRequestDemand{})
	if result.Decision.Admitted() || result.Decision.Reason != ReasonInvalidRequest ||
		result.Decision.Scope != ProtectionRequest || result.Decision.ReservationID != 0 ||
		result.Handle.usable() {
		t.Fatalf("invalid TPS demand reached a reservation: %+v", result)
	}
}

func TestTPSOnlyControllerInitializationNeedsOnlyRuntimeIdentity(t *testing.T) {
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: "model-runtime-identity",
		TPS:             TPSPolicyConfig{Reference: 25},
	})
	if err != nil {
		t.Fatalf("TPS-only controller retained a resource startup dependency: %v", err)
	}
	controller.Close()
}

func TestTPSOnlyControllerObservationNeedsNoResourceTelemetry(t *testing.T) {
	now := time.Unix(30_200, 0)
	controller, err := NewAdmissionController(ControllerConfig{
		RuntimeIdentity: testRuntimeIdentity,
		TPS:             TPSPolicyConfig{Reference: 25},
	})
	if err != nil {
		t.Fatal(err)
	}
	publishObservation(t, controller, testObservation(now, 1, 0, 1, 0))
	result := controller.Admit(now.Add(time.Millisecond), testDemand(1))
	if !result.Decision.Admitted() {
		t.Fatalf("TPS-only admission unavailable without resource telemetry: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("observation fixture reservation rollback failed")
	}
}

func TestTPSOnlyControllerPressureHoldClearsOnFirstFreshObservation(t *testing.T) {
	for _, test := range []struct {
		name       string
		waiting    int64
		preemption uint64
		subreason  TPSDecisionSubreason
	}{
		{name: "waiting", waiting: DefaultWindowConcurrency, subreason: TPSDecisionSubreasonWaiting},
		{name: "preemption", preemption: 1, subreason: TPSDecisionSubreasonPreemption},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := time.Unix(30_500, 0)
			controller := testControllerWithTPSObservation(
				t,
				25,
				testObservation(start, 4, 0, 0, 0),
			)
			generation := uint64(0)
			for step := 1; step <= 4; step++ {
				generation += 50
				publishObservation(t, controller, testObservation(
					start.Add(time.Duration(step)*500*time.Millisecond), 4, 0, generation, 0,
				))
			}
			pressureAt := start.Add(2500 * time.Millisecond)
			generation += 50
			publishObservation(t, controller, testObservation(
				pressureAt, 4, test.waiting, generation, test.preemption,
			))
			protected := controller.Admit(pressureAt.Add(time.Millisecond), testDemand(1)).Decision
			if protected.Admitted() || protected.Reason != ReasonTPSReference ||
				protected.TPSDecisionSubreason != test.subreason {
				t.Fatalf("current pressure did not stop marginal admission: %+v", protected)
			}

			clearAt := start.Add(3 * time.Second)
			generation += 50
			publishObservation(t, controller, testObservation(
				clearAt, 3, 0, generation, test.preemption,
			))
			first := controller.Admit(clearAt.Add(time.Millisecond), testDemand(1))
			if !first.Decision.Admitted() || first.Decision.ProjectedRunning != 4 {
				t.Fatalf("first clear 500ms observation did not recover capacity: %+v", first.Decision)
			}
			second := controller.Admit(clearAt.Add(time.Millisecond), testDemand(1))
			if !second.Decision.Admitted() || second.Decision.ProjectedRunning != 5 {
				t.Fatalf("healthy backend retained the old one-at-a-time recovery cap: %+v", second.Decision)
			}
			if !first.Handle.Terminate(TerminalCancel) || !second.Handle.Terminate(TerminalCancel) {
				t.Fatal("recovery fixture reservation rollback failed")
			}
		})
	}
}
