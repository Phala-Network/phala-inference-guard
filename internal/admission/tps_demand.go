package admission

type TPSDemandSource string

const (
	TPSDemandSourceRequest  TPSDemandSource = "request"
	TPSDemandSourceFallback TPSDemandSource = "fallback"
)

// RequestPriority is used only to order requests that are concurrently
// waiting for a local admission decision. It never changes the TPS policy or
// any hard capacity bound.
type RequestPriority uint8

const (
	RequestPriorityBasic RequestPriority = iota
	RequestPriorityPremium
)

func (p RequestPriority) String() string {
	if p == RequestPriorityPremium {
		return "premium"
	}
	return "basic"
}

func (p RequestPriority) valid() bool {
	return p == RequestPriorityBasic || p == RequestPriorityPremium
}

// TPSRequestDemand is the complete request input to TPS admission. Input-token,
// KV, cache, and Prefill estimates deliberately do not enter this value.
type TPSRequestDemand struct {
	DecodeSequences int64
	Source          TPSDemandSource
	Priority        RequestPriority
}

func NewTPSRequestDemand(decodeSequences int64) TPSRequestDemand {
	return TPSRequestDemand{DecodeSequences: decodeSequences, Source: TPSDemandSourceRequest}
}

func NewFallbackTPSRequestDemand() TPSRequestDemand {
	return TPSRequestDemand{DecodeSequences: 1, Source: TPSDemandSourceFallback}
}

// WithPriority returns a demand carrying a valid local ordering hint. Unknown
// values are deliberately normalized to basic rather than gaining priority.
func (d TPSRequestDemand) WithPriority(priority RequestPriority) TPSRequestDemand {
	if !priority.valid() {
		priority = RequestPriorityBasic
	}
	d.Priority = priority
	return d
}

func (d TPSRequestDemand) valid() bool {
	return d.DecodeSequences > 0 &&
		(d.Source == TPSDemandSourceRequest || d.Source == TPSDemandSourceFallback) &&
		d.Priority.valid()
}
