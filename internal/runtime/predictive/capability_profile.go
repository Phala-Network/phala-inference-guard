package predictive

import (
	"fmt"
	"math"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

const (
	CapabilityProfileSchema = "request-aware-capability-v1"

	capabilityPrefillSafetyRatio = 0.80
	capabilityRegularSeconds     = 5.0
	capabilityExclusiveSeconds   = 20.0
	capabilityQuiescentSeconds   = 40.0
	capabilityAggregateSeconds   = 20.0
)

type CapabilityProfileSource string

const (
	CapabilityProfileExplicit   CapabilityProfileSource = "explicit"
	CapabilityProfileCalibrated CapabilityProfileSource = "startup_calibration"
	CapabilityProfileFallback   CapabilityProfileSource = "fallback"
)

type PrefillTokenBounds struct {
	Regular   int64
	Exclusive int64
	Quiescent int64
	Aggregate int64
}

type CapabilityProfileInput struct {
	ModelIdentitySHA256             string
	KVCapacityTokens                int64
	KVBlockSize                     int64
	KVTargetRatio                   float64
	KVHardRatio                     float64
	ObservedColdPrefillTokensPerSec float64
	Prefill                         PrefillTokenBounds
	Source                          CapabilityProfileSource
}

type BackendCapabilityProfile struct {
	SchemaVersion                string
	ModelIdentitySHA256          string
	KVCapacityTokens             int64
	KVBlockSize                  int64
	KVSoftLimitTokens            int64
	KVHardLimitTokens            int64
	SafeColdPrefillTokensPerSec  float64
	PrefillRegularTokens         int64
	PrefillExclusiveTokens       int64
	PrefillQuiescentTokens       int64
	PrefillAggregateBudgetTokens int64
	Source                       CapabilityProfileSource
}

func NewBackendCapabilityProfile(input CapabilityProfileInput) (BackendCapabilityProfile, error) {
	if input.ModelIdentitySHA256 == "" || input.KVCapacityTokens <= 0 || input.KVCapacityTokens > 1<<53 ||
		input.KVBlockSize <= 0 || input.KVBlockSize >= input.KVCapacityTokens ||
		!requestAwareFinite(input.KVTargetRatio) || !requestAwareFinite(input.KVHardRatio) ||
		input.KVTargetRatio <= 0 || input.KVHardRatio <= input.KVTargetRatio || input.KVHardRatio >= 1 {
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability geometry is invalid")
	}
	soft, ok := capabilityRatioTokens(input.KVCapacityTokens, input.KVBlockSize, input.KVTargetRatio)
	if !ok {
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability soft KV limit is invalid")
	}
	hard, ok := capabilityRatioTokens(input.KVCapacityTokens, input.KVBlockSize, input.KVHardRatio)
	if !ok || soft >= hard || hard >= input.KVCapacityTokens || hard-soft < input.KVBlockSize {
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability KV limits are invalid")
	}

	profile := BackendCapabilityProfile{
		SchemaVersion:       CapabilityProfileSchema,
		ModelIdentitySHA256: input.ModelIdentitySHA256,
		KVCapacityTokens:    input.KVCapacityTokens,
		KVBlockSize:         input.KVBlockSize,
		KVSoftLimitTokens:   soft,
		KVHardLimitTokens:   hard,
		Source:              input.Source,
	}
	switch input.Source {
	case CapabilityProfileCalibrated:
		bounds, safeRate, err := calibratedPrefillBounds(input.ObservedColdPrefillTokensPerSec, input.KVBlockSize)
		if err != nil {
			return BackendCapabilityProfile{}, err
		}
		profile.SafeColdPrefillTokensPerSec = safeRate
		profile.setPrefill(bounds)
	case CapabilityProfileExplicit, CapabilityProfileFallback:
		bounds := alignPrefillBounds(input.Prefill, input.KVBlockSize)
		if err := validatePrefillBounds(bounds, input.KVBlockSize); err != nil {
			return BackendCapabilityProfile{}, err
		}
		profile.setPrefill(bounds)
	default:
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability profile source is invalid")
	}
	if err := profile.Validate(); err != nil {
		return BackendCapabilityProfile{}, err
	}
	return profile, nil
}

func (p BackendCapabilityProfile) Validate() error {
	if p.SchemaVersion != CapabilityProfileSchema || p.ModelIdentitySHA256 == "" ||
		p.KVCapacityTokens <= 0 || p.KVCapacityTokens > 1<<53 ||
		p.KVBlockSize <= 0 || p.KVBlockSize >= p.KVCapacityTokens ||
		p.KVSoftLimitTokens <= 0 || p.KVHardLimitTokens <= p.KVSoftLimitTokens ||
		p.KVHardLimitTokens >= p.KVCapacityTokens ||
		p.KVSoftLimitTokens%p.KVBlockSize != 0 || p.KVHardLimitTokens%p.KVBlockSize != 0 {
		return fmt.Errorf("backend capability profile is invalid")
	}
	if err := validatePrefillBounds(PrefillTokenBounds{
		Regular:   p.PrefillRegularTokens,
		Exclusive: p.PrefillExclusiveTokens,
		Quiescent: p.PrefillQuiescentTokens,
		Aggregate: p.PrefillAggregateBudgetTokens,
	}, p.KVBlockSize); err != nil {
		return err
	}
	switch p.Source {
	case CapabilityProfileCalibrated:
		if !requestAwareFinite(p.SafeColdPrefillTokensPerSec) || p.SafeColdPrefillTokensPerSec <= 0 {
			return fmt.Errorf("calibrated backend capability rate is invalid")
		}
	case CapabilityProfileExplicit, CapabilityProfileFallback:
		if p.SafeColdPrefillTokensPerSec != 0 {
			return fmt.Errorf("unmeasured backend capability rate must be zero")
		}
	default:
		return fmt.Errorf("backend capability profile source is invalid")
	}
	return nil
}

func capabilityRatioTokens(capacity, blockSize int64, ratio float64) (int64, bool) {
	if capacity <= 0 || blockSize <= 0 || !requestAwareFinite(ratio) || ratio <= 0 || ratio >= 1 {
		return 0, false
	}
	raw := math.Floor(float64(capacity) * ratio)
	if raw <= 0 || raw > float64(math.MaxInt64) {
		return 0, false
	}
	return capabilityBlockRoundDown(int64(raw), blockSize), true
}

func calibratedPrefillBounds(observedRate float64, blockSize int64) (PrefillTokenBounds, float64, error) {
	if !requestAwareFinite(observedRate) || observedRate <= 0 || observedRate > float64(math.MaxInt64)/capabilityQuiescentSeconds {
		return PrefillTokenBounds{}, 0, fmt.Errorf("observed cold-Prefill rate is invalid")
	}
	safeRate := math.Floor(observedRate * capabilityPrefillSafetyRatio)
	if safeRate <= 0 {
		return PrefillTokenBounds{}, 0, fmt.Errorf("safe cold-Prefill rate is invalid")
	}
	// An idle cold-Prefill probe may tighten known-safe ceilings, but it cannot
	// prove that wider Prefill/Decode overlap preserves QoS.
	bounds := PrefillTokenBounds{
		Regular: capabilityBoundedPrefillTokens(
			safeRate, capabilityRegularSeconds, domain.DefaultPrefillRegularTokens, blockSize,
		),
		Exclusive: capabilityBoundedPrefillTokens(
			safeRate, capabilityExclusiveSeconds, domain.DefaultPrefillExclusiveTokens, blockSize,
		),
		Quiescent: capabilityBoundedPrefillTokens(
			safeRate, capabilityQuiescentSeconds, domain.DefaultPrefillQuiescentTokens, blockSize,
		),
		Aggregate: capabilityBoundedPrefillTokens(
			safeRate, capabilityAggregateSeconds, domain.DefaultPrefillAggregateBudgetTokens, blockSize,
		),
	}
	if err := validatePrefillBounds(bounds, blockSize); err != nil {
		return PrefillTokenBounds{}, 0, err
	}
	return bounds, safeRate, nil
}

func capabilityBoundedPrefillTokens(rate, seconds float64, maximum, blockSize int64) int64 {
	derived := int64(math.Floor(rate * seconds))
	if derived > maximum {
		derived = maximum
	}
	return capabilityBlockRoundDown(derived, blockSize)
}

func alignPrefillBounds(bounds PrefillTokenBounds, blockSize int64) PrefillTokenBounds {
	bounds.Regular = capabilityBlockRoundDown(bounds.Regular, blockSize)
	bounds.Exclusive = capabilityBlockRoundDown(bounds.Exclusive, blockSize)
	bounds.Quiescent = capabilityBlockRoundDown(bounds.Quiescent, blockSize)
	bounds.Aggregate = capabilityBlockRoundDown(bounds.Aggregate, blockSize)
	return bounds
}

func validatePrefillBounds(bounds PrefillTokenBounds, blockSize int64) error {
	if blockSize <= 0 || bounds.Regular <= 0 || bounds.Exclusive <= bounds.Regular ||
		bounds.Quiescent <= bounds.Exclusive || bounds.Aggregate < bounds.Exclusive ||
		bounds.Aggregate > bounds.Quiescent || bounds.Regular%blockSize != 0 ||
		bounds.Exclusive%blockSize != 0 || bounds.Quiescent%blockSize != 0 || bounds.Aggregate%blockSize != 0 {
		return fmt.Errorf("backend capability Prefill bounds are invalid")
	}
	return nil
}

func capabilityBlockRoundDown(tokens, blockSize int64) int64 {
	if tokens <= 0 || blockSize <= 0 {
		return 0
	}
	return tokens - tokens%blockSize
}

func (p *BackendCapabilityProfile) setPrefill(bounds PrefillTokenBounds) {
	p.PrefillRegularTokens = bounds.Regular
	p.PrefillExclusiveTokens = bounds.Exclusive
	p.PrefillQuiescentTokens = bounds.Quiescent
	p.PrefillAggregateBudgetTokens = bounds.Aggregate
}
