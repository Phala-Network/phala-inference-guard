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

func TestTPSGateWarmingAllowsTwoSequencesButNotAnUnboundedBurst(t *testing.T) {
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

func TestTPSGateDoesNotExploreWhenBasePlusOneWouldExceedTolerance(t *testing.T) {
	snapshot := TPSSnapshot{Enabled: true, Ready: true, Reference: 20, AggregateTPS: 150, MeanActiveTPS: 22}
	decision := (tpsGate{}).evaluate(ProjectedState{RawRunning: 7, TPS: snapshot})
	if decision.fits || decision.reason != ReasonTPSReference || decision.sequenceLimit != 7 ||
		decision.postAdmitSequences != 8 {
		t.Fatalf("unsafe long-lived exploration=%+v", decision)
	}
}

func TestTPSGateSpendsLongWindowHeadroomForOneBoundedObservationWave(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 4, QualifiedTokens: 120,
		QualifiedActiveSeconds: 2, QualifiedSequenceSeconds: 4,
		AggregateTPS: 60, MeanActiveTPS: 30,
	}
	for unobserved := int64(0); unobserved <= 4; unobserved++ {
		state := ProjectedState{
			RawRunning:               2,
			UnobservedSequences:      unobserved,
			GenerationDelta:          30,
			ObservationInterval:      500 * time.Millisecond,
			ObservationIntervalValid: true,
			TPS:                      snapshot,
		}
		decision := (tpsGate{}).evaluate(state)
		wantFit := unobserved < 4
		if decision.fits != wantFit || decision.sequenceLimit != 6 ||
			decision.currentSequences != 2+unobserved ||
			decision.postAdmitSequences != 3+unobserved {
			t.Fatalf("unobserved=%d decision=%+v want fit/limit=%t/6", unobserved, decision, wantFit)
		}
	}
}

func TestTPSGateSpendsQualifiedHeadroomBeforeWindowMatures(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Reference: 20,
		QualifiedSamples: 1, QualifiedTokens: 30,
		QualifiedActiveSeconds: 0.5, QualifiedSequenceSeconds: 1,
		AggregateTPS: 60, MeanActiveTPS: 30,
	}
	state := ProjectedState{
		RawRunning: 2, GenerationDelta: 30,
		ObservationInterval: 500 * time.Millisecond, ObservationIntervalValid: true,
		TPS: snapshot,
	}
	fit := (tpsGate{}).evaluate(state)
	if !fit.fits || fit.sequenceLimit != 3 || fit.postAdmitSequences != 3 {
		t.Fatalf("qualified warming headroom fit=%+v", fit)
	}
	state.UnobservedSequences = 1
	protected := (tpsGate{}).evaluate(state)
	if protected.fits || protected.reason != ReasonTPSReference ||
		protected.sequenceLimit != 3 || protected.postAdmitSequences != 4 {
		t.Fatalf("qualified warming headroom protection=%+v", protected)
	}
}

func TestTPSGateDoesNotSpendHeadroomWithoutCurrentSafeSignal(t *testing.T) {
	snapshot := TPSSnapshot{
		Enabled: true, Ready: true, Reference: 20,
		QualifiedSamples: 4, QualifiedTokens: 120,
		QualifiedActiveSeconds: 2, QualifiedSequenceSeconds: 4,
		AggregateTPS: 60, MeanActiveTPS: 30,
	}
	for _, test := range []struct {
		name  string
		state ProjectedState
	}{
		{name: "no current generation", state: ProjectedState{}},
		{name: "waiting", state: ProjectedState{GenerationDelta: 30, RawWaiting: 1, ObservationInterval: 500 * time.Millisecond, ObservationIntervalValid: true}},
		{name: "preemption", state: ProjectedState{GenerationDelta: 30, PreemptionDelta: 1, ObservationInterval: 500 * time.Millisecond, ObservationIntervalValid: true}},
		{name: "invalid interval", state: ProjectedState{GenerationDelta: 30, ObservationInterval: 500 * time.Millisecond}},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := test.state
			state.RawRunning = 2
			state.UnobservedSequences = 1
			state.TPS = snapshot
			decision := (tpsGate{}).evaluate(state)
			if decision.fits || decision.reason != ReasonTPSReference || decision.sequenceLimit != 3 {
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
