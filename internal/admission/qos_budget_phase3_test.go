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
