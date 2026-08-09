package predictive

import (
	"fmt"
	"math"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type RequestAwareAction string

const (
	RequestAwareAdmit       RequestAwareAction = "admit"
	RequestAwareSizeProtect RequestAwareAction = "size_protect"
	RequestAwareHardProtect RequestAwareAction = "hard_protect"
)

type RequestAwareReason string

const (
	RequestAwareReasonOpen               RequestAwareReason = "open"
	RequestAwareReasonStale              RequestAwareReason = "stale"
	RequestAwareReasonPreemption         RequestAwareReason = "preemption"
	RequestAwareReasonKV                 RequestAwareReason = "kv"
	RequestAwareReasonPrefillBudget      RequestAwareReason = "prefill_budget"
	RequestAwareReasonPrefillConcurrency RequestAwareReason = "prefill_concurrency"
	RequestAwareReasonPrefillExclusive   RequestAwareReason = "prefill_exclusive"
	RequestAwareReasonPrefillBusy        RequestAwareReason = "prefill_busy"
	RequestAwareReasonDecodeInterference RequestAwareReason = "decode_interference"
	RequestAwareReasonDuplicate          RequestAwareReason = "duplicate"
	RequestAwareReasonUnavailable        RequestAwareReason = "unavailable"
	RequestAwareReasonInvalid            RequestAwareReason = "invalid"
)

type RequestAwarePressureSource string

const (
	RequestAwarePressureNone    RequestAwarePressureSource = "none"
	RequestAwarePressurePrefill RequestAwarePressureSource = "prefill"
	RequestAwarePressureDecode  RequestAwarePressureSource = "decode"
)

type RequestAwarePrefillClass string

const (
	RequestAwarePrefillRegular   RequestAwarePrefillClass = "regular"
	RequestAwarePrefillWeighted  RequestAwarePrefillClass = "weighted"
	RequestAwarePrefillExclusive RequestAwarePrefillClass = "exclusive"
	RequestAwarePrefillQuiescent RequestAwarePrefillClass = "quiescent"

	DefaultRequestAwarePrefillRegularTokens         = domain.DefaultPrefillRegularTokens
	DefaultRequestAwarePrefillExclusiveTokens       = domain.DefaultPrefillExclusiveTokens
	DefaultRequestAwarePrefillQuiescentTokens       = domain.DefaultPrefillQuiescentTokens
	DefaultRequestAwarePrefillAggregateBudgetTokens = domain.DefaultPrefillAggregateBudgetTokens
)

type RequestAwareConfig struct {
	HardKVLimitTokens            int64
	BlockSize                    int64
	PrefillRegularTokens         int64
	PrefillExclusiveTokens       int64
	PrefillQuiescentTokens       int64
	PrefillAggregateBudgetTokens int64
}

func (c RequestAwareConfig) Validate() error {
	if err := validateResourceGateConfig(c.resourceGateConfig()); err != nil {
		return fmt.Errorf("request-aware policy configuration is invalid: %w", err)
	}
	if err := validateInterferenceGateConfig(c.interferenceGateConfig()); err != nil {
		return fmt.Errorf("request-aware policy configuration is invalid: %w", err)
	}
	if err := validateDecodeEnvelopeConfig(c.decodeEnvelopeConfig()); err != nil {
		return fmt.Errorf("request-aware policy configuration is invalid: %w", err)
	}
	return nil
}

func (c RequestAwareConfig) resourceGateConfig() ResourceGateConfig {
	return ResourceGateConfig{
		HardKVLimitTokens: c.HardKVLimitTokens,
		BlockSize:         c.BlockSize,
	}
}

func (c RequestAwareConfig) interferenceGateConfig() InterferenceGateConfig {
	return InterferenceGateConfig{
		PrefillRegularTokens:         c.PrefillRegularTokens,
		PrefillExclusiveTokens:       c.PrefillExclusiveTokens,
		PrefillQuiescentTokens:       c.PrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: c.PrefillAggregateBudgetTokens,
	}
}

func (c RequestAwareConfig) decodeEnvelopeConfig() DecodeEnvelopeConfig {
	return DecodeEnvelopeConfig{InterferenceBudgetTokens: c.PrefillRegularTokens}
}

type RequestAwareInput struct {
	MetricsFresh                     bool
	IdentityValid                    bool
	CapacityTokens                   int64
	UsedTokens                       int64
	ReservedTokens                   int64
	RequestReservedTokens            int64
	SelectionInputTokens             int64
	Running                          int
	Waiting                          int
	EffectiveSequences               int
	AggregateTPSProxy                float64
	MeanActiveTPSProxy               float64
	TPSValid                         bool
	PreemptionObserved               bool
	EstimatedPrefillTokens           int64
	PendingPrefillSequences          int
	PendingPrefillTokens             int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
	PendingUnknownPrefillSequences   int
}

type RequestAwareDecision struct {
	Action                           RequestAwareAction
	Reason                           RequestAwareReason
	PressureSource                   RequestAwarePressureSource
	Pressure                         float64
	AllowanceTokens                  int64
	EffectiveKV                      int64
	PostAdmitKV                      int64
	RemainingKV                      int64
	HardKVLimit                      int64
	EffectiveSequences               int
	PrefillClass                     RequestAwarePrefillClass
	EstimatedPrefillTokens           int64
	PendingPrefillSequences          int
	PendingPrefillTokens             int64
	PostAdmitPendingPrefillTokens    int64
	PendingLongPrefillSequences      int
	PendingQuiescentPrefillSequences int
	PendingUnknownPrefillSequences   int
}

type RequestAwarePolicy struct {
	resourceGate     ResourceGate
	interferenceGate InterferenceGate
	decodeEnvelope   DecodeEnvelope
}

func NewRequestAwarePolicy(config RequestAwareConfig) (*RequestAwarePolicy, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &RequestAwarePolicy{
		resourceGate:     ResourceGate{config: config.resourceGateConfig()},
		interferenceGate: InterferenceGate{config: config.interferenceGateConfig()},
		decodeEnvelope:   DecodeEnvelope{config: config.decodeEnvelopeConfig()},
	}, nil
}

func (p *RequestAwarePolicy) MatchesCapability(profile BackendCapabilityProfile) bool {
	if p == nil || profile.Validate() != nil {
		return false
	}
	return p.resourceGate.MatchesCapability(profile) &&
		p.interferenceGate.MatchesCapability(profile) &&
		p.decodeEnvelope.MatchesCapability(profile)
}

func (p *RequestAwarePolicy) Evaluate(input RequestAwareInput) RequestAwareDecision {
	if p == nil {
		return RequestAwareDecision{Action: RequestAwareHardProtect, Reason: RequestAwareReasonInvalid}
	}
	estimatedPrefillTokens := input.EstimatedPrefillTokens
	if estimatedPrefillTokens == 0 {
		estimatedPrefillTokens = input.SelectionInputTokens
	}
	resource := p.resourceGate.Evaluate(ResourceGateInput{
		MetricsFresh:          input.MetricsFresh,
		IdentityValid:         input.IdentityValid,
		CapacityTokens:        input.CapacityTokens,
		UsedTokens:            input.UsedTokens,
		ReservedTokens:        input.ReservedTokens,
		RequestReservedTokens: input.RequestReservedTokens,
	})
	interference := p.interferenceGate.Evaluate(InterferenceGateInput{
		EstimatedPrefillTokens:           estimatedPrefillTokens,
		Running:                          input.Running,
		Waiting:                          input.Waiting,
		EffectiveSequences:               input.EffectiveSequences,
		PreemptionObserved:               input.PreemptionObserved,
		PendingPrefillSequences:          input.PendingPrefillSequences,
		PendingPrefillTokens:             input.PendingPrefillTokens,
		PendingLongPrefillSequences:      input.PendingLongPrefillSequences,
		PendingQuiescentPrefillSequences: input.PendingQuiescentPrefillSequences,
		PendingUnknownPrefillSequences:   input.PendingUnknownPrefillSequences,
	})
	decision := RequestAwareDecision{
		Action:                           RequestAwareHardProtect,
		Reason:                           resource.Reason,
		PressureSource:                   RequestAwarePressureNone,
		EffectiveKV:                      resource.EffectiveKV,
		PostAdmitKV:                      resource.PostAdmitKV,
		RemainingKV:                      resource.RemainingKV,
		HardKVLimit:                      resource.HardKVLimit,
		EffectiveSequences:               input.EffectiveSequences,
		PrefillClass:                     interference.PrefillClass,
		EstimatedPrefillTokens:           interference.EstimatedPrefillTokens,
		PendingPrefillSequences:          interference.PendingPrefillSequences,
		PendingPrefillTokens:             interference.PendingPrefillTokens,
		PostAdmitPendingPrefillTokens:    interference.PostAdmitPendingPrefillTokens,
		PendingLongPrefillSequences:      interference.PendingLongPrefillSequences,
		PendingQuiescentPrefillSequences: interference.PendingQuiescentPrefillSequences,
		PendingUnknownPrefillSequences:   interference.PendingUnknownPrefillSequences,
	}
	if !resource.Fits {
		return decision
	}
	if !interference.Admit {
		decision.Reason = interference.Reason
		if interference.HardProtection {
			return decision
		}
		decision.Action = RequestAwareSizeProtect
		decision.PressureSource = RequestAwarePressurePrefill
		decision.Pressure = 1
		decision.AllowanceTokens = 0
		return decision
	}
	decode := p.decodeEnvelope.Evaluate(DecodeEnvelopeInput{
		PostAdmitPrefillTokens: interference.PostAdmitPendingPrefillTokens,
		ActiveDecodeSequences:  input.EffectiveSequences,
	})
	if !decode.Admit {
		decision.Reason = decode.Reason
		if decode.HardProtection {
			return decision
		}
		decision.Action = RequestAwareSizeProtect
		decision.PressureSource = RequestAwarePressureDecode
		decision.Pressure = decode.RejectedPressure
		decision.AllowanceTokens = 0
		return decision
	}
	decision.Action = RequestAwareAdmit
	decision.Reason = RequestAwareReasonOpen
	decision.AllowanceTokens = resource.RemainingKV
	return decision
}

func (p *RequestAwarePolicy) prefillClass(tokens int64) RequestAwarePrefillClass {
	if p == nil {
		return ""
	}
	return p.interferenceGate.Classify(tokens)
}

func requestAwareFinite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func requestAwareAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > (1<<63-1)-right {
		return 0, false
	}
	return left + right, true
}
