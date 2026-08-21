package admission

import (
	"testing"
	"time"
)

func TestV01218BoundedQoSBudgetSimulationChangesOnlyForecastLifetime(t *testing.T) {
	state := ProjectedState{
		RawRunning:               7,
		GenerationDelta:          75,
		ObservationInterval:      500 * time.Millisecond,
		ObservationIntervalValid: true,
		TPS: TPSSnapshot{
			Enabled: true, Ready: true, Reference: 20,
			QualifiedSamples: 20, QualifiedTokens: 2_400,
			QualifiedSequenceSamples: 20, QualifiedSequenceTokens: 2_400,
			QualifiedSequenceSeconds: 100,
			AggregateTPS:             150,
			MeanActiveTPS:            24,
		},
	}
	knownLongLimit := tpsAdmissionDemand{
		additionalSequences: 1,
		outputLimitTokens:   95_000,
		outputLimitKnown:    true,
	}
	unknownLimit := tpsAdmissionDemand{additionalSequences: 1}

	for _, demand := range []tpsAdmissionDemand{knownLongLimit, unknownLimit} {
		current := (tpsGate{}).evaluateAdditional(state, demand)
		if current.fits || current.qosBudgeted || current.sequenceLimit != 7 {
			t.Fatalf("complete declared-lifetime policy unexpectedly admitted: %+v", current)
		}

		bounded := (tpsGate{qosBudget: qosBudgetForecast{
			controlHorizon: 30 * time.Second,
		}}).evaluateAdditional(state, demand)
		if !bounded.fits || !bounded.qosBudgeted || bounded.sequenceLimit != 8 ||
			bounded.subreason != TPSDecisionSubreasonQoSBudgetGranted {
			t.Fatalf("bounded control horizon did not grant one marginal lease: %+v", bounded)
		}
	}
}

func TestV01218ProductionControllerUsesBoundedTPSDebtHorizon(t *testing.T) {
	for _, test := range []struct {
		name       string
		knownLimit bool
	}{
		{name: "large declared output", knownLimit: true},
		{name: "unknown output"},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := time.Unix(11_900, 0)
			clock := &manualAdmissionClock{at: start}
			capability := testCapability()
			controller, err := NewAdmissionController(ControllerConfig{
				Capability: capability, WorkProfile: testRequestWorkProfile(),
				TPS: TPSPolicyConfig{Reference: 20}, Now: clock.Now,
			})
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()

			publishObservation(t, controller, testObservation(capability, start, 0, 7, 0, 0, 0))
			for step := 1; step <= 4; step++ {
				at := start.Add(time.Duration(step) * 500 * time.Millisecond)
				clock.Set(at)
				publishObservation(t, controller, testObservation(
					capability,
					at,
					0,
					7,
					0,
					uint64(step*75),
					0,
				))
			}

			estimate := testEstimate(1, 1, 16)
			if test.knownLimit {
				estimate.OutputLimitTokens = 95_000
				estimate.OutputLimitKnown = true
			}
			admitted := controller.Admit(clock.Now(), estimate)
			if !admitted.Decision.Admitted() || !admitted.Decision.TPSQoSBudgeted ||
				admitted.Decision.TPSSequenceLimit != 8 ||
				admitted.Decision.TPSDecisionSubreason != TPSDecisionSubreasonQoSBudgetGranted {
				t.Fatalf("production Controller did not use bounded TPS debt: %+v", admitted.Decision)
			}
		})
	}
}

func TestV01218BoundedQoSBudgetSimulationStillRequiresEnoughHorizonSurplus(t *testing.T) {
	state := ProjectedState{
		RawRunning:               7,
		GenerationDelta:          75,
		ObservationInterval:      500 * time.Millisecond,
		ObservationIntervalValid: true,
		TPS: TPSSnapshot{
			Enabled: true, Ready: true, Reference: 20,
			QualifiedSamples: 20, QualifiedTokens: 2_400,
			QualifiedSequenceSamples: 20, QualifiedSequenceTokens: 2_400,
			QualifiedSequenceSeconds: 100,
			AggregateTPS:             150,
			MeanActiveTPS:            24,
		},
	}
	decision := (tpsGate{qosBudget: qosBudgetForecast{
		controlHorizon: 60 * time.Second,
	}}).evaluateAdditional(state, tpsAdmissionDemand{additionalSequences: 1})
	if decision.fits || decision.qosBudgeted || decision.sequenceLimit != 7 ||
		decision.subreason != TPSDecisionSubreasonQoSBudgetLifetime {
		t.Fatalf("bounded horizon spent surplus it does not have: %+v", decision)
	}
}

func TestV01218BoundedQoSBudgetLeaseDoesNotExpireAtSoftHorizon(t *testing.T) {
	for _, test := range []struct {
		name       string
		cause      TerminalCause
		knownLimit bool
	}{
		{name: "success before next poll", cause: TerminalSuccess, knownLimit: true},
		{name: "error", cause: TerminalError, knownLimit: false},
		{name: "disconnect", cause: TerminalDisconnect, knownLimit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			start := time.Unix(12_000, 0)
			clock := &manualAdmissionClock{at: start}
			capability := testCapability()
			controller, err := NewBoundedTPSDebtSimulationController(ControllerConfig{
				Capability: capability, WorkProfile: testRequestWorkProfile(),
				TPS: TPSPolicyConfig{Reference: 20}, Now: clock.Now,
			}, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer controller.Close()

			publishObservation(t, controller, testObservation(capability, start, 0, 7, 0, 0, 0))
			for step := 1; step <= 4; step++ {
				at := start.Add(time.Duration(step) * 500 * time.Millisecond)
				clock.Set(at)
				publishObservation(t, controller, testObservation(
					capability,
					at,
					0,
					7,
					0,
					uint64(step*75),
					0,
				))
			}

			estimate := testEstimate(1, 1, 16)
			if test.knownLimit {
				estimate.OutputLimitTokens = 95_000
				estimate.OutputLimitKnown = true
			}
			admitted := controller.Admit(clock.Now(), estimate)
			if !admitted.Decision.Admitted() || !admitted.Decision.TPSQoSBudgeted ||
				!admitted.Handle.MarkForwarded() || !admitted.Handle.MarkFirstByte() {
				t.Fatalf("bounded lease was not created: %+v", admitted.Decision)
			}

			clock.Set(clock.Now().Add(1500 * time.Millisecond))
			if !admitted.Handle.Terminate(test.cause) {
				t.Fatalf("bounded lease terminal cause %q was rejected", test.cause)
			}
			debt := controller.Snapshot(clock.Now()).State
			if debt.QoSBudgetLeases != 1 || debt.ResidualDebts != 1 {
				t.Fatalf("soft horizon expired terminal liability: %+v", debt)
			}
			if duplicate := controller.Admit(clock.Now(), estimate).Decision; duplicate.Admitted() ||
				duplicate.State.QoSBudgetLeases != 1 {
				t.Fatalf("rolling surplus was spent twice: %+v", duplicate)
			}

			coveredAt := start.Add(4 * time.Second)
			clock.Set(coveredAt)
			publishObservation(t, controller, testObservation(capability, coveredAt, 0, 7, 0, 600, 0))
			covered := controller.Snapshot(clock.Now()).State
			if covered.QoSBudgetLeases != 0 || covered.ResidualDebts != 0 {
				t.Fatalf("covering observation retained terminal liability: %+v", covered)
			}
		})
	}
}
