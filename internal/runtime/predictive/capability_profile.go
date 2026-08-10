package predictive

import (
	"fmt"
	"math"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

const (
	CapabilityProfileSchema                    = "request-aware-capability-v3"
	DefaultCapabilityDecodeHorizonTokens int64 = 256
)

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
	MaxModelLenTokens            int64
	MaximumAdmissibleInputTokens int64
	PrefillRegularTokens         int64
	PrefillExclusiveTokens       int64
	PrefillQuiescentTokens       int64
	PrefillContendedBudgetTokens int64
	PrefillAggregateBudgetTokens int64
	Source                       CapabilityProfileSource
}

func NewBackendCapabilityProfile(input CapabilityProfileInput) (BackendCapabilityProfile, error) {
	if input.ModelIdentitySHA256 == "" || input.KVCapacityTokens <= 0 || input.KVCapacityTokens > 1<<53 ||
		input.KVBlockSize <= 0 || input.KVBlockSize >= input.KVCapacityTokens ||
		!requestAwareFinite(input.KVHardRatio) || input.KVHardRatio <= 0 || input.KVHardRatio >= 1 ||
		input.MaxModelLen <= 0 || input.MaxModelLen > 1<<53 {
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability geometry is invalid")
	}
	hard, ok := capabilityRatioTokens(input.KVCapacityTokens, input.KVBlockSize, input.KVHardRatio)
	if !ok || hard >= input.KVCapacityTokens {
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability KV limits are invalid")
	}
	maximumInput, ok := capabilityMaximumAdmissibleInput(
		input.MaxModelLen,
		hard,
		input.KVBlockSize,
		DefaultCapabilityDecodeHorizonTokens,
	)
	if !ok {
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability admissible input span is empty")
	}

	profile := BackendCapabilityProfile{
		SchemaVersion:                CapabilityProfileSchema,
		ModelIdentitySHA256:          input.ModelIdentitySHA256,
		KVCapacityTokens:             input.KVCapacityTokens,
		KVBlockSize:                  input.KVBlockSize,
		KVHardLimitTokens:            hard,
		MaxModelLenTokens:            input.MaxModelLen,
		MaximumAdmissibleInputTokens: maximumInput,
		Source:                       input.Source,
	}
	var bounds PrefillTokenBounds
	var contendedBudget int64
	switch input.Source {
	case CapabilityProfileAutomatic:
		var err error
		bounds, contendedBudget, err = automaticPrefillBounds(maximumInput, input.KVBlockSize)
		if err != nil {
			return BackendCapabilityProfile{}, err
		}
	case CapabilityProfileExplicit:
		bounds = alignPrefillBounds(input.Prefill, input.KVBlockSize)
		contendedBudget = capabilityBlockRoundDown(
			minCapabilityTokens(bounds.Regular, maximumInput),
			input.KVBlockSize,
		)
	default:
		return BackendCapabilityProfile{}, fmt.Errorf("backend capability profile source is invalid")
	}
	if err := validatePrefillProfile(bounds, contendedBudget, maximumInput, input.KVBlockSize); err != nil {
		return BackendCapabilityProfile{}, err
	}
	profile.setPrefill(bounds, contendedBudget)
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
		p.KVHardLimitTokens%p.KVBlockSize != 0 ||
		p.MaxModelLenTokens <= 0 || p.MaxModelLenTokens > 1<<53 {
		return fmt.Errorf("backend capability profile is invalid")
	}
	if p.Source != CapabilityProfileAutomatic && p.Source != CapabilityProfileExplicit {
		return fmt.Errorf("backend capability profile source is invalid")
	}
	maximumInput, ok := capabilityMaximumAdmissibleInput(
		p.MaxModelLenTokens,
		p.KVHardLimitTokens,
		p.KVBlockSize,
		DefaultCapabilityDecodeHorizonTokens,
	)
	if !ok || maximumInput != p.MaximumAdmissibleInputTokens {
		return fmt.Errorf("backend capability maximum input is invalid")
	}
	bounds := PrefillTokenBounds{
		Regular:   p.PrefillRegularTokens,
		Exclusive: p.PrefillExclusiveTokens,
		Quiescent: p.PrefillQuiescentTokens,
		Aggregate: p.PrefillAggregateBudgetTokens,
	}
	if err := validatePrefillProfile(
		bounds,
		p.PrefillContendedBudgetTokens,
		p.MaximumAdmissibleInputTokens,
		p.KVBlockSize,
	); err != nil {
		return err
	}
	if p.Source == CapabilityProfileAutomatic {
		expectedBounds, expectedContended, err := automaticPrefillBounds(
			p.MaximumAdmissibleInputTokens,
			p.KVBlockSize,
		)
		if err != nil || bounds != expectedBounds || p.PrefillContendedBudgetTokens != expectedContended {
			return fmt.Errorf("automatic backend capability Prefill profile is invalid")
		}
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

func automaticPrefillBounds(maximumInput, blockSize int64) (PrefillTokenBounds, int64, error) {
	if maximumInput <= 0 || blockSize <= 0 {
		return PrefillTokenBounds{}, 0, fmt.Errorf("automatic backend capability span is invalid")
	}
	bounds := PrefillTokenBounds{
		Regular:   capabilityBlockRoundDown(domain.DefaultPrefillRegularTokens, blockSize),
		Exclusive: capabilityBlockRoundDown(domain.DefaultPrefillExclusiveTokens, blockSize),
		Quiescent: capabilityBlockRoundDown(domain.DefaultPrefillQuiescentTokens, blockSize),
		Aggregate: capabilityBlockRoundDown(
			minCapabilityTokens(domain.DefaultPrefillAggregateBudgetTokens, maximumInput),
			blockSize,
		),
	}
	contended := capabilityBlockRoundDown(
		minCapabilityTokens(domain.DefaultPrefillRegularTokens, maximumInput),
		blockSize,
	)
	return bounds, contended, nil
}

func alignPrefillBounds(bounds PrefillTokenBounds, blockSize int64) PrefillTokenBounds {
	bounds.Regular = capabilityBlockRoundDown(bounds.Regular, blockSize)
	bounds.Exclusive = capabilityBlockRoundDown(bounds.Exclusive, blockSize)
	bounds.Quiescent = capabilityBlockRoundDown(bounds.Quiescent, blockSize)
	bounds.Aggregate = capabilityBlockRoundDown(bounds.Aggregate, blockSize)
	return bounds
}

func validatePrefillProfile(
	bounds PrefillTokenBounds,
	contendedBudget int64,
	maximumInput int64,
	blockSize int64,
) error {
	if blockSize <= 0 || bounds.Regular <= 0 || bounds.Exclusive <= bounds.Regular ||
		bounds.Quiescent <= bounds.Exclusive || bounds.Quiescent > 1<<53 ||
		bounds.Regular%blockSize != 0 ||
		bounds.Exclusive%blockSize != 0 || bounds.Quiescent%blockSize != 0 || bounds.Aggregate%blockSize != 0 {
		return fmt.Errorf("backend capability Prefill bounds are invalid")
	}
	if maximumInput <= 0 || contendedBudget <= 0 || contendedBudget > bounds.Regular ||
		contendedBudget > maximumInput || contendedBudget%blockSize != 0 ||
		bounds.Aggregate < contendedBudget || bounds.Aggregate > bounds.Quiescent {
		return fmt.Errorf("backend capability Prefill budgets are invalid")
	}
	return nil
}

func capabilityMaximumAdmissibleInput(
	maxModelLen,
	hardLimit,
	blockSize,
	decodeHorizon int64,
) (int64, bool) {
	if maxModelLen <= 0 || hardLimit <= 0 || blockSize <= 0 || decodeHorizon < 0 {
		return 0, false
	}
	alignedTotal := capabilityBlockRoundDown(minCapabilityTokens(maxModelLen, hardLimit), blockSize)
	if alignedTotal <= decodeHorizon {
		return 0, false
	}
	return alignedTotal - decodeHorizon, true
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

func (p *BackendCapabilityProfile) setPrefill(bounds PrefillTokenBounds, contendedBudget int64) {
	p.PrefillRegularTokens = bounds.Regular
	p.PrefillExclusiveTokens = bounds.Exclusive
	p.PrefillQuiescentTokens = bounds.Quiescent
	p.PrefillContendedBudgetTokens = contendedBudget
	p.PrefillAggregateBudgetTokens = bounds.Aggregate
}
