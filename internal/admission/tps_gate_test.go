package admission

import "testing"

func TestTPSHealthGateDisabledIsOpen(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{}, DefaultWindowConcurrency)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.result != TPSDecisionResultDisabled ||
		decision.subreason != TPSDecisionSubreasonDisabled {
		t.Fatalf("disabled decision=%+v", decision)
	}
}

func TestTPSHealthGateRejectsInvalidEnabledSnapshot(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: TPSSnapshot{
		Enabled:   true,
		Reference: 25,
		Latest:    TPSIntervalSnapshot{Qualified: true},
	}}, DefaultWindowConcurrency)
	if decision.fits || decision.reason != ReasonResourceExhausted ||
		decision.result != TPSDecisionResultInvalid ||
		decision.subreason != TPSDecisionSubreasonInvalidState {
		t.Fatalf("invalid decision=%+v", decision)
	}
}

func TestTPSHealthGateTreatsConfirmedWaitingAndPreemptionAsPressure(t *testing.T) {
	for _, test := range []struct {
		name      string
		state     ProjectedState
		subreason TPSDecisionSubreason
	}{
		{name: "waiting", state: ProjectedState{
			RawWaiting: 1, PreviousRawWaiting: 1, ObservationIntervalValid: true,
		}, subreason: TPSDecisionSubreasonWaiting},
		{name: "preemption", state: ProjectedState{PreemptionDelta: 1}, subreason: TPSDecisionSubreasonPreemption},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := test.state
			state.TPS = healthyTPSSnapshot(25, 30, 30)
			decision := (tpsGate{}).evaluate(state, DefaultWindowConcurrency)
			if decision.fits || decision.reason != ReasonTPSReference ||
				decision.result != TPSDecisionResultProtect || decision.subreason != test.subreason {
				t.Fatalf("pressure decision=%+v", decision)
			}
		})
	}
}

func TestTPSHealthGateKeepsOneSubWindowWaitingSampleOpen(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{RawWaiting: 1}, 4)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.result != TPSDecisionResultDisabled ||
		decision.subreason != TPSDecisionSubreasonDisabled {
		t.Fatalf("transient waiting decision=%+v", decision)
	}
}

func TestTPSHealthGateProtectsWindowSizedWaitingImmediately(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{RawWaiting: 4}, 4)
	if decision.fits || decision.reason != ReasonTPSReference ||
		decision.result != TPSDecisionResultProtect ||
		decision.subreason != TPSDecisionSubreasonWaiting {
		t.Fatalf("window-sized waiting decision=%+v", decision)
	}
}

func TestTPSHealthGateDoesNotConfirmWaitingAcrossInvalidInterval(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{
		RawWaiting:               1,
		PreviousRawWaiting:       1,
		ObservationIntervalValid: false,
	}, 4)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.result != TPSDecisionResultDisabled ||
		decision.subreason != TPSDecisionSubreasonDisabled {
		t.Fatalf("non-adjacent waiting decision=%+v", decision)
	}
}

func TestTPSHealthGateWarmingAndNoCurrentEvidenceStayOpen(t *testing.T) {
	warming := (tpsGate{}).evaluate(ProjectedState{TPS: TPSSnapshot{
		Enabled:   true,
		Reference: 25,
	}}, DefaultWindowConcurrency)
	if !warming.fits || warming.subreason != TPSDecisionSubreasonWarming {
		t.Fatalf("warming decision=%+v", warming)
	}

	noCurrent := healthyTPSSnapshot(25, 30, 1)
	noCurrent.Latest = TPSIntervalSnapshot{}
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: noCurrent}, DefaultWindowConcurrency)
	if !decision.fits || decision.subreason != TPSDecisionSubreasonNoCurrentEvidence {
		t.Fatalf("no-current decision=%+v", decision)
	}
}

func TestTPSHealthGateKeepsHealthyRollingWindowOpenAcrossOneLowInterval(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: healthyTPSSnapshot(25, 30, 10)}, DefaultWindowConcurrency)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.subreason != TPSDecisionSubreasonHealthyWindow {
		t.Fatalf("healthy-window decision=%+v", decision)
	}
}

func TestTPSHealthGateReopensImmediatelyFromQualifiedCurrentRecovery(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: healthyTPSSnapshot(25, 20, 30)}, DefaultWindowConcurrency)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.subreason != TPSDecisionSubreasonRecoveredCurrent {
		t.Fatalf("recovered-current decision=%+v", decision)
	}
}

func TestTPSHealthGateProtectsOnlyWhenRollingAndCurrentAreBelowReference(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: healthyTPSSnapshot(25, 20, 10)}, DefaultWindowConcurrency)
	if decision.fits || decision.reason != ReasonTPSReference ||
		decision.result != TPSDecisionResultProtect ||
		decision.subreason != TPSDecisionSubreasonBelowReference {
		t.Fatalf("below-reference decision=%+v", decision)
	}
}

func TestV01223HealthyWindowDoesNotDeriveConcurrencyCapacity(t *testing.T) {
	state := ProjectedState{
		RawRunning:          200,
		UnobservedSequences: 32,
		SequenceLiabilities: 32,
		TPS:                 healthyTPSSnapshot(25, 71.4, 60),
	}
	decision := (tpsGate{}).evaluate(state, DefaultWindowConcurrency)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.subreason != TPSDecisionSubreasonHealthyWindow {
		t.Fatalf("healthy TPS was turned into capacity=%+v", decision)
	}
}

func healthyTPSSnapshot(reference, rollingMean, currentMean float64) TPSSnapshot {
	return TPSSnapshot{
		Enabled:                  true,
		Ready:                    true,
		Reference:                reference,
		QualifiedSamples:         20,
		QualifiedTokens:          2_000,
		QualifiedActiveSeconds:   10,
		QualifiedSequenceSamples: 20,
		QualifiedSequenceTokens:  rollingMean * 100,
		QualifiedSequenceSeconds: 100,
		AggregateTPS:             200,
		MeanActiveTPS:            rollingMean,
		Latest: TPSIntervalSnapshot{
			Qualified:       true,
			Tokens:          100,
			DurationSeconds: 1,
			SequenceSeconds: 100 / currentMean,
			AggregateTPS:    100,
			MeanActiveTPS:   currentMean,
		},
	}
}
