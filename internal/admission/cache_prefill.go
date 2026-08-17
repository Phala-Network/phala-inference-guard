package admission

import (
	"math"
	"time"
)

const (
	cachePrefillMinimumEvidenceTokens uint64        = 4 * 1024
	cachePrefillMaximumHitCredit      float64       = 0.75
	cachePrefillObservationLifetime   time.Duration = time.Second
)

type cachePrefillObservation struct {
	valid          bool
	hitFraction    float64
	creditFraction float64
	evidenceTokens uint64
	spentTokens    int64
	leaseSequence  uint64
	observedAt     time.Time
}

func nextCachePrefillObservation(
	previous observedState,
	current BackendObservation,
) (cachePrefillObservation, bool) {
	if !previous.observation.CacheCountersValid || !current.CacheCountersValid ||
		current.CacheHitTokensTotal > current.CacheQueryTokensTotal ||
		previous.observation.CacheHitTokensTotal > previous.observation.CacheQueryTokensTotal ||
		current.CacheQueryTokensTotal < previous.observation.CacheQueryTokensTotal ||
		current.CacheHitTokensTotal < previous.observation.CacheHitTokensTotal ||
		current.ObservedAt.Before(previous.observation.ObservedAt) {
		return cachePrefillObservation{}, false
	}
	queryDelta := current.CacheQueryTokensTotal - previous.observation.CacheQueryTokensTotal
	hitDelta := current.CacheHitTokensTotal - previous.observation.CacheHitTokensTotal
	if hitDelta > queryDelta {
		return cachePrefillObservation{}, false
	}
	if queryDelta == 0 {
		if previous.cache.valid && !current.ObservedAt.Before(previous.cache.observedAt) &&
			current.ObservedAt.Sub(previous.cache.observedAt) <= cachePrefillObservationLifetime {
			return previous.cache, false
		}
		return cachePrefillObservation{}, false
	}
	if queryDelta < cachePrefillMinimumEvidenceTokens {
		return cachePrefillObservation{}, false
	}
	hitFraction := float64(hitDelta) / float64(queryDelta)
	if math.IsNaN(hitFraction) || math.IsInf(hitFraction, 0) || hitFraction < 0 || hitFraction > 1 {
		return cachePrefillObservation{}, false
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
	}, true
}

func validCachePrefillObservation(value cachePrefillObservation) bool {
	if !value.valid {
		return value.hitFraction == 0 && value.creditFraction == 0 &&
			value.evidenceTokens == 0 && value.spentTokens == 0 &&
			value.leaseSequence == 0 && value.observedAt.IsZero()
	}
	budget := cachePrefillCreditTokenBudget(value.evidenceTokens, value.creditFraction)
	return !value.observedAt.IsZero() && value.leaseSequence > 0 &&
		value.evidenceTokens >= cachePrefillMinimumEvidenceTokens &&
		finiteNonnegative(value.hitFraction) && value.hitFraction <= 1 &&
		finiteNonnegative(value.creditFraction) && value.creditFraction <= cachePrefillMaximumHitCredit &&
		value.creditFraction <= value.hitFraction && value.spentTokens >= 0 && value.spentTokens <= budget
}

func cachePrefillObservationActiveAt(value cachePrefillObservation, now time.Time) bool {
	if !value.valid || now.IsZero() || now.Before(value.observedAt) {
		return false
	}
	return now.Sub(value.observedAt) <= cachePrefillObservationLifetime
}

func cachePrefillCreditTokenBudget(evidenceTokens uint64, creditFraction float64) int64 {
	if evidenceTokens == 0 || !finiteNonnegative(creditFraction) || creditFraction <= 0 ||
		creditFraction > cachePrefillMaximumHitCredit {
		return 0
	}
	credit := math.Floor(float64(evidenceTokens) * creditFraction)
	if credit <= 0 {
		return 0
	}
	if credit >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(credit)
}

func spendCachePrefillCredit(
	observation cachePrefillObservation,
	tokens int64,
) (cachePrefillObservation, uint64, bool) {
	if tokens < 0 || !validCachePrefillObservation(observation) {
		return cachePrefillObservation{}, 0, false
	}
	if tokens == 0 {
		return observation, 0, true
	}
	budget := cachePrefillCreditTokenBudget(observation.evidenceTokens, observation.creditFraction)
	if tokens > budget-observation.spentTokens {
		return cachePrefillObservation{}, 0, false
	}
	next := observation
	next.spentTokens += tokens
	return next, observation.leaseSequence, true
}

func refundCachePrefillCredit(
	observation cachePrefillObservation,
	leaseSequence uint64,
	tokens int64,
) (cachePrefillObservation, bool) {
	if tokens < 0 || (tokens > 0 && leaseSequence == 0) ||
		!validCachePrefillObservation(observation) {
		return cachePrefillObservation{}, false
	}
	if tokens == 0 || observation.leaseSequence != leaseSequence {
		return observation, true
	}
	if observation.spentTokens < tokens {
		return cachePrefillObservation{}, false
	}
	next := observation
	next.spentTokens -= tokens
	return next, true
}
