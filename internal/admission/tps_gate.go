package admission

type gateDecision struct {
	fits   bool
	reason Reason
}

type tpsGateDecision struct {
	gateDecision
	result    TPSDecisionResult
	subreason TPSDecisionSubreason
}

type tpsGate struct {
	backendTierScheduling bool
	// warmPromptTokens, when positive, makes TPS health Prompt-aware for the
	// default and flex tiers while the rolling window is below the reference.
	// A warm Prompt (estimated uncached part at most this many tokens, one
	// Prefill chunk) passes on a recovered latest sample: it costs at most the
	// one-step stall any chunk costs. A cold or unknown Prompt needs the
	// window mean at the reference, because a recovered sample is the moment
	// a Prefill chunk ended and says nothing about absorbing several more,
	// and is admitted only while no other sheddable cold Prompt is pending
	// first byte, so cold Prompts Prefill in turn instead of all at once.
	// Zero keeps the plain reference gate for every tier.
	warmPromptTokens int64
}

func (g tpsGate) promptAware() bool {
	return g.warmPromptTokens > 0
}

func (g tpsGate) warm(demand TPSRequestDemand) bool {
	return g.promptAware() && demand.PrefillKnown && demand.PrefillTokens <= g.warmPromptTokens
}

// coldSheddable reports whether a request is a sheddable cold Prompt: a
// default or flex request whose estimated uncached Prompt exceeds the warm
// size or is unknown, with Prompt-aware gating on.
func (g tpsGate) coldSheddable(demand TPSRequestDemand) bool {
	return g.promptAware() && demand.Tier.Sheddable() && !g.warm(demand)
}

func (g tpsGate) evaluate(
	state ProjectedState,
	waitingAllowance int64,
	tier RequestTier,
) tpsGateDecision {
	return g.evaluateDemand(state, waitingAllowance, TPSRequestDemand{Tier: tier})
}

func (g tpsGate) evaluateDemand(
	state ProjectedState,
	waitingAllowance int64,
	demand TPSRequestDemand,
) tpsGateDecision {
	tier := demand.Tier
	decision := tpsGateDecision{
		gateDecision: gateDecision{fits: true, reason: ReasonOpen},
		result:       TPSDecisionResultDisabled,
		subreason:    TPSDecisionSubreasonDisabled,
	}
	if tier.Sheddable() && state.RawWaiting > waitingAllowance {
		decision.fits = false
		decision.reason = ReasonTPSReference
		decision.result = TPSDecisionResultProtect
		decision.subreason = TPSDecisionSubreasonWaiting
		return decision
	}
	snapshot := state.TPS
	if !snapshot.Enabled {
		return decision
	}
	decision.result = TPSDecisionResultAdmit
	decision.subreason = TPSDecisionSubreasonWarming
	if !validTPSSnapshot(snapshot) || snapshot.Reference <= 0 {
		decision.fits = false
		decision.reason = ReasonResourceExhausted
		decision.result = TPSDecisionResultInvalid
		decision.subreason = TPSDecisionSubreasonInvalidState
		return decision
	}
	if tier.Sheddable() && state.PreemptionDelta > 0 {
		decision.fits = false
		decision.reason = ReasonTPSReference
		decision.result = TPSDecisionResultProtect
		decision.subreason = TPSDecisionSubreasonPreemption
		return decision
	}
	if !snapshot.Ready {
		decision.subreason = TPSDecisionSubreasonWarming
		return decision
	}
	if !snapshot.Latest.Qualified {
		decision.subreason = TPSDecisionSubreasonNoCurrentEvidence
		return decision
	}
	if g.coldSheddable(demand) && state.ColdSheddablePending > 0 {
		decision.fits = false
		decision.reason = ReasonTPSReference
		decision.result = TPSDecisionResultProtect
		decision.subreason = TPSDecisionSubreasonColdPromptPending
		return decision
	}
	if snapshot.MeanActiveTPS >= snapshot.Reference {
		decision.subreason = TPSDecisionSubreasonHealthyWindow
		return decision
	}
	if snapshot.Latest.MeanActiveTPS >= snapshot.Reference {
		if g.coldSheddable(demand) {
			decision.fits = false
			decision.reason = ReasonTPSReference
			decision.result = TPSDecisionResultProtect
			decision.subreason = TPSDecisionSubreasonColdPrompt
			return decision
		}
		if tier.Sheddable() && g.warm(demand) {
			decision.subreason = TPSDecisionSubreasonWarmPrompt
			return decision
		}
		decision.subreason = TPSDecisionSubreasonRecoveredCurrent
		return decision
	}
	// A backend that schedules its waiting queue by the tier PIG writes into
	// every request runs priority Prefill ahead of every queued default and
	// flex request, so the Decode mean no longer describes the service a
	// priority request receives and the reference protects only the
	// sheddable tiers. Any other backend protects every tier.
	if !tier.Sheddable() && g.backendTierScheduling {
		decision.subreason = TPSDecisionSubreasonBackendScheduled
		return decision
	}
	decision.fits = false
	decision.reason = ReasonTPSReference
	decision.result = TPSDecisionResultProtect
	decision.subreason = TPSDecisionSubreasonBelowReference
	return decision
}
