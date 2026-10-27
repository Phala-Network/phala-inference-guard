package admission

import "testing"

func TestTPSHealthGateDisabledIsOpen(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{}, DefaultWindowConcurrency, TierFlex)
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
	}}, DefaultWindowConcurrency, TierFlex)
	if decision.fits || decision.reason != ReasonResourceExhausted ||
		decision.result != TPSDecisionResultInvalid ||
		decision.subreason != TPSDecisionSubreasonInvalidState {
		t.Fatalf("invalid decision=%+v", decision)
	}
}

func TestTPSHealthGateTreatsExcessWaitingAndPreemptionAsPressure(t *testing.T) {
	for _, test := range []struct {
		name      string
		state     ProjectedState
		subreason TPSDecisionSubreason
	}{
		{name: "waiting", state: ProjectedState{
			RawWaiting: 4, PreviousRawWaiting: 1, ObservationIntervalValid: true,
		}, subreason: TPSDecisionSubreasonWaiting},
		{name: "preemption", state: ProjectedState{PreemptionDelta: 1}, subreason: TPSDecisionSubreasonPreemption},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := test.state
			state.TPS = healthyTPSSnapshot(25, 30, 30)
			decision := (tpsGate{}).evaluate(state, DefaultWaitingAllowance, TierFlex)
			if decision.fits || decision.reason != ReasonTPSReference ||
				decision.result != TPSDecisionResultProtect || decision.subreason != test.subreason {
				t.Fatalf("pressure decision=%+v", decision)
			}
		})
	}
}

func TestTPSHealthGateUnsheddableTiersIgnoreWaitingAndPreemptionPressure(t *testing.T) {
	for _, state := range []ProjectedState{
		{RawWaiting: 4, PreviousRawWaiting: 4, ObservationIntervalValid: true},
		{PreemptionDelta: 1},
	} {
		state.TPS = healthyTPSSnapshot(25, 30, 30)
		decision := (tpsGate{}).evaluate(state, DefaultWindowConcurrency, TierPriority)
		if !decision.fits || decision.reason != ReasonOpen {
			t.Fatalf("priority non-TPS pressure blocked request: %+v", decision)
		}
	}
}

func TestTPSHealthGateAllowsWaitingAtAllowance(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{RawWaiting: 3, PreviousRawWaiting: 3}, 3, TierFlex)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.result != TPSDecisionResultDisabled ||
		decision.subreason != TPSDecisionSubreasonDisabled {
		t.Fatalf("allowed waiting decision=%+v", decision)
	}
}

func TestTPSHealthGateProtectsWaitingAboveAllowanceImmediately(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{RawWaiting: 4}, 3, TierFlex)
	if decision.fits || decision.reason != ReasonTPSReference ||
		decision.result != TPSDecisionResultProtect ||
		decision.subreason != TPSDecisionSubreasonWaiting {
		t.Fatalf("excess waiting decision=%+v", decision)
	}
}

func TestTPSHealthGateUsesCurrentWaitingOnly(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{
		RawWaiting:               3,
		PreviousRawWaiting:       4,
		ObservationIntervalValid: false,
	}, 3, TierFlex)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.result != TPSDecisionResultDisabled ||
		decision.subreason != TPSDecisionSubreasonDisabled {
		t.Fatalf("historical waiting decision=%+v", decision)
	}
}

func TestTPSHealthGateWarmingAndNoCurrentEvidenceStayOpen(t *testing.T) {
	warming := (tpsGate{}).evaluate(ProjectedState{TPS: TPSSnapshot{
		Enabled:   true,
		Reference: 25,
	}}, DefaultWindowConcurrency, TierFlex)
	if !warming.fits || warming.subreason != TPSDecisionSubreasonWarming {
		t.Fatalf("warming decision=%+v", warming)
	}

	noCurrent := healthyTPSSnapshot(25, 30, 1)
	noCurrent.Latest = TPSIntervalSnapshot{}
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: noCurrent}, DefaultWindowConcurrency, TierFlex)
	if !decision.fits || decision.subreason != TPSDecisionSubreasonNoCurrentEvidence {
		t.Fatalf("no-current decision=%+v", decision)
	}
}

