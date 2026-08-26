package admission

import (
	"testing"
	"time"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestTPSOnlyControllerDoesNotProtectOnInputSize(t *testing.T) {
	now := time.Unix(30_000, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, 0, 0, 0, 1, 0),
	)

	result := controller.Admit(
		now.Add(time.Millisecond),
		testEstimate(capability.MaximumInputTokens+1, capability.MaximumInputTokens+1, 256),
	)
	if !result.Decision.Admitted() {
		t.Fatalf("TPS-only admission changed because of input size: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("input-size invariant reservation rollback failed")
	}
}

func TestTPSOnlyControllerDoesNotProtectOnKVObservation(t *testing.T) {
	now := time.Unix(30_100, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, capability.KVHardLimitTokens-64, 0, 0, 1, 0),
	)

	result := controller.Admit(now.Add(time.Millisecond), testEstimate(1_024, 1_024, 256))
	if !result.Decision.Admitted() {
		t.Fatalf("TPS-only admission changed because of KV observation: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("KV invariant reservation rollback failed")
	}
}

func TestTPSOnlyControllerDoesNotProtectOnPrefillLiability(t *testing.T) {
	now := time.Unix(30_200, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, 0, 0, 0, 1, 0),
	)

	first := controller.Admit(now.Add(time.Millisecond), testEstimate(200*1024, 200*1024, 256))
	if !first.Decision.Admitted() {
		t.Fatalf("TPS-only prefill invariant setup admission: %+v", first.Decision)
	}
	defer first.Handle.Terminate(TerminalCancel)

	second := controller.Admit(now.Add(2*time.Millisecond), testEstimate(100*1024, 100*1024, 256))
	if !second.Decision.Admitted() {
		t.Fatalf("TPS-only admission changed because of Prefill liability: %+v", second.Decision)
	}
	if !second.Handle.Terminate(TerminalCancel) {
		t.Fatal("Prefill invariant reservation rollback failed")
	}
}

func TestTPSOnlyControllerFallsBackWhenDetailedTokenEstimateIsMissing(t *testing.T) {
	now := time.Unix(30_300, 0)
	capability := testCapability()
	controller := testControllerWithObservation(
		t,
		capability,
		testObservation(capability, now, 0, 0, 0, 1, 0),
	)

	result := controller.Admit(now.Add(time.Millisecond), predictive.RequestEstimate{})
	if !result.Decision.Admitted() {
		t.Fatalf("TPS-only admission did not use the one-sequence fallback: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("fallback reservation rollback failed")
	}
}

func TestTPSOnlyControllerInitializationNeedsOnlyRuntimeIdentity(t *testing.T) {
	controller, err := NewAdmissionController(ControllerConfig{
		Capability: Capability{Fingerprint: "model-runtime-identity"},
		TPS:        TPSPolicyConfig{Reference: 25},
	})
	if err != nil {
		t.Fatalf("TPS-only controller retained a KV/Prefill startup dependency: %v", err)
	}
	controller.Close()
}

func TestTPSOnlyControllerObservationDoesNotRequireKVOrCacheTelemetry(t *testing.T) {
	now := time.Unix(30_400, 0)
	capability := testCapability()
	controller, err := NewAdmissionController(ControllerConfig{
		Capability:  capability,
		WorkProfile: testRequestWorkProfile(),
		TPS:         TPSPolicyConfig{Reference: 25},
	})
	if err != nil {
		t.Fatal(err)
	}
	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("sample window unavailable")
	}
	publication := controller.PublishObservation(window, BackendObservation{
		CapabilityFingerprint: capability.Fingerprint,
		ObservedAt:            now,
		MaximumAge:            time.Second,
		Running:               1,
		GenerationTokensTotal: 1,
	})
	if !publication.Accepted {
		t.Fatalf("optional KV/cache telemetry made the TPS observation unavailable: %+v", publication)
	}
	result := controller.Admit(now.Add(time.Millisecond), predictive.RequestEstimate{})
	if !result.Decision.Admitted() {
		t.Fatalf("TPS-only admission unavailable without KV/cache telemetry: %+v", result.Decision)
	}
	if !result.Handle.Terminate(TerminalCancel) {
		t.Fatal("optional-telemetry fixture reservation rollback failed")
	}
}

func TestTPSOnlyControllerPressureHoldClearsOnFirstFreshObservation(t *testing.T) {
	for _, test := range []struct {
		name       string
		waiting    int64
		preemption uint64
		subreason  TPSDecisionSubreason
	}{
		{name: "waiting", waiting: 1, subreason: TPSDecisionSubreasonWaiting},
		{name: "preemption", preemption: 1, subreason: TPSDecisionSubreasonPreemption},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := time.Unix(30_500, 0)
			capability := testCapability()
			controller := testControllerWithTPSObservation(
				t,
				capability,
				25,
				testObservation(capability, start, 0, 4, 0, 0, 0),
			)
			generation := uint64(0)
			for step := 1; step <= 4; step++ {
				generation += 50
				publishObservation(t, controller, testObservation(
					capability,
					start.Add(time.Duration(step)*500*time.Millisecond),
					0,
					4,
					0,
					generation,
					0,
				))
			}
			pressureAt := start.Add(2500 * time.Millisecond)
			generation += 50
			publishObservation(t, controller, testObservation(
				capability,
				pressureAt,
				0,
				4,
				test.waiting,
				generation,
				test.preemption,
			))
			protected := controller.Admit(pressureAt.Add(time.Millisecond), predictive.RequestEstimate{}).Decision
			if protected.Admitted() || protected.Reason != ReasonTPSReference ||
				protected.TPSDecisionSubreason != test.subreason {
				t.Fatalf("current pressure did not stop marginal admission: %+v", protected)
			}

			clearAt := start.Add(3 * time.Second)
			generation += 50
			publishObservation(t, controller, testObservation(
				capability,
				clearAt,
				0,
				3,
				0,
				generation,
				test.preemption,
			))
			first := controller.Admit(clearAt.Add(time.Millisecond), predictive.RequestEstimate{})
			if !first.Decision.Admitted() || first.Decision.TPSCurrentSequences != 3 ||
				first.Decision.TPSPostAdmitSequences != 4 {
				t.Fatalf("first clear 500ms observation did not recover capacity: %+v", first.Decision)
			}
			second := controller.Admit(clearAt.Add(time.Millisecond), predictive.RequestEstimate{}).Decision
			if second.Admitted() || second.TPSCurrentSequences != 4 ||
				second.TPSPostAdmitSequences != 5 || second.ReservationID != 0 {
				t.Fatalf("same-snapshot reservation allowed excessive recovery: %+v", second)
			}
			if !first.Handle.Terminate(TerminalCancel) {
				t.Fatal("recovery fixture reservation rollback failed")
			}
		})
}
