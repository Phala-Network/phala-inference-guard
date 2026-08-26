package admission

import (
	"testing"
	"time"
)

func TestTPSGateDisabledDoesNotChangeAdmissionOrRunSequenceProjection(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{RawRunning: 100})
	if !decision.fits || decision.reason != ReasonOpen || decision.sequenceLimit != 0 ||
		decision.currentSequences != 0 || decision.postAdmitSequences != 0 {
		t.Fatalf("disabled decision=%+v", decision)
	}
}

func TestTPSGateWarmingAllowsBoundedColdStartsButNotAnUnboundedBurst(t *testing.T) {
	snapshot := TPSSnapshot{Enabled: true, Reference: 20}
	for _, test := range []struct {
		state   ProjectedState
		fits    bool
		current int64
		post    int64
		limit   int64
	}{
		{state: ProjectedState{}, fits: true, current: 0, post: 1, limit: 2},
		{state: ProjectedState{UnobservedSequences: 1, SequenceLiabilities: 1}, fits: true, current: 1, post: 2, limit: 2},
		{state: ProjectedState{UnobservedSequences: 2, SequenceLiabilities: 2}, fits: false, current: 2, post: 3, limit: 2},
		{state: ProjectedState{RawRunning: 2}, fits: true, current: 2, post: 3, limit: 3},
		{state: ProjectedState{RawRunning: 2, UnobservedSequences: 1}, fits: false, current: 3, post: 4, limit: 3},
		{state: ProjectedState{RawRunning: 2, RawWaiting: 1}, fits: false, current: 3, post: 4, limit: 3},
		{state: ProjectedState{RawRunning: 100}, fits: false, current: 100, post: 101, limit: 100},
	} {
		state := test.state
		state.TPS = snapshot
		decision := (tpsGate{}).evaluate(state)
		if decision.fits != test.fits || decision.currentSequences != test.current ||
			decision.postAdmitSequences != test.post || decision.sequenceLimit != test.limit {
			t.Fatalf("warming decision=%+v state=%+v", decision, state)
		}
		if !test.fits && decision.reason != ReasonTPSReference {
			t.Fatalf("warming protection reason=%s", decision.reason)
		}
	}
}

func TestTPSGateIdleProbeDoesNotBypassCompleteBatchFanout(t *testing.T) {
	decision := (tpsGate{}).evaluateAdditional(
		ProjectedState{TPS: TPSSnapshot{
			Enabled: true, Ready: true, Reference: 20,
			QualifiedSamples: 4, QualifiedSequenceSeconds: 8,
			AggregateTPS: 20, MeanActiveTPS: 20,
		}},
		tpsAdmissionDemand{additionalSequences: 3},
	)
	if decision.fits || decision.reason != ReasonTPSReference ||
		decision.currentSequences != 0 || decision.postAdmitSequences != 3 ||
		decision.sequenceLimit != 1 {
		t.Fatalf("idle batch bypassed ready base limit: %+v", decision)
	}
}

func TestTPSGateUsesStrictRateDerivedBaseWithoutRequestBudget(t *testing.T) {
	gate := tpsGate{}
	healthy := TPSSnapshot{Enabled: true, Ready: true, Reference: 20, AggregateTPS: 210, MeanActiveTPS: 25}
	protected := gate.evaluate(ProjectedState{RawRunning: 10, TPS: healthy})
	if protected.fits || protected.reason != ReasonTPSReference || protected.sequenceLimit != 10 ||
		protected.currentSequences != 10 || protected.postAdmitSequences != 11 {
		t.Fatalf("strict base protection=%+v", protected)
	}
	protected = gate.evaluate(ProjectedState{RawRunning: 11, TPS: healthy})
	if protected.fits || protected.reason != ReasonTPSReference || protected.sequenceLimit != 10 || protected.postAdmitSequences != 12 {
		t.Fatalf("healthy protection=%+v", protected)
	}

	below := TPSSnapshot{Enabled: true, Ready: true, Reference: 20, AggregateTPS: 159, MeanActiveTPS: 19}
	protected = gate.evaluate(ProjectedState{RawRunning: 7, TPS: below})
	if protected.fits || protected.sequenceLimit != 7 || protected.reason != ReasonTPSReference {
		t.Fatalf("below-reference protection=%+v", protected)
	}
}

