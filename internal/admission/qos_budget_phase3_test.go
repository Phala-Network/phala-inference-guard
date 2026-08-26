package admission

import (
	"testing"
	"time"
)

func TestQoSBudgetCoversOnlyTheNextObservationInterval(t *testing.T) {
	state := qosBudgetTestState()
	limit, leased, subreason := qosBudgetSequenceLimit(
		state,
		7,
		7,
		tpsAdmissionDemand{additionalSequences: 1},
	)
	if limit != 8 || !leased || subreason != TPSDecisionSubreasonQoSBudgetGranted {
		t.Fatalf("next-observation surplus decision=%d/%t/%s", limit, leased, subreason)
	}

	state.TPS.QualifiedSequenceTokens = 2_001
	limit, leased, subreason = qosBudgetSequenceLimit(
		state,
		7,
		7,
		tpsAdmissionDemand{additionalSequences: 1},
	)
	if limit != 7 || leased || subreason != TPSDecisionSubreasonQoSBudgetNoSurplus {
		t.Fatalf("insufficient next-observation surplus decision=%d/%t/%s", limit, leased, subreason)
	}
}

func TestQoSBudgetDoesNotExpandDuringCurrentTPSDegradation(t *testing.T) {
	state := qosBudgetTestState()
	state.GenerationDelta = 60
	limit, leased, subreason := qosBudgetSequenceLimit(
		state,
		7,
		7,
		tpsAdmissionDemand{additionalSequences: 1},
	)
	if limit != 7 || leased || subreason != TPSDecisionSubreasonQoSBudgetCurrentRate {
		t.Fatalf("degraded current-rate decision=%d/%t/%s", limit, leased, subreason)
	}
}

func qosBudgetTestState() ProjectedState {
	return ProjectedState{
		RawRunning:               7,
		GenerationDelta:          75,
		ObservationInterval:      500 * time.Millisecond,
		ObservationIntervalValid: true,
		TPS: TPSSnapshot{
			Enabled: true, Ready: true, Reference: 20,
			QualifiedSamples: 20, QualifiedTokens: 2_400,
			QualifiedSequenceSamples: 20, QualifiedSequenceTokens: 2_400,
			QualifiedSequenceSeconds: 100, AggregateTPS: 150, MeanActiveTPS: 24,
		},
	}
}
