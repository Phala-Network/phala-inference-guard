package predictive

import "fmt"

type PrefillQoSGateConfig struct {
	PrefillRegularTokens         int64
	PrefillExclusiveTokens       int64
	PrefillQuiescentTokens       int64
	PrefillContendedBudgetTokens int64
	PrefillAggregateBudgetTokens int64
}

type PrefillQoSGateInput struct {
	EstimatedPrefillTokens           int64
	LocalActiveDecodeSequences       int
	RawRunning                       int
	RawWaiting                       int
	PreemptionObserved               bool
	PendingPrefillSequences          int
	PendingPrefillTokens             int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
	PendingUnknownPrefillSequences   int
}

type PrefillQoSGateResult struct {
	Admit                            bool
	HardProtection                   bool
	Reason                           RequestAwareReason
	Contended                        bool
	PrefillClass                     RequestAwarePrefillClass
	EstimatedPrefillTokens           int64
	PendingPrefillSequences          int
	PendingPrefillTokens             int64
	PostAdmitPendingPrefillTokens    int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
	PendingUnknownPrefillSequences   int
}

type PrefillQoSGate struct {
	config PrefillQoSGateConfig
}

func NewPrefillQoSGate(config PrefillQoSGateConfig) (*PrefillQoSGate, error) {
	if err := validatePrefillQoSGateConfig(config); err != nil {
		return nil, err
	}
	return &PrefillQoSGate{config: config}, nil
}

func validatePrefillQoSGateConfig(config PrefillQoSGateConfig) error {
	if config.PrefillRegularTokens <= 0 ||
		config.PrefillExclusiveTokens <= config.PrefillRegularTokens ||
		config.PrefillQuiescentTokens <= config.PrefillExclusiveTokens ||
		config.PrefillContendedBudgetTokens <= 0 ||
		config.PrefillContendedBudgetTokens > config.PrefillRegularTokens ||
		config.PrefillAggregateBudgetTokens < config.PrefillContendedBudgetTokens ||
		config.PrefillAggregateBudgetTokens > config.PrefillQuiescentTokens {
		return fmt.Errorf("Prefill QoS gate configuration is invalid")
	}
	return nil
}

func (g *PrefillQoSGate) Evaluate(input PrefillQoSGateInput) PrefillQoSGateResult {
	result := PrefillQoSGateResult{
		HardProtection:                   true,
		Reason:                           RequestAwareReasonInvalid,
		EstimatedPrefillTokens:           input.EstimatedPrefillTokens,
		PendingPrefillSequences:          input.PendingPrefillSequences,
		PendingPrefillTokens:             input.PendingPrefillTokens,
		PendingLongPrefillSequences:      input.PendingLongPrefillSequences,
		PendingQuiescentPrefillSequences: input.PendingQuiescentPrefillSequences,
		PendingUnknownPrefillSequences:   input.PendingUnknownPrefillSequences,
	}
	if g == nil {
		return result
	}
	result.PrefillClass = g.Classify(input.EstimatedPrefillTokens)
	// Generation progress is interval telemetry, not proof that Decode work is
	// still active at the end of the observation. In particular, a completion
	// window can advance generation_tokens_total while reporting running=0.
	// Current backend state, local lifecycle state, and a fresh preemption are
	// the only signals allowed to select the contended regime.
	result.Contended = input.LocalActiveDecodeSequences > 0 || input.RawRunning > 0 ||
		input.RawWaiting > 0 || input.PreemptionObserved
	if input.EstimatedPrefillTokens <= 0 || input.LocalActiveDecodeSequences < 0 ||
		input.RawRunning < 0 || input.RawWaiting < 0 || input.PendingPrefillSequences < 0 ||
		input.PendingPrefillTokens < 0 || input.PendingLongPrefillSequences < 0 ||
		input.PendingLongPrefillSequences > input.PendingPrefillSequences ||
		input.PendingQuiescentPrefillSequences < 0 ||
		input.PendingQuiescentPrefillSequences > input.PendingLongPrefillSequences ||
		input.PendingUnknownPrefillSequences < 0 ||
		input.PendingUnknownPrefillSequences > input.PendingPrefillSequences ||
		(input.PendingPrefillSequences == 0 && input.PendingPrefillTokens != 0) ||
		(input.PendingPrefillSequences > 0 && input.PendingPrefillTokens == 0 && input.PendingUnknownPrefillSequences == 0) {
		return result
	}

	postAdmitTokens, valid := requestAwareAdd(input.PendingPrefillTokens, input.EstimatedPrefillTokens)
	if !valid {
		return result
	}
	result.PostAdmitPendingPrefillTokens = postAdmitTokens
	reason, protect := g.protectionReason(input, result.PrefillClass, result.Contended, postAdmitTokens)
	if protect {
		result.HardProtection = false
		result.Reason = reason
		return result
	}
	result.Admit = true
	result.HardProtection = false
	result.Reason = RequestAwareReasonOpen
	return result
}

func (g *PrefillQoSGate) Classify(tokens int64) RequestAwarePrefillClass {
	switch {
	case g == nil || tokens <= 0:
		return ""
	case tokens < g.config.PrefillRegularTokens:
		return RequestAwarePrefillRegular
	case tokens < g.config.PrefillExclusiveTokens:
		return RequestAwarePrefillWeighted
	case tokens < g.config.PrefillQuiescentTokens:
		return RequestAwarePrefillExclusive
	default:
		return RequestAwarePrefillQuiescent
	}
}

func (g *PrefillQoSGate) protectionReason(
	input PrefillQoSGateInput,
	class RequestAwarePrefillClass,
	contended bool,
	postAdmitTokens int64,
) (RequestAwareReason, bool) {
	if input.PendingLongPrefillSequences > 0 {
		if class == RequestAwarePrefillRegular {
			return RequestAwareReasonPrefillBusy, true
		}
		return RequestAwareReasonPrefillExclusive, true
	}
	if contended {
		if class != RequestAwarePrefillRegular {
			return RequestAwareReasonPrefillBusy, true
		}
		return RequestAwareReasonPrefillBudget, postAdmitTokens > g.config.PrefillContendedBudgetTokens
	}
	if input.PendingUnknownPrefillSequences > 0 && class != RequestAwarePrefillRegular {
		return RequestAwareReasonPrefillExclusive, true
	}
	switch class {
	case RequestAwarePrefillRegular, RequestAwarePrefillWeighted:
		return RequestAwareReasonPrefillBudget, postAdmitTokens > g.config.PrefillAggregateBudgetTokens
	case RequestAwarePrefillExclusive:
		return RequestAwareReasonPrefillConcurrency, input.PendingPrefillSequences > 0
	case RequestAwarePrefillQuiescent:
		return RequestAwareReasonPrefillBusy, input.PendingPrefillSequences > 0 ||
			input.LocalActiveDecodeSequences > 0 || input.RawRunning > 0 || input.RawWaiting > 0
	default:
		return RequestAwareReasonInvalid, true
	}
}

func (g *PrefillQoSGate) MatchesCapability(profile BackendCapabilityProfile) bool {
	return g != nil &&
		g.config.PrefillRegularTokens == profile.PrefillRegularTokens &&
		g.config.PrefillExclusiveTokens == profile.PrefillExclusiveTokens &&
		g.config.PrefillQuiescentTokens == profile.PrefillQuiescentTokens &&
		g.config.PrefillContendedBudgetTokens == profile.PrefillContendedBudgetTokens &&
		g.config.PrefillAggregateBudgetTokens == profile.PrefillAggregateBudgetTokens
}
