package admission

type TPSDemandSource string

const (
	TPSDemandSourceRequest  TPSDemandSource = "request"
	TPSDemandSourceFallback TPSDemandSource = "fallback"
)

// TPSRequestDemand is the complete request input to TPS admission. Input-token,
// KV, cache, and Prefill estimates deliberately do not enter this value.
type TPSRequestDemand struct {
	DecodeSequences int64
	Source          TPSDemandSource
}

func NewTPSRequestDemand(decodeSequences int64) TPSRequestDemand {
	return TPSRequestDemand{DecodeSequences: decodeSequences, Source: TPSDemandSourceRequest}
}

func NewFallbackTPSRequestDemand() TPSRequestDemand {
	return TPSRequestDemand{DecodeSequences: 1, Source: TPSDemandSourceFallback}
}

func (d TPSRequestDemand) valid() bool {
	return d.DecodeSequences > 0 &&
		(d.Source == TPSDemandSourceRequest || d.Source == TPSDemandSourceFallback)
}
