package predictive

import "fmt"

type InterferenceGateConfig struct {
	PrefillRegularTokens         int64
	PrefillExclusiveTokens       int64
	PrefillQuiescentTokens       int64
	PrefillAggregateBudgetTokens int64
}

type InterferenceGateInput struct {
	EstimatedPrefillTokens           int64
	Running                          int
	Waiting                          int
	EffectiveSequences               int
	PreemptionObserved               bool
	PendingPrefillSequences          int
	PendingPrefillTokens             int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
	PendingUnknownPrefillSequences   int
}

type InterferenceGateResult struct {
	Admit                            bool
	HardProtection                   bool
	Reason                           RequestAwareReason
	PrefillClass                     RequestAwarePrefillClass
	EstimatedPrefillTokens           int64
	PendingPrefillSequences          int
	PendingPrefillTokens             int64
	PostAdmitPendingPrefillTokens    int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
	PendingUnknownPrefillSequences   int
}

type InterferenceGate struct {
	config InterferenceGateConfig
}

func NewInterferenceGate(config InterferenceGateConfig) (*InterferenceGate, error) {
	if err := validateInterferenceGateConfig(config); err != nil {
		return nil, err
	}
	return &InterferenceGate{config: config}, nil
}

func validateInterferenceGateConfig(config InterferenceGateConfig) error {
	if config.PrefillRegularTokens <= 0 ||
		config.PrefillExclusiveTokens <= config.PrefillRegularTokens ||
		config.PrefillQuiescentTokens <= config.PrefillExclusiveTokens ||
		config.PrefillAggregateBudgetTokens < config.PrefillExclusiveTokens ||
		config.PrefillAggregateBudgetTokens > config.PrefillQuiescentTokens {
		return fmt.Errorf("interference gate configuration is invalid")
	}
	return nil
}

func (g *InterferenceGate) Evaluate(input InterferenceGateInput) InterferenceGateResult {
	result := InterferenceGateResult{
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
	if input.EstimatedPrefillTokens <= 0 || input.Running < 0 || input.Waiting < 0 ||
		input.EffectiveSequences < input.Running || input.PendingPrefillSequences < 0 ||
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
	if input.PreemptionObserved && result.PrefillClass != RequestAwarePrefillRegular {
		result.Reason = RequestAwareReasonPreemption
		return result
	}
	if reason, protect := g.protectionReason(input, result.PrefillClass, postAdmitTokens); protect {
		result.HardProtection = false
		result.Reason = reason
		return result
	}
	result.Admit = true
	result.HardProtection = false
	result.Reason = RequestAwareReasonOpen
	return result
}

func (g *InterferenceGate) Classify(tokens int64) RequestAwarePrefillClass {
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

func (g *InterferenceGate) protectionReason(
	input InterferenceGateInput,
	class RequestAwarePrefillClass,
	postAdmitTokens int64,
) (RequestAwareReason, bool) {
	if input.Waiting > 0 && class != RequestAwarePrefillRegular {
		return RequestAwareReasonPrefillBusy, true
	}
	if input.PendingUnknownPrefillSequences > 0 && class != RequestAwarePrefillRegular {
		return RequestAwareReasonPrefillExclusive, true
	}
	if input.PendingQuiescentPrefillSequences > 0 && class != RequestAwarePrefillRegular {
		return RequestAwareReasonPrefillExclusive, true
	}
	switch class {
	case RequestAwarePrefillRegular:
		if input.PendingLongPrefillSequences > 0 {
			return RequestAwareReasonPrefillBusy, true
		}
		return RequestAwareReasonPrefillBudget, postAdmitTokens > g.config.PrefillAggregateBudgetTokens
	case RequestAwarePrefillWeighted:
		return RequestAwareReasonPrefillBudget, postAdmitTokens > g.config.PrefillAggregateBudgetTokens
	case RequestAwarePrefillExclusive:
		return RequestAwareReasonPrefillConcurrency, input.PendingLongPrefillSequences > 0
	case RequestAwarePrefillQuiescent:
		return RequestAwareReasonPrefillBusy, input.PendingPrefillSequences > 0
	default:
		return RequestAwareReasonInvalid, true
	}
}

func (g *InterferenceGate) MatchesCapability(profile BackendCapabilityProfile) bool {
	return g != nil &&
		g.config.PrefillRegularTokens == profile.PrefillRegularTokens &&
		g.config.PrefillExclusiveTokens == profile.PrefillExclusiveTokens &&
		g.config.PrefillQuiescentTokens == profile.PrefillQuiescentTokens &&
		g.config.PrefillAggregateBudgetTokens == profile.PrefillAggregateBudgetTokens
}