func TestV01215TPSGateKeepsLongWindowCapacityAcrossOneLowCurrentSample(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 50,
		QualifiedSamples: 60, QualifiedSequenceSeconds: 240,
		AggregateTPS: 300, MeanActiveTPS: 75,
	}
	for unobserved := int64(0); unobserved <= 3; unobserved++ {
		state := ProjectedState{
			RawRunning:               3,
			UnobservedSequences:      unobserved,
			GenerationDelta:          60,
			ObservationInterval:      500 * time.Millisecond,
			ObservationIntervalValid: true,
			TPS:                      snapshot,
		}
		decision := (tpsGate{}).evaluate(state)
		wantFit := unobserved < 3
		if decision.fits != wantFit || decision.sequenceLimit != 6 ||
			decision.currentSequences != 3+unobserved ||
			decision.postAdmitSequences != 4+unobserved {
			t.Fatalf("unobserved=%d long-window decision=%+v want fit/limit=%t/6", unobserved, decision, wantFit)
		}
		if !wantFit && decision.reason != ReasonTPSReference {
			t.Fatalf("long-window overflow reason=%s", decision.reason)
		}
	}
}

func TestTPSGateDoesNotExploreWhenBasePlusOneWouldExceedTolerance(t *testing.T) {
	snapshot := TPSSnapshot{Enabled: true, Ready: true, Reference: 20, AggregateTPS: 150, MeanActiveTPS: 22}
	decision := (tpsGate{}).evaluate(ProjectedState{RawRunning: 7, TPS: snapshot})
	if decision.fits || decision.reason != ReasonTPSReference || decision.sequenceLimit != 7 ||
		decision.postAdmitSequences != 8 {
		t.Fatalf("unsafe long-lived exploration=%+v", decision)
	}
}

func TestTPSGateUsesCurrentRateForOneBoundedWarmingStep(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Reference: 20,
		QualifiedSamples: 1, QualifiedTokens: 75,
		QualifiedActiveSeconds: 0.5, QualifiedSequenceSeconds: 2,
		AggregateTPS: 150, MeanActiveTPS: 37.5,
	}
	for unobserved := int64(0); unobserved <= 1; unobserved++ {
		state := ProjectedState{
			RawRunning:               4,
			UnobservedSequences:      unobserved,
			GenerationDelta:          75,
			ObservationInterval:      500 * time.Millisecond,
			ObservationIntervalValid: true,
			TPS:                      snapshot,
		}
		decision := (tpsGate{}).evaluate(state)
		wantFit := unobserved == 0
		if decision.fits != wantFit || decision.sequenceLimit != 5 ||
			decision.currentSequences != 4+unobserved ||
			decision.postAdmitSequences != 5+unobserved {
			t.Fatalf("unobserved=%d decision=%+v want fit/limit=%t/5", unobserved, decision, wantFit)
		}
	}
}

func TestTPSGateUsesCurrentRateForOneBoundedMatureRecoveryStep(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 4, QualifiedTokens: 100,
		QualifiedActiveSeconds: 1, QualifiedSequenceSeconds: 4,
		AggregateTPS: 100, MeanActiveTPS: 25,
	}
	for unobserved := int64(0); unobserved <= 1; unobserved++ {
		state := ProjectedState{
			RawRunning:               5,
			UnobservedSequences:      unobserved,
			GenerationDelta:          70,
			ObservationInterval:      500 * time.Millisecond,
			ObservationIntervalValid: true,
			TPS:                      snapshot,
		}
		decision := (tpsGate{}).evaluate(state)
		wantFit := unobserved == 0
		if decision.fits != wantFit || decision.sequenceLimit != 6 ||
			decision.currentSequences != 5+unobserved ||
			decision.postAdmitSequences != 6+unobserved {
			t.Fatalf("unobserved=%d decision=%+v want fit/limit=%t/6", unobserved, decision, wantFit)
		}
	}
}

