package predictive

import (
	"fmt"
	"math"
)

type DecodeEnvelopeConfig struct {
	InterferenceBudgetTokens int64
}

type DecodeEnvelopeInput struct {
	PostAdmitPrefillTokens int64
	ActiveDecodeSequences  int
}

type DecodeEnvelopeResult struct {
	Admit                  bool
	HardProtection         bool
	Reason                 RequestAwareReason
	PostAdmitPrefillTokens int64
	ActiveDecodeSequences  int
	InterferenceCharge     int64
	InterferenceBudget     int64
	RejectedPressure       float64
}

type DecodeEnvelope struct {
	config DecodeEnvelopeConfig
}

func NewDecodeEnvelope(config DecodeEnvelopeConfig) (*DecodeEnvelope, error) {
	if err := validateDecodeEnvelopeConfig(config); err != nil {
		return nil, err
	}
	return &DecodeEnvelope{config: config}, nil
}

func validateDecodeEnvelopeConfig(config DecodeEnvelopeConfig) error {
	if config.InterferenceBudgetTokens <= 0 {
		return fmt.Errorf("decode envelope configuration is invalid")
	}
	return nil
}

func (e *DecodeEnvelope) Evaluate(input DecodeEnvelopeInput) DecodeEnvelopeResult {
	result := DecodeEnvelopeResult{
		HardProtection:         true,
		Reason:                 RequestAwareReasonInvalid,
		PostAdmitPrefillTokens: input.PostAdmitPrefillTokens,
		ActiveDecodeSequences:  input.ActiveDecodeSequences,
	}
	if e == nil {
		return result
	}
	result.InterferenceBudget = e.config.InterferenceBudgetTokens
	if input.PostAdmitPrefillTokens <= 0 || input.ActiveDecodeSequences < 0 {
		return result
	}
	if input.ActiveDecodeSequences == 0 {
		result.Admit = true
		result.HardProtection = false
		result.Reason = RequestAwareReasonOpen
		return result
	}

	decodeSequences := int64(input.ActiveDecodeSequences)
	if input.PostAdmitPrefillTokens > math.MaxInt64/decodeSequences {
		return result
	}
	result.InterferenceCharge = input.PostAdmitPrefillTokens * decodeSequences
	result.HardProtection = false
	if result.InterferenceCharge <= result.InterferenceBudget {
		result.Admit = true
		result.Reason = RequestAwareReasonOpen
		return result
	}
	result.Reason = RequestAwareReasonDecodeInterference
	result.RejectedPressure = float64(result.InterferenceCharge) / float64(result.InterferenceBudget)
	return result
}

func (e *DecodeEnvelope) MatchesCapability(profile BackendCapabilityProfile) bool {
	return e != nil && e.config.InterferenceBudgetTokens == profile.PrefillRegularTokens
}
