package admission

import (
	"fmt"
	"math"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type Capability struct {
	Fingerprint                  string
	MaxModelLenTokens            int64
	KVCapacityTokens             int64
	KVBlockSize                  int64
	KVHardLimitTokens            int64
	MaximumInputTokens           int64
	MinimumDecodeHorizonTokens   int64
	PrefillRegularTokens         int64
	PrefillExclusiveTokens       int64
	PrefillQuiescentTokens       int64
	PrefillContendedBudgetTokens int64
	PrefillAggregateBudgetTokens int64
}

func (c Capability) Validate() error {
	if c.Fingerprint == "" || c.MaxModelLenTokens <= 0 || c.MaxModelLenTokens > 1<<53 ||
		c.KVCapacityTokens <= 0 || c.KVCapacityTokens > 1<<53 ||
		c.KVBlockSize <= 0 || c.KVBlockSize >= c.KVCapacityTokens ||
		c.KVHardLimitTokens <= 0 || c.KVHardLimitTokens >= c.KVCapacityTokens ||
		c.KVHardLimitTokens%c.KVBlockSize != 0 || c.MaximumInputTokens <= 0 ||
		c.MinimumDecodeHorizonTokens < 0 {
		return fmt.Errorf("admission capability geometry is invalid")
	}
	if c.MaximumInputTokens > c.MaxModelLenTokens-c.MinimumDecodeHorizonTokens ||
		c.MaximumInputTokens > c.KVHardLimitTokens-c.MinimumDecodeHorizonTokens {
		return fmt.Errorf("admission capability maximum input is invalid")
	}
	if c.PrefillRegularTokens <= 0 || c.PrefillExclusiveTokens <= c.PrefillRegularTokens ||
		c.PrefillQuiescentTokens <= c.PrefillExclusiveTokens ||
		c.PrefillContendedBudgetTokens <= 0 ||
		c.PrefillContendedBudgetTokens > c.PrefillRegularTokens ||
		c.PrefillAggregateBudgetTokens < c.PrefillContendedBudgetTokens ||
		c.PrefillAggregateBudgetTokens > c.PrefillQuiescentTokens {
		return fmt.Errorf("admission capability Prefill profile is invalid")
	}
	for _, value := range []int64{
		c.PrefillRegularTokens,
		c.PrefillExclusiveTokens,
		c.PrefillQuiescentTokens,
		c.PrefillContendedBudgetTokens,
		c.PrefillAggregateBudgetTokens,
	} {
		if value%c.KVBlockSize != 0 {
			return fmt.Errorf("admission capability Prefill alignment is invalid")
		}
	}
	if c.KVHardLimitTokens/c.KVBlockSize > int64(math.MaxInt) {
		return fmt.Errorf("admission capability reservation bound is invalid")
	}
	if _, err := c.minimumWork(); err != nil {
		return fmt.Errorf("admission capability minimum request is invalid: %w", err)
	}
	return nil
}

func (c Capability) minimumWork() (predictive.RequestWork, error) {
	return predictive.BuildRequestWork(predictive.RequestEstimate{
		SelectionInputTokens:     1,
		KVReservationInputTokens: 1,
		DecodeHorizonTokens:      c.MinimumDecodeHorizonTokens,
	}, c.KVBlockSize)
}

func (c Capability) matchesObservation(observation BackendObservation) bool {
	return observation.CapabilityFingerprint == c.Fingerprint &&
		observation.MaxModelLenTokens == c.MaxModelLenTokens &&
		observation.KVCapacityTokens == c.KVCapacityTokens &&
		observation.KVBlockSize == c.KVBlockSize
}