func TestTPSGateDoesNotUseCurrentRateWithoutCurrentSafeSignal(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Reference: 20,
		QualifiedSamples: 1, QualifiedTokens: 75,
		QualifiedActiveSeconds: 0.5, QualifiedSequenceSeconds: 2,
		AggregateTPS: 150, MeanActiveTPS: 37.5,
	}
	for _, test := range []struct {
		name  string
		state ProjectedState
		limit int64
	}{
		{name: "no current generation", state: ProjectedState{}, limit: 2},
		{name: "waiting", state: ProjectedState{GenerationDelta: 30, RawWaiting: 1, ObservationInterval: 500 * time.Millisecond, ObservationIntervalValid: true}, limit: 3},
		{name: "preemption", state: ProjectedState{GenerationDelta: 30, PreemptionDelta: 1, ObservationInterval: 500 * time.Millisecond, ObservationIntervalValid: true}, limit: 2},
		{name: "invalid interval", state: ProjectedState{GenerationDelta: 30, ObservationInterval: 500 * time.Millisecond}, limit: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := test.state
			state.RawRunning = 2
			state.TPS = snapshot
			decision := (tpsGate{}).evaluate(state)
			if decision.fits || decision.reason != ReasonTPSReference || decision.sequenceLimit != test.limit {
				t.Fatalf("unsafe exploration decision=%+v state=%+v", decision, state)
			}
		})
	}
}

func TestTPSGateDoesNotRepeatUnsafeOneToTwoExplorationAfterWarming(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20, AggregateTPS: 30, MeanActiveTPS: 30,
	}
	decision := (tpsGate{}).evaluate(ProjectedState{RawRunning: 1, TPS: snapshot})
	if decision.fits || decision.reason != ReasonTPSReference || decision.sequenceLimit != 1 ||
		decision.postAdmitSequences != 2 {
		t.Fatalf("unsafe one-to-two exploration=%+v", decision)
	}
}

func TestTPSGateAddsOnlyReservationsNotYetVisibleUpstream(t *testing.T) {
	gate := tpsGate{}
	snapshot := TPSSnapshot{Enabled: true, Ready: true, Reference: 20, AggregateTPS: 100, MeanActiveTPS: 20}
	for _, test := range []struct {
		name    string
		state   ProjectedState
		current int64
	}{
		{name: "already visible upstream", state: ProjectedState{RawRunning: 5, SequenceLiabilities: 5}, current: 5},
		{name: "reservation not visible upstream", state: ProjectedState{RawRunning: 3, UnobservedSequences: 4, SequenceLiabilities: 4}, current: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := test.state
			state.TPS = snapshot
			decision := gate.evaluate(state)
			if decision.currentSequences != test.current || decision.postAdmitSequences != test.current+1 {
				t.Fatalf("decision=%+v", decision)
			}
		})
	}
}

func TestTPSGateAlwaysAllowsOneIdleProbeAfterWindowIsReady(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20, AggregateTPS: 0, MeanActiveTPS: 0,
	}})
	if !decision.fits || decision.sequenceLimit != 1 || decision.currentSequences != 0 || decision.postAdmitSequences != 1 {
		t.Fatalf("idle probe decision=%+v", decision)
	}
}

func TestTPSGateCountsBackendWaitingTowardFutureDemand(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{
		RawRunning: 4,
		RawWaiting: 1,
		TPS: TPSSnapshot{
			Enabled: true, Ready: true, Reference: 20, AggregateTPS: 100, MeanActiveTPS: 25,
		},
	})
	if decision.fits || decision.reason != ReasonTPSReference ||
		decision.sequenceLimit != 5 || decision.currentSequences != 5 ||
		decision.postAdmitSequences != 6 {
		t.Fatalf("waiting demand was not protected: %+v", decision)
	}
}