func TestTPSHealthGateKeepsHealthyRollingWindowOpenAcrossOneLowInterval(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: healthyTPSSnapshot(25, 30, 10)}, DefaultWindowConcurrency, TierFlex)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.subreason != TPSDecisionSubreasonHealthyWindow {
		t.Fatalf("healthy-window decision=%+v", decision)
	}
}

func TestTPSHealthGateReopensImmediatelyFromQualifiedCurrentRecovery(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: healthyTPSSnapshot(25, 20, 30)}, DefaultWindowConcurrency, TierFlex)
	if !decision.fits || decision.reason != ReasonOpen ||
		decision.subreason != TPSDecisionSubreasonRecoveredCurrent {
		t.Fatalf("recovered-current decision=%+v", decision)
	}
}

func TestTPSHealthGateProtectsOnlyWhenRollingAndCurrentAreBelowReference(t *testing.T) {
	decision := (tpsGate{}).evaluate(ProjectedState{TPS: healthyTPSSnapshot(25, 20, 10)}, DefaultWindowConcurrency, TierFlex)
	if decision.fits || decision.reason != ReasonTPSReference ||
		decision.result != TPSDecisionResultProtect ||
		decision.subreason != TPSDecisionSubreasonBelowReference {
		t.Fatalf("below-reference decision=%+v", decision)
	}
}

func TestTPSHealthGateUnsheddableTiersPassBelowReferenceOnlyWhenBackendSchedulesByTier(t *testing.T) {
	gate := tpsGate{backendTierScheduling: true}
	// A backend that does not order its queue by tier (vLLM) protects every
	// tier at the reference, as v0.12.35 did.
	plain := (tpsGate{}).evaluate(ProjectedState{TPS: healthyTPSSnapshot(25, 20, 10)}, DefaultWindowConcurrency, TierPriority)
	if plain.fits || plain.subreason != TPSDecisionSubreasonBelowReference {
		t.Fatalf("priority below reference without backend tier scheduling=%+v", plain)
	}
	state := ProjectedState{TPS: healthyTPSSnapshot(25, 20, 10)}
	for _, tier := range []RequestTier{TierPriority} {
		decision := gate.evaluate(state, DefaultWindowConcurrency, tier)
		if !decision.fits || decision.reason != ReasonOpen ||
			decision.subreason != TPSDecisionSubreasonBackendScheduled {
			t.Fatalf("%s below reference with backend tier scheduling=%+v", tier, decision)
		}
		invalid := gate.evaluate(ProjectedState{TPS: TPSSnapshot{Enabled: true}}, DefaultWindowConcurrency, tier)
		if invalid.fits || invalid.reason != ReasonResourceExhausted {
			t.Fatalf("backend tier scheduling must not admit %s on invalid TPS state: %+v", tier, invalid)
		}
	}
	for _, tier := range []RequestTier{TierDefault, TierFlex} {
		shed := gate.evaluate(state, DefaultWindowConcurrency, tier)
		if shed.fits || shed.subreason != TPSDecisionSubreasonBelowReference {
			t.Fatalf("%s below reference with backend tier scheduling=%+v", tier, shed)
		}
	}
}

