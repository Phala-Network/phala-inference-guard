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
		{state: ProjectedState{PendingPrefillSequences: 1}, fits: true, current: 1, post: 2, limit: 2},
		{state: ProjectedState{PendingPrefillSequences: 2}, fits: false, current: 2, post: 3, limit: 2},
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

func TestTPSGateUsesRateDerivedBaseAndOneHealthyExploration(t *testing.T) {
	gate := tpsGate{}
	healthy := TPSSnapshot{Enabled: true, Ready: true, Reference: 20, AggregateTPS: 210, MeanActiveTPS: 25}
	fit := gate.evaluate(ProjectedState{RawRunning: 10, TPS: healthy})
	if !fit.fits || fit.sequenceLimit != 11 || fit.currentSequences != 10 || fit.postAdmitSequences != 11 {
		t.Fatalf("healthy fit=%+v", fit)
	}
	protected := gate.evaluate(ProjectedState{RawRunning: 11, TPS: healthy})
	if protected.fits || protected.reason != ReasonTPSReference || protected.sequenceLimit != 11 || protected.postAdmitSequences != 12 {
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

func TestTPSGateUsesMaximumOfRawAndTrackedSequencesWithoutDoubleCounting(t *testing.T) {
	gate := tpsGate{}
	snapshot := TPSSnapshot{Enabled: true, Ready: true, Reference: 20, AggregateTPS: 100, MeanActiveTPS: 20}
	for _, test := range []struct {
		name    string
		state   ProjectedState
		current int64
	}{
		{name: "already visible upstream", state: ProjectedState{RawRunning: 5, PendingPrefillSequences: 3, LocalActiveDecode: 2}, current: 5},
		{name: "reservation not visible upstream", state: ProjectedState{RawRunning: 3, PendingPrefillSequences: 4, LocalActiveDecode: 2}, current: 6},
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

func TestV01215TPSGateUsesCurrentMeanForOneMarginalHealthyWave(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 25,
		QualifiedSamples: 20, QualifiedSequenceSeconds: 100,
		AggregateTPS: 120, MeanActiveTPS: 30,
	}
	decision := (tpsGate{}).evaluate(ProjectedState{
		RawRunning:               4,
		GenerationDelta:          60,
		ObservationInterval:      500 * time.Millisecond,
		ObservationIntervalValid: true,
		TPS:                      snapshot,
	})
	if !decision.fits || decision.sequenceLimit != 5 ||
		decision.currentSequences != 4 || decision.postAdmitSequences != 5 {
		t.Fatalf("marginal healthy current wave was not admitted: %+v", decision)
	}
}

func TestV01215TPSGateAllowsBoundedIdleRefillWithoutCurrentGeneration(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 20, QualifiedSequenceSeconds: 100,
		AggregateTPS: 240, MeanActiveTPS: 24,
	}
	first := (tpsGate{}).evaluate(ProjectedState{TPS: snapshot})
	second := (tpsGate{}).evaluate(ProjectedState{PendingPrefillSequences: 1, TPS: snapshot})
	third := (tpsGate{}).evaluate(ProjectedState{PendingPrefillSequences: 2, TPS: snapshot})
	if !first.fits || first.sequenceLimit != tpsWarmingSequenceLimit ||
		!second.fits || second.sequenceLimit != tpsWarmingSequenceLimit ||
		third.fits || third.reason != ReasonTPSReference ||
		third.sequenceLimit != tpsWarmingSequenceLimit {
		t.Fatalf("bounded idle refill first=%+v second=%+v third=%+v", first, second, third)
	}
}