func TestV01215TPSGateFreezesHistoricalHeadroomDuringCurrentPressure(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 20, QualifiedSequenceSeconds: 100,
		AggregateTPS: 240, MeanActiveTPS: 24,
	}
	for _, test := range []struct {
		name  string
		state ProjectedState
	}{
		{name: "waiting", state: ProjectedState{RawRunning: 5, RawWaiting: 1}},
		{name: "preemption", state: ProjectedState{RawRunning: 5, PreemptionDelta: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := test.state
			state.TPS = snapshot
			decision := (tpsGate{}).evaluate(state)
			if decision.fits || decision.reason != ReasonTPSReference ||
				decision.sequenceLimit != decision.currentSequences {
				t.Fatalf("current pressure spent historical headroom: %+v", decision)
			}
		})
	}
}

func TestV01215TPSGateKeepsMatureLongWindowCapacityWithinOnePoll(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 20, QualifiedSequenceSeconds: 100,
		AggregateTPS: 240, MeanActiveTPS: 24,
	}
	for unobserved := int64(0); unobserved <= 1; unobserved++ {
		state := ProjectedState{
			RawRunning: 5, UnobservedSequences: unobserved,
			GenerationDelta: 70, ObservationInterval: 500 * time.Millisecond,
			ObservationIntervalValid: true, TPS: snapshot,
		}
		decision := (tpsGate{}).evaluate(state)
		if !decision.fits || decision.sequenceLimit != 12 ||
			decision.currentSequences != 5+unobserved {
			t.Fatalf("unobserved=%d mature long-window capacity was discarded: %+v", unobserved, decision)
		}
	}
}

func TestV01215TPSGateLowFlowHealthOpensExactlyOneProbeWave(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 50,
		QualifiedSamples: 20, QualifiedSequenceSeconds: 20,
		AggregateTPS: 60, MeanActiveTPS: 60,
	}
	for unobserved := int64(0); unobserved <= 1; unobserved++ {
		decision := (tpsGate{}).evaluate(ProjectedState{
			RawRunning: 1, UnobservedSequences: unobserved,
			GenerationDelta: 30, ObservationInterval: 500 * time.Millisecond,
			ObservationIntervalValid: true, TPS: snapshot,
		})
		wantFit := unobserved == 0
		if decision.fits != wantFit || decision.sequenceLimit != 2 ||
			decision.currentSequences != 1+unobserved ||
			decision.postAdmitSequences != 2+unobserved {
			t.Fatalf("unobserved=%d low-flow probe=%+v want fit=%t", unobserved, decision, wantFit)
		}
	}
}

func TestV01216TPSGateAllowsOneBoundedNearReferenceProbeWithoutLifetimeBudget(t *testing.T) {
	for _, test := range []struct {
		name       string
		reference  float64
		aggregate  float64
		generation uint64
	}{
		{name: "reference 25", reference: 25, aggregate: 120, generation: 60},
		{name: "reference 50", reference: 50, aggregate: 240, generation: 120},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := (tpsGate{}).evaluate(ProjectedState{
				RawRunning:               4,
				GenerationDelta:          test.generation,
				ObservationInterval:      500 * time.Millisecond,
				ObservationIntervalValid: true,
				TPS: TPSSnapshot{
					Enabled: true, Ready: true, Reference: test.reference,
					QualifiedSamples: 20, QualifiedSequenceSeconds: 100,
					AggregateTPS: test.aggregate, MeanActiveTPS: 1.2 * test.reference,
				},
			})
			if !decision.fits || decision.reason != ReasonOpen || decision.sequenceLimit != 5 ||
				decision.currentSequences != 4 || decision.postAdmitSequences != 5 || decision.qosBudgeted {
				t.Fatalf("bounded near-reference probe was over-protected: %+v", decision)
			}
		})
	}
}