func TestTPSHealthGatePromptAwareSheddableTiers(t *testing.T) {
	gate := tpsGate{backendTierScheduling: true, warmPromptTokens: 8192}
	// Window below the reference, latest sample recovered: a cold or unknown
	// default or flex Prompt waits for the window, a warm one passes on the
	// sample, and the priority tier passes as it always did.
	recovered := ProjectedState{TPS: healthyTPSSnapshot(25, 20, 30)}
	for _, tier := range []RequestTier{TierDefault, TierFlex} {
		for name, demand := range map[string]TPSRequestDemand{
			"cold":    NewTPSRequestDemand(1).WithTier(tier).WithPrefillTokens(8193),
			"unknown": NewTPSRequestDemand(1).WithTier(tier),
		} {
			decision := gate.evaluateDemand(recovered, DefaultWindowConcurrency, demand)
			if decision.fits || decision.reason != ReasonTPSReference ||
				decision.result != TPSDecisionResultProtect || decision.subreason != TPSDecisionSubreasonColdPrompt {
				t.Fatalf("%s %s Prompt on a recovered sample=%+v", tier, name, decision)
			}
		}
		warm := gate.evaluateDemand(recovered, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(tier).WithPrefillTokens(8192))
		if !warm.fits || warm.subreason != TPSDecisionSubreasonWarmPrompt {
			t.Fatalf("%s warm Prompt on a recovered sample=%+v", tier, warm)
		}
	}
	priority := gate.evaluateDemand(recovered, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierPriority).WithPrefillTokens(100_000))
	if !priority.fits || priority.subreason != TPSDecisionSubreasonRecoveredCurrent {
		t.Fatalf("priority cold Prompt on a recovered sample=%+v", priority)
	}
	// Window and latest sample both below: nothing sheddable passes, warm or
	// not; the priority tier still passes through backend tier scheduling.
	below := ProjectedState{TPS: healthyTPSSnapshot(25, 20, 10)}
	warmBelow := gate.evaluateDemand(below, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierDefault).WithPrefillTokens(8192))
	if warmBelow.fits || warmBelow.subreason != TPSDecisionSubreasonBelowReference {
		t.Fatalf("warm Prompt below reference=%+v", warmBelow)
	}
	priorityBelow := gate.evaluateDemand(below, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierPriority))
	if !priorityBelow.fits || priorityBelow.subreason != TPSDecisionSubreasonBackendScheduled {
		t.Fatalf("priority below reference=%+v", priorityBelow)
	}
	// A healthy window admits everything as before.
	healthy := ProjectedState{TPS: healthyTPSSnapshot(25, 30, 10)}
	coldHealthy := gate.evaluateDemand(healthy, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierDefault).WithPrefillTokens(100_000))
	if !coldHealthy.fits || coldHealthy.subreason != TPSDecisionSubreasonHealthyWindow {
		t.Fatalf("cold Prompt on a healthy window=%+v", coldHealthy)
	}
	// A warm Prompt still respects the waiting allowance.
	waiting := ProjectedState{RawWaiting: 4, TPS: healthyTPSSnapshot(25, 20, 30)}
	if decision := gate.evaluateDemand(waiting, 3, NewTPSRequestDemand(1).WithTier(TierFlex).WithPrefillTokens(10)); decision.fits {
		t.Fatalf("a warm Prompt must still respect the waiting allowance: %+v", decision)
	}
	// Off (warm size zero): a recovered sample admits a cold sheddable Prompt
	// as it always did, and nothing passes below the reference.
	off := tpsGate{}
	coldOff := off.evaluateDemand(recovered, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierDefault).WithPrefillTokens(8193))
	if !coldOff.fits || coldOff.subreason != TPSDecisionSubreasonRecoveredCurrent {
		t.Fatalf("cold Prompt on a recovered sample with Prompt-aware gating off=%+v", coldOff)
	}
	if offBelow := off.evaluateDemand(below, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierFlex).WithPrefillTokens(0)); offBelow.fits {
		t.Fatalf("Prompt-aware gating off admitted below reference: %+v", offBelow)
	}
}

