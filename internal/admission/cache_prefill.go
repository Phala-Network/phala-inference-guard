package admission

import (
	"math"
	"time"
)

const (
	cachePrefillMinimumEvidenceTokens uint64        = 4 * 1024
	cachePrefillMaximumHitCredit      float64       = 0.75
	cachePrefillObservationLifetime   time.Duration = 10 * time.Second
)

type cachePrefillObservation struct {
	valid          bool
	hitFraction    float64
	creditFraction float64
	evidenceTokens uint64
	observedAt     time.Time
}

func nextCachePrefillObservation(
	previous observedState,
	current BackendObservation,
) cachePrefillObservation {
	if !previous.observation.CacheCountersValid || !current.CacheCountersValid ||
		current.CacheHitTokensTotal > current.CacheQueryTokensTotal ||
		previous.observation.CacheHitTokensTotal > previous.observation.CacheQueryTokensTotal ||
		current.CacheQueryTokensTotal < previous.observation.CacheQueryTokensTotal ||
		current.CacheHitTokensTotal < previous.observation.CacheHitTokensTotal ||
		current.ObservedAt.Before(previous.observation.ObservedAt) {
		return cachePrefillObservation{}
	}
	queryDelta := current.CacheQueryTokensTotal - previous.observation.CacheQueryTokensTotal
	hitDelta := current.CacheHitTokensTotal - previous.observation.CacheHitTokensTotal
	if hitDelta > queryDelta {
		return cachePrefillObservation{}
	}
	if queryDelta == 0 {
		if previous.cache.valid && !current.ObservedAt.Before(previous.cache.observedAt) &&
			current.ObservedAt.Sub(previous.cache.observedAt) <= cachePrefillObservationLifetime {
			return previous.cache
		}
		return cachePrefillObservation{}
	}
	if queryDelta < cachePrefillMinimumEvidenceTokens {
		return cachePrefillObservation{}
	}
	hitFraction := float64(hitDelta) / float64(queryDelta)
	if math.IsNaN(hitFraction) || math.IsInf(hitFraction, 0) || hitFraction < 0 || hitFraction > 1 {
		return cachePrefillObservation{}
	}
	credit := hitFraction
	if credit > cachePrefillMaximumHitCredit {
		credit = cachePrefillMaximumHitCredit
	}
	return cachePrefillObservation{
		valid:          true,
		hitFraction:    hitFraction,
		creditFraction: credit,
		evidenceTokens: queryDelta,
		observedAt:     current.ObservedAt,
	}
}

func validCachePrefillObservation(value cachePrefillObservation) bool {
	if !value.valid {
		return value.hitFraction == 0 && value.creditFraction == 0 &&
			value.evidenceTokens == 0 && value.observedAt.IsZero()
	}
	return !value.observedAt.IsZero() && value.evidenceTokens >= cachePrefillMinimumEvidenceTokens &&
		finiteNonnegative(value.hitFraction) && value.hitFraction <= 1 &&
		finiteNonnegative(value.creditFraction) && value.creditFraction <= cachePrefillMaximumHitCredit &&
		value.creditFraction <= value.hitFraction
}
