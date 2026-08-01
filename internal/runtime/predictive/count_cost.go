package predictive

import (
	"math"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type CountAdmissionProposal struct {
	RequestID                    string
	Analysis                     TokenCountAnalysis
	DecodeHorizonUpper           int64
	AccruedLocalAdmissionLatency time.Duration
	Confidence                   float64
}

type CountRequestCost struct {
	ManifestID                   string
	BackendEpoch                 string
	InputTokens                  int64
	AccruedLocalAdmissionLatency time.Duration
	PhysicalKVUpper              int64
	ActiveKVUpper                int64
	FuturePhysicalKVUpper        int64
	FutureActiveKVUpper          int64
	UncachedPrefillUpper         int64
	DecodeHorizonUpper           int64
	DecodeSequencesUpper         int
	ActiveContextTokensUpper     int64
	FutureContextTokensUpper     int64
	Confidence                   float64
}

func buildCountRequestCost(identity CoordinatorIdentity, modelMaximumLength int64, proposal CountAdmissionProposal) (CountRequestCost, domain.Reason) {
	if proposal.RequestID == "" || proposal.DecodeHorizonUpper < 0 || proposal.AccruedLocalAdmissionLatency < 0 || !positiveFinite(proposal.Confidence) || proposal.Confidence > 1 {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	if err := proposal.Analysis.Validate(identity.ManifestID, identity.BackendEpoch); err != nil {
		return CountRequestCost{}, domain.ReasonTokenizerProfileUnknown
	}
	if proposal.Analysis.ExactInputTokens > math.MaxInt64-proposal.DecodeHorizonUpper {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	activeContext := proposal.Analysis.ExactInputTokens + proposal.DecodeHorizonUpper
	if activeContext > modelMaximumLength {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	kvUpper, ok := roundUpCountCost(activeContext, int64(identity.BlockSize))
	if !ok {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	inputKVUpper, ok := roundUpCountCost(proposal.Analysis.ExactInputTokens, int64(identity.BlockSize))
	if !ok || inputKVUpper > kvUpper {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	futureKVUpper := kvUpper - inputKVUpper
	return CountRequestCost{
		ManifestID:                   identity.ManifestID,
		BackendEpoch:                 identity.BackendEpoch,
		InputTokens:                  proposal.Analysis.ExactInputTokens,
		AccruedLocalAdmissionLatency: proposal.AccruedLocalAdmissionLatency,
		PhysicalKVUpper:              kvUpper,
		ActiveKVUpper:                kvUpper,
		FuturePhysicalKVUpper:        futureKVUpper,
		FutureActiveKVUpper:          futureKVUpper,
		UncachedPrefillUpper:         proposal.Analysis.ExactInputTokens,
		DecodeHorizonUpper:           proposal.DecodeHorizonUpper,
		DecodeSequencesUpper:         1,
		ActiveContextTokensUpper:     activeContext,
		FutureContextTokensUpper:     proposal.DecodeHorizonUpper,
		Confidence:                   proposal.Confidence,
	}, domain.ReasonFit
}

func (c CountRequestCost) managerCost() domain.RequestCost {
	return domain.RequestCost{
		ManifestID:                   c.ManifestID,
		InputTokens:                  c.InputTokens,
		AccruedLocalAdmissionLatency: c.AccruedLocalAdmissionLatency,
		KV: domain.KVIncrement{
			PhysicalKVUpper: c.PhysicalKVUpper,
			ActiveKVUpper:   c.ActiveKVUpper,
		},
		FutureKV: domain.KVIncrement{
			PhysicalKVUpper: c.FuturePhysicalKVUpper,
			ActiveKVUpper:   c.FutureActiveKVUpper,
		},
		UncachedPrefillUpper:     c.UncachedPrefillUpper,
		DecodeHorizonUpper:       c.DecodeHorizonUpper,
		DecodeSequencesUpper:     c.DecodeSequencesUpper,
		ActiveContextTokensUpper: c.ActiveContextTokensUpper,
		FutureContextTokensUpper: c.FutureContextTokensUpper,
		Confidence:               c.Confidence,
	}
}

func roundUpCountCost(value, blockSize int64) (int64, bool) {
	if value <= 0 {
		return 0, true
	}
	if blockSize <= 0 || value > math.MaxInt64-(blockSize-1) {
		return 0, false
	}
	return ((value + blockSize - 1) / blockSize) * blockSize, true
}
