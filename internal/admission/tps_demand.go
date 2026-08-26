package admission

import (
	"fmt"

	predictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type TPSDemandSource string

const (
	TPSDemandSourceEstimate TPSDemandSource = "estimate"
	TPSDemandSourceFallback TPSDemandSource = "fallback"
)

// TPSRequestDemand is the complete request input to TPS admission. Input-token,
// KV, cache, and Prefill estimates deliberately do not enter this value.
type TPSRequestDemand struct {
	DecodeSequences   int64
	OutputLimitTokens int64
	OutputLimitKnown  bool
	Source            TPSDemandSource
}

func tpsRequestDemandFromEstimate(estimate predictive.RequestEstimate) (TPSRequestDemand, error) {
	sequences := estimate.DecodeSequences
	source := TPSDemandSourceEstimate
	if sequences == 0 {
		sequences = 1
		source = TPSDemandSourceFallback
	}
	if sequences < 0 || estimate.OutputLimitTokens < 0 ||
		(!estimate.OutputLimitKnown && estimate.OutputLimitTokens != 0) ||
		estimate.BasePromptCount < 0 {
		return TPSRequestDemand{}, fmt.Errorf("TPS request demand is invalid")
	}
	if estimate.BasePromptCount > 0 &&
		(estimate.BasePromptCount > sequences || sequences%estimate.BasePromptCount != 0) {
		return TPSRequestDemand{}, fmt.Errorf("TPS request batch shape is invalid")
	}
	return TPSRequestDemand{
		DecodeSequences:   sequences,
		OutputLimitTokens: estimate.OutputLimitTokens,
		OutputLimitKnown:  estimate.OutputLimitKnown,
		Source:            source,
	}, nil
}

func (d TPSRequestDemand) valid() bool {
	return d.DecodeSequences > 0 && d.OutputLimitTokens >= 0 &&
		(d.OutputLimitKnown || d.OutputLimitTokens == 0) &&
		(d.Source == TPSDemandSourceEstimate || d.Source == TPSDemandSourceFallback)
}

func (d TPSRequestDemand) gateDemand() tpsAdmissionDemand {
	return tpsAdmissionDemand{
		additionalSequences: d.DecodeSequences,
		outputLimitTokens:   d.OutputLimitTokens,
		outputLimitKnown:    d.OutputLimitKnown,
	}
}