func TestV01215TPSGateDoesNotOpenKnownBelowFloorMarginalWave(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 20, QualifiedSequenceSeconds: 100,
		AggregateTPS: 150, MeanActiveTPS: 150.0 / 7,
	}
	decision := (tpsGate{}).evaluate(ProjectedState{
		RawRunning:               7,
		GenerationDelta:          75,
		ObservationInterval:      500 * time.Millisecond,
		ObservationIntervalValid: true,
		TPS:                      snapshot,
	})
	if decision.fits || decision.reason != ReasonTPSReference ||
		decision.sequenceLimit != 7 || decision.currentSequences != 7 ||
		decision.postAdmitSequences != 8 {
		t.Fatalf("known below-floor marginal wave was admitted: %+v", decision)
	}
}

func TestV01215TPSGateSpendsLongWindowSurplusOnOnlyOneMarginalWave(t *testing.T) {
	single := tpsAdmissionDemand{additionalSequences: 1}
	positive := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 20, QualifiedTokens: 2400, QualifiedActiveSeconds: 16,
		QualifiedSequenceSamples: 20, QualifiedSequenceTokens: 2400,
		QualifiedSequenceSeconds: 100, AggregateTPS: 150, MeanActiveTPS: 24,
	}
	negative := positive
	negative.QualifiedSequenceSeconds = 125
	negative.MeanActiveTPS = 19.2

	base := ProjectedState{
		RawRunning: 7, GenerationDelta: 75,
		ObservationInterval: 500 * time.Millisecond, ObservationIntervalValid: true,
	}
	for _, test := range []struct {
		name     string
		state    ProjectedState
		demand   tpsAdmissionDemand
		fits     bool
		limit    int64
		budgeted bool
	}{
		{name: "positive observed surplus", state: base, demand: single, fits: true, limit: 8, budgeted: true},
		{name: "same snapshot spent", state: withUnobservedSequences(base, 1), demand: single, fits: false, limit: 7},
		{name: "live budget lease", state: withQoSBudgetLease(base), demand: single, fits: false, limit: 7},
		{name: "multi-sequence wave", state: base, demand: tpsAdmissionDemand{additionalSequences: 2}, fits: false, limit: 7},
		{name: "negative surplus", state: base, demand: single, fits: false, limit: 7},
		{name: "current rate too low", state: withGenerationDelta(base, 60), demand: single, fits: false, limit: 7},
		{name: "waiting", state: withWaiting(base, 1), demand: single, fits: false, limit: 8},
		{name: "preemption", state: withPreemptionDelta(base, 1), demand: single, fits: false, limit: 7},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := test.state
			state.TPS = positive
			if test.name == "negative surplus" {
				state.TPS = negative
			}
			decision := (tpsGate{}).evaluateAdditional(state, test.demand)
			if decision.fits != test.fits || decision.sequenceLimit != test.limit ||
				decision.currentSequences+test.demand.additionalSequences != decision.postAdmitSequences ||
				decision.qosBudgeted != test.budgeted {
				t.Fatalf("QoS-budget decision=%+v state=%+v", decision, state)
			}
			if !test.fits && decision.reason != ReasonTPSReference {
				t.Fatalf("QoS-budget protection reason=%s", decision.reason)
			}
		})
	}
}

func withQoSBudgetLease(state ProjectedState) ProjectedState {
	state.QoSBudgetLeases = 1
	state.LiveReservations = 1
	return state
}

func withUnobservedSequences(state ProjectedState, sequences int64) ProjectedState {
	state.UnobservedSequences = sequences
	return state
}

func withGenerationDelta(state ProjectedState, tokens uint64) ProjectedState {
	state.GenerationDelta = tokens
	return state
}

func withWaiting(state ProjectedState, waiting int64) ProjectedState {
	state.RawWaiting = waiting
	return state
}

func withPreemptionDelta(state ProjectedState, preemptions uint64) ProjectedState {
	state.PreemptionDelta = preemptions
	return state
}

