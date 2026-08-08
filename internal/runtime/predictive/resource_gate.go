package predictive

import "fmt"

type ResourceGateConfig struct {
	HardKVLimitTokens int64
	BlockSize         int64
}

type ResourceGateInput struct {
	MetricsFresh          bool
	IdentityValid         bool
	CapacityTokens        int64
	UsedTokens            int64
	ReservedTokens        int64
	RequestReservedTokens int64
}

type ResourceGateResult struct {
	Fits        bool
	Reason      RequestAwareReason
	EffectiveKV int64
	PostAdmitKV int64
	RemainingKV int64
	HardKVLimit int64
}

type ResourceGate struct {
	config ResourceGateConfig
}

func NewResourceGate(config ResourceGateConfig) (*ResourceGate, error) {
	if err := validateResourceGateConfig(config); err != nil {
		return nil, err
	}
	return &ResourceGate{config: config}, nil
}

func validateResourceGateConfig(config ResourceGateConfig) error {
	if config.BlockSize <= 0 || config.HardKVLimitTokens <= 0 ||
		config.HardKVLimitTokens%config.BlockSize != 0 {
		return fmt.Errorf("resource gate configuration is invalid")
	}
	return nil
}

func (g *ResourceGate) Evaluate(input ResourceGateInput) ResourceGateResult {
	result := ResourceGateResult{Reason: RequestAwareReasonInvalid}
	if g == nil {
		return result
	}
	result.HardKVLimit = g.config.HardKVLimitTokens
	if input.CapacityTokens <= 0 || input.CapacityTokens > 1<<53 ||
		input.UsedTokens < 0 || input.UsedTokens > input.CapacityTokens ||
		input.ReservedTokens < 0 || input.RequestReservedTokens <= 0 {
		return result
	}

	effectiveKV, valid := requestAwareAdd(input.UsedTokens, input.ReservedTokens)
	if !valid {
		return result
	}
	result.EffectiveKV = effectiveKV
	postAdmitKV, valid := requestAwareAdd(effectiveKV, input.RequestReservedTokens)
	if !valid {
		return result
	}
	result.PostAdmitKV = postAdmitKV
	result.RemainingKV = g.config.HardKVLimitTokens - effectiveKV
	if result.RemainingKV < 0 {
		result.RemainingKV = 0
	}

	if !input.MetricsFresh || !input.IdentityValid {
		result.Reason = RequestAwareReasonStale
		return result
	}
	if g.config.HardKVLimitTokens >= input.CapacityTokens {
		return result
	}
	if effectiveKV > g.config.HardKVLimitTokens || postAdmitKV > g.config.HardKVLimitTokens {
		result.Reason = RequestAwareReasonKV
		return result
	}
	result.Fits = true
	result.Reason = RequestAwareReasonOpen
	return result
}

func (g *ResourceGate) MatchesCapability(profile BackendCapabilityProfile) bool {
	return g != nil &&
		g.config.HardKVLimitTokens == profile.KVHardLimitTokens &&
		g.config.BlockSize == profile.KVBlockSize
}
