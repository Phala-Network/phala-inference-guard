package predictive

import (
	"fmt"
	"math"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

const CapabilityProfileSchema = "request-aware-capability-v2"

type CapabilityProfileSource string

const (
	CapabilityProfileExplicit  CapabilityProfileSource = "explicit"
	CapabilityProfileAutomatic CapabilityProfileSource = "automatic"
)

type PrefillTokenBounds struct {
	Regular   int64
	Exclusive int64
	Quiescent int64
	Aggregate int64
}

type CapabilityProfileInput struct {
	ModelIdentitySHA256 string
	KVCapacityTokens    int64
	KVBlockSize         int64
	KVHardRatio         float64
	MaxModelLen         int64
	Prefill             PrefillTokenBounds
	Source              CapabilityProfileSource
}

type BackendCapabilityProfile struct {
	SchemaVersion                string
	ModelIdentitySHA256          string
	KVCapacityTokens             int64
	KVBlockSize                  int64
	KVHardLimitTokens            int64
	PrefillRegularTokens         int64
	PrefillExclusiveTokens       int64
	PrefillQuiescentTokens       int64
	PrefillAggregateBudgetTokens int64
	Source                       CapabilityProfileSource
}

func NewBackendCapabilityProfile(input CapabilityProfileInput) (BackendCapabilityProfile, error) {
	if input.ModelIdentitySHA256 == "" || input.KVCapacityTokens <= 0 || input.KVCapacityTokens > 1<<53 ||
		input.KVBlockSize <= 0 || input.KVBlockSize >= input.KVCapacityTokens ||
		!requestAwareFinite(input.KVHardRatio) || input.KVHardRatio <= 0 || input.KVHardRatio >= 1 {
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability geometry is invalid")
	}
	hard, ok := capabilityRatioTokens(input.KVCapacityTokens, input.KVBlockSize, input.KVHardRatio)
	if !ok || hard >= input.KVCapacityTokens {
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability KV limits are invalid")
	}

	profile := BackendCapabilityProfile{
		SchemaVersion:       CapabilityProfileSchema,
		ModelIdentitySHA256: input.ModelIdentitySHA256,
		KVCapacityTokens:    input.KVCapacityTokens,
		KVBlockSize:         input.KVBlockSize,
		KVHardLimitTokens:   hard,
		Source:              input.Source,
	}
	var bounds PrefillTokenBounds
	switch input.Source {
	case CapabilityProfileAutomatic:
		var err error
		bounds, err = automaticPrefillBounds(input.MaxModelLen, hard, input.KVBlockSize)
		if err != nil {
			return BackendCapabilityProfile{}, err
		}
	case CapabilityProfileExplicit:
		bounds = alignPrefillBounds(input.Prefill, input.KVBlockSize)
	default:
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability profile source is invalid")
	}
	if err := validatePrefillBounds(bounds, input.KVBlockSize); err != nil {
		return BackendCapabilityProfile{}, err
	}
	if input.Source == CapabilityProfileAutomatic && bounds.Quiescent > hard {
		return BackendCapabilityProfile{}, fmt.Errorf("automatic backend capability Prefill bounds exceed hard KV")
	}
	profile.setPrefill(bounds)
	if err := profile.Validate(); err != nil {
		return BackendCapabilityProfile{}, err
	}
	return profile, nil
}

func (p BackendCapabilityProfile) Validate() error {
	if p.SchemaVersion != CapabilityProfileSchema || p.ModelIdentitySHA256 == "" ||
		p.KVCapacityTokens <= 0 || p.KVCapacityTokens > 1<<53 ||
		p.KVBlockSize <= 0 || p.KVBlockSize >= p.KVCapacityTokens ||
		p.KVHardLimitTokens <= 0 || p.KVHardLimitTokens >= p.KVCapacityTokens ||
		p.KVHardLimitTokens%p.KVBlockSize != 0 {
		return fmt.Errorf("backend capability profile is invalid")
	}
	if p.Source != CapabilityProfileAutomatic && p.Source != CapabilityProfileExplicit {
		return fmt.Errorf("backend capability profile source is invalid")
	}
	if err := validatePrefillBounds(PrefillTokenBounds{
		Regular:   p.PrefillRegularTokens,
		Exclusive: p.PrefillExclusiveTokens,
		Quiescent: p.PrefillQuiescentTokens,
		Aggregate: p.PrefillAggregateBudgetTokens,
	}, p.KVBlockSize); err != nil {
		return err
	}
	if p.Source == CapabilityProfileAutomatic && p.PrefillQuiescentTokens > p.KVHardLimitTokens {
		return fmt.Errorf("automatic backend capability Prefill bounds exceed hard KV")
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

func automaticPrefillBounds(maxModelLen, hardLimit, blockSize int64) (PrefillTokenBounds, error) {
	if maxModelLen <= 0 || maxModelLen > 1<<53 || hardLimit <= 0 || blockSize <= 0 {
		return PrefillTokenBounds{}, fmt.Errorf("automatic backend capability span is invalid")
	}
	effectiveSpan := capabilityBlockRoundDown(minCapabilityTokens(maxModelLen, hardLimit), blockSize)
	if effectiveSpan <= 0 {
		return PrefillTokenBounds{}, fmt.Errorf("automatic backend capability span is empty")
	}
	bounds := PrefillTokenBounds{
		Regular: capabilityBlockRoundDown(minCapabilityTokens(
			domain.DefaultPrefillRegularTokens, effectiveSpan/8,
		), blockSize),
		Exclusive: capabilityBlockRoundDown(minCapabilityTokens(
			domain.DefaultPrefillExclusiveTokens, effectiveSpan/2,
		), blockSize),
		Quiescent: capabilityBlockRoundDown(minCapabilityTokens(
			domain.DefaultPrefillQuiescentTokens, effectiveSpan,
		), blockSize),
	}
	bounds.Aggregate = bounds.Exclusive
	return bounds, nil
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

func minCapabilityTokens(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func (p *BackendCapabilityProfile) setPrefill(bounds PrefillTokenBounds) {
	p.PrefillRegularTokens = bounds.Regular
	p.PrefillExclusiveTokens = bounds.Exclusive
	p.PrefillQuiescentTokens = bounds.Quiescent
	p.PrefillAggregateBudgetTokens = bounds.Aggregate
}
