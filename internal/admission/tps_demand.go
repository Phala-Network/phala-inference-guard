package admission

import "strings"

type TPSDemandSource string

const (
	TPSDemandSourceRequest  TPSDemandSource = "request"
	TPSDemandSourceFallback TPSDemandSource = "fallback"
)

// RequestTier is the scheduling tier a trusted ingress assigns to a request.
// Tiers are ordered by how costly a rejection is. Priority traffic is served
// from this node or not at all, so a rejection is final for the customer and
// the node never sheds it for load. Default and flex traffic have another
// route to fall over to, so they are the tiers the node sheds under load,
// flex first. The zero value is TierDefault, so a request without a tier is
// treated as resold traffic.
type RequestTier uint8

const (
	TierDefault RequestTier = iota
	TierFlex
	TierPriority
)

// ParseRequestTier maps one header value to a tier. Anything other than the
// exact names (case and surrounding space ignored) is TierDefault.
func ParseRequestTier(value string) RequestTier {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "priority":
		return TierPriority
	case "flex":
		return TierFlex
	default:
		return TierDefault
	}
}

func (t RequestTier) String() string {
	switch t {
	case TierPriority:
		return "priority"
	case TierFlex:
		return "flex"
	default:
		return "default"
	}
}

// Rank orders tiers for queueing: a higher rank is served first.
func (t RequestTier) Rank() int {
	switch t {
	case TierPriority:
		return 2
	case TierDefault:
		return 1
	default:
		return 0
	}
}

// Sheddable reports whether load protection applies to the tier: the
// waiting allowance, preemption, the running limit, the window bound and,
// with backend tier scheduling, TPS health. Default and flex are sheddable;
// priority is held to availability protection alone.
func (t RequestTier) Sheddable() bool {
	return t == TierDefault || t == TierFlex
}

func (t RequestTier) valid() bool {
	return t == TierDefault || t == TierFlex || t == TierPriority
}

// RequestTiers lists every tier in a fixed order for metrics.
var RequestTiers = [...]RequestTier{TierPriority, TierDefault, TierFlex}

// TPSRequestDemand is the complete request input to admission. PrefillTokens
// is the estimated uncached Prompt cost when PrefillKnown is set.
type TPSRequestDemand struct {
	DecodeSequences int64
	Source          TPSDemandSource
	Tier            RequestTier
	PrefillTokens   int64
	PrefillKnown    bool
}

func NewTPSRequestDemand(decodeSequences int64) TPSRequestDemand {
	return TPSRequestDemand{DecodeSequences: decodeSequences, Source: TPSDemandSourceRequest}
}

func NewFallbackTPSRequestDemand() TPSRequestDemand {
	return TPSRequestDemand{DecodeSequences: 1, Source: TPSDemandSourceFallback}
}

// WithTier returns a demand carrying a valid tier. Unknown values are
// normalized to TierDefault rather than gaining priority.
func (d TPSRequestDemand) WithTier(tier RequestTier) TPSRequestDemand {
	if !tier.valid() {
		tier = TierDefault
	}
	d.Tier = tier
	return d
}

// WithPrefillTokens returns a demand carrying an estimated uncached Prompt
// cost. Negative estimates are clamped to zero.
func (d TPSRequestDemand) WithPrefillTokens(tokens int64) TPSRequestDemand {
	if tokens < 0 {
		tokens = 0
	}
	d.PrefillTokens = tokens
	d.PrefillKnown = true
	return d
}

func (d TPSRequestDemand) valid() bool {
	return d.DecodeSequences > 0 &&
		(d.Source == TPSDemandSourceRequest || d.Source == TPSDemandSourceFallback) &&
		d.Tier.valid()
}