func TestTPSHealthGateColdPromptSerial(t *testing.T) {
	gate := tpsGate{warmPromptTokens: 8192}
	// Healthy window, one sheddable cold Prompt still pending first byte: a
	// second cold or unknown sheddable Prompt waits; warm and priority pass.
	pending := ProjectedState{TPS: healthyTPSSnapshot(25, 30, 10), ColdSheddablePending: 1, UnobservedSequences: 1, SequenceLiabilities: 1}
	for _, tier := range []RequestTier{TierDefault, TierFlex} {
		for name, demand := range map[string]TPSRequestDemand{
			"cold":    NewTPSRequestDemand(1).WithTier(tier).WithPrefillTokens(8193),
			"unknown": NewTPSRequestDemand(1).WithTier(tier),
		} {
			decision := gate.evaluateDemand(pending, DefaultWindowConcurrency, demand)
			if decision.fits || decision.reason != ReasonTPSReference ||
				decision.result != TPSDecisionResultProtect || decision.subreason != TPSDecisionSubreasonColdPromptPending {
				t.Fatalf("%s %s Prompt with a cold Prompt pending=%+v", tier, name, decision)
			}
		}
		warm := gate.evaluateDemand(pending, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(tier).WithPrefillTokens(8192))
		if !warm.fits || warm.subreason != TPSDecisionSubreasonHealthyWindow {
			t.Fatalf("%s warm Prompt with a cold Prompt pending=%+v", tier, warm)
		}
	}
	priority := gate.evaluateDemand(pending, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierPriority).WithPrefillTokens(100_000))
	if !priority.fits || priority.subreason != TPSDecisionSubreasonHealthyWindow {
		t.Fatalf("priority cold Prompt with a cold Prompt pending=%+v", priority)
	}
	// The rule precedes the recovered-sample pass as well.
	recoveredPending := ProjectedState{TPS: healthyTPSSnapshot(25, 20, 30), ColdSheddablePending: 1, UnobservedSequences: 1, SequenceLiabilities: 1}
	recovered := gate.evaluateDemand(recoveredPending, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierDefault).WithPrefillTokens(8193))
	if recovered.fits || recovered.subreason != TPSDecisionSubreasonColdPromptPending {
		t.Fatalf("cold Prompt on a recovered sample with a cold Prompt pending=%+v", recovered)
	}
	// Nothing pending: the cold Prompt passes on the healthy window.
	clear := ProjectedState{TPS: healthyTPSSnapshot(25, 30, 10)}
	first := gate.evaluateDemand(clear, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierDefault).WithPrefillTokens(8193))
	if !first.fits || first.subreason != TPSDecisionSubreasonHealthyWindow {
		t.Fatalf("cold Prompt with nothing pending=%+v", first)
	}
	// Without TPS evidence the rule does not apply (warming admits as before).
	warming := pending
	warming.TPS.Ready = false
	warmingDecision := gate.evaluateDemand(warming, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierDefault).WithPrefillTokens(8193))
	if !warmingDecision.fits || warmingDecision.subreason != TPSDecisionSubreasonWarming {
		t.Fatalf("cold Prompt while warming with a cold Prompt pending=%+v", warmingDecision)
	}
	// Off (warm size zero): a pending cold Prompt does not matter.
	off := tpsGate{}
	offDecision := off.evaluateDemand(pending, DefaultWindowConcurrency, NewTPSRequestDemand(1).WithTier(TierDefault).WithPrefillTokens(8193))
	if !offDecision.fits || offDecision.subreason != TPSDecisionSubreasonHealthyWindow {
		t.Fatalf("cold Prompt with a cold Prompt pending and Prompt-aware gating off=%+v", offDecision)
	}
}

func TestV01223HealthyWindowDoesNotDeriveConcurrencyCapacity(t *testing.T) {
	state := ProjectedState{
		RawRunning:          200,
		UnobservedSequences: 32,
		SequenceLiabilities: 32,
		TPS:                 healthyTPSSnapshot(25, 71.4, 60),
	}
	decision := (tpsGate{}).evaluate(state, DefaultWindowConcurrency, TierFlex)
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
