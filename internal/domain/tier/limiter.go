package tier

import (
	"sync/atomic"

	requesttier "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

type Limiter struct {
	basicInflight   atomic.Int64
	premiumInflight atomic.Int64
	basicWaiting    atomic.Int64
	premiumWaiting  atomic.Int64
	basicAccepted   atomic.Uint64
	premiumAccepted atomic.Uint64
	basicRejected   atomic.Uint64
	premiumRejected atomic.Uint64
}

type Snapshot struct {
	BasicInflight   int64
	PremiumInflight int64
	BasicWaiting    int64
	PremiumWaiting  int64
	BasicAccepted   uint64
	PremiumAccepted uint64
	BasicRejected   uint64
	PremiumRejected uint64
	BasicLimit      int
	PremiumReserved int
}

func (l *Limiter) Snapshot(globalLimit int) Snapshot {
	premiumInflight := l.premiumInflight.Load()
	basicLimit := BasicLimitWithPremium(globalLimit, premiumInflight)
	reserved := PremiumReserved(globalLimit, premiumInflight)
	return Snapshot{
		BasicInflight:   l.basicInflight.Load(),
		PremiumInflight: premiumInflight,
		BasicWaiting:    l.basicWaiting.Load(),
		PremiumWaiting:  l.premiumWaiting.Load(),
		BasicAccepted:   l.basicAccepted.Load(),
		PremiumAccepted: l.premiumAccepted.Load(),
		BasicRejected:   l.basicRejected.Load(),
		PremiumRejected: l.premiumRejected.Load(),
		BasicLimit:      basicLimit,
		PremiumReserved: reserved,
	}
}

func (l *Limiter) Acquire(tier requesttier.Tier, globalLimit int) (func(), string) {
	if tier == requesttier.Premium {
		l.premiumInflight.Add(1)
		return func() {
			l.premiumInflight.Add(-1)
		}, ""
	}
	if l.premiumWaiting.Load() > 0 {
		return nil, "tier_priority"
	}
	limit := BasicLimitWithPremium(globalLimit, l.premiumInflight.Load())
	if limit <= 0 {
		return nil, "tier_basic_limit"
	}
	for {
		current := l.basicInflight.Load()
		if current >= int64(limit) {
			return nil, "tier_basic_limit"
		}
		if l.basicInflight.CompareAndSwap(current, current+1) {
			return func() {
				l.basicInflight.Add(-1)
			}, ""
		}
	}
}

func (l *Limiter) MarkWaiting(tier requesttier.Tier) func() {
	if tier == requesttier.Premium {
		l.premiumWaiting.Add(1)
		return func() {
			l.premiumWaiting.Add(-1)
		}
	}
	l.basicWaiting.Add(1)
	return func() {
		l.basicWaiting.Add(-1)
	}
}

func (l *Limiter) ObserveAccepted(tier requesttier.Tier) {
	if tier == requesttier.Premium {
		l.premiumAccepted.Add(1)
		return
	}
	l.basicAccepted.Add(1)
}

func (l *Limiter) ObserveRejected(tier requesttier.Tier) {
	if tier == requesttier.Premium {
		l.premiumRejected.Add(1)
		return
	}
	l.basicRejected.Add(1)
}