func TestTPSGateUsesRecentReadyWindowForAtomicIdleRefill(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 20, QualifiedSequenceSeconds: 100,
		AggregateTPS: 240, MeanActiveTPS: 24,
	}
	first := (tpsGate{}).evaluate(ProjectedState{TPS: snapshot})
	atLimit := (tpsGate{}).evaluate(ProjectedState{
		UnobservedSequences: 11, SequenceLiabilities: 11, TPS: snapshot,
	})
	aboveLimit := (tpsGate{}).evaluate(ProjectedState{
		UnobservedSequences: 12, SequenceLiabilities: 12, TPS: snapshot,
	})
	if !first.fits || first.sequenceLimit != 12 || first.subreason != TPSDecisionSubreasonIdle ||
		!atLimit.fits || atLimit.sequenceLimit != 12 ||
		aboveLimit.fits || aboveLimit.reason != ReasonTPSReference || aboveLimit.sequenceLimit != 12 {
		t.Fatalf("ready idle refill first=%+v at_limit=%+v above_limit=%+v", first, atLimit, aboveLimit)
	}
}

func TestV01218TPSGateAttributesProtectionAndBudgetSubreasonsWithoutChangingLimits(t *testing.T) {
	disabled := (tpsGate{}).evaluate(ProjectedState{})
	if disabled.result != TPSDecisionResultDisabled || disabled.subreason != TPSDecisionSubreasonDisabled ||
		!disabled.fits {
		t.Fatalf("disabled attribution=%+v", disabled)
	}
	warming := (tpsGate{}).evaluate(ProjectedState{
		UnobservedSequences: 2,
		SequenceLiabilities: 2,
		TPS:                 TPSSnapshot{Enabled: true, Reference: 20},
	})
	if warming.result != TPSDecisionResultProtect || warming.subreason != TPSDecisionSubreasonWarming ||
		warming.fits || warming.sequenceLimit != 2 {
		t.Fatalf("warming attribution=%+v", warming)
	}
	waiting := (tpsGate{}).evaluate(ProjectedState{
		RawRunning: 1,
		RawWaiting: 1,
		TPS:        TPSSnapshot{Enabled: true, Ready: true, Reference: 20, AggregateTPS: 100},
	})
	if waiting.result != TPSDecisionResultProtect || waiting.subreason != TPSDecisionSubreasonWaiting || waiting.fits {
		t.Fatalf("waiting attribution=%+v", waiting)
	}
	preemption := (tpsGate{}).evaluate(ProjectedState{
		RawRunning:      1,
		PreemptionDelta: 1,
		TPS:             TPSSnapshot{Enabled: true, Ready: true, Reference: 20, AggregateTPS: 100},
	})
	if preemption.result != TPSDecisionResultProtect ||
		preemption.subreason != TPSDecisionSubreasonPreemption || preemption.fits {
		t.Fatalf("preemption attribution=%+v", preemption)
	}

	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 20, QualifiedTokens: 2_400,
		QualifiedSequenceSamples: 20, QualifiedSequenceTokens: 2_400,
		QualifiedSequenceSeconds: 100, AggregateTPS: 150, MeanActiveTPS: 24,
	}
	state := ProjectedState{
		RawRunning: 7, GenerationDelta: 75,
		ObservationInterval: 500 * time.Millisecond, ObservationIntervalValid: true,
		TPS: snapshot,
	}
	granted := (tpsGate{}).evaluateAdditional(state, tpsAdmissionDemand{additionalSequences: 1})
	if granted.result != TPSDecisionResultAdmit ||
		granted.subreason != TPSDecisionSubreasonQoSBudgetGranted ||
		!granted.fits || !granted.qosBudgeted || granted.sequenceLimit != 8 {
		t.Fatalf("budget grant attribution=%+v", granted)
	}
	degraded := state
	degraded.GenerationDelta = 60
	currentRate := (tpsGate{}).evaluateAdditional(degraded, tpsAdmissionDemand{additionalSequences: 1})
	if currentRate.result != TPSDecisionResultProtect ||
		currentRate.subreason != TPSDecisionSubreasonQoSBudgetCurrentRate ||
		currentRate.fits || currentRate.sequenceLimit != 7 {
		t.Fatalf("budget current-rate attribution=%+v", currentRate)
	}
}
