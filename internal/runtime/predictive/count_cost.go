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

type UpperBoundAdmissionProposal struct {
	RequestID                    string
	InputTokensUpper             int64
	RawInputTokensHigh           int64
	DecodeHorizonUpper           int64
	AccruedLocalAdmissionLatency time.Duration
	Confidence                   float64
}

type CountRequestCost struct {
	ManifestID                   string
	BackendEpoch                 string
	InputTokens                  int64
	RequestComplexityTokensUpper int64
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
	if err := proposal.Analysis.Validate(identity.ManifestID, identity.BackendEpoch); err != nil {
		return CountRequestCost{}, domain.ReasonTokenizerProfileUnknown
	}
	return buildUpperBoundRequestCost(identity, modelMaximumLength, UpperBoundAdmissionProposal{
		RequestID:                    proposal.RequestID,
		InputTokensUpper:             proposal.Analysis.ExactInputTokens,
		RawInputTokensHigh:           proposal.Analysis.ExactInputTokens,
		DecodeHorizonUpper:           proposal.DecodeHorizonUpper,
		AccruedLocalAdmissionLatency: proposal.AccruedLocalAdmissionLatency,
		Confidence:                   proposal.Confidence,
	})
}

func buildUpperBoundRequestCost(identity CoordinatorIdentity, modelMaximumLength int64, proposal UpperBoundAdmissionProposal) (CountRequestCost, domain.Reason) {
	if proposal.RequestID == "" || proposal.InputTokensUpper < 0 || proposal.RawInputTokensHigh < 0 || proposal.DecodeHorizonUpper < 0 || proposal.AccruedLocalAdmissionLatency < 0 || !positiveFinite(proposal.Confidence) || proposal.Confidence > 1 {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	if proposal.InputTokensUpper > math.MaxInt64-proposal.DecodeHorizonUpper {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	activeContext := proposal.InputTokensUpper + proposal.DecodeHorizonUpper
	if activeContext > modelMaximumLength {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	kvUpper, ok := roundUpCountCost(activeContext, int64(identity.BlockSize))
	if !ok {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	inputKVUpper, ok := roundUpCountCost(proposal.InputTokensUpper, int64(identity.BlockSize))
	if !ok || inputKVUpper > kvUpper {
		return CountRequestCost{}, domain.ReasonPredictorProfileUnknown
	}
	futureKVUpper := kvUpper - inputKVUpper
	requestComplexity := proposal.RawInputTokensHigh
	if requestComplexity < proposal.InputTokensUpper {
		requestComplexity = proposal.InputTokensUpper
	}
	return CountRequestCost{
		ManifestID:                   identity.ManifestID,
		BackendEpoch:                 identity.BackendEpoch,
		InputTokens:                  proposal.InputTokensUpper,
		RequestComplexityTokensUpper: requestComplexity,
		AccruedLocalAdmissionLatency: proposal.AccruedLocalAdmissionLatency,
		PhysicalKVUpper:              kvUpper,
		ActiveKVUpper:                kvUpper,
		FuturePhysicalKVUpper:        futureKVUpper,
		FutureActiveKVUpper:          futureKVUpper,
		UncachedPrefillUpper:         proposal.InputTokensUpper,
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
		RequestComplexityTokensUpper: c.RequestComplexityTokensUpper,
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
