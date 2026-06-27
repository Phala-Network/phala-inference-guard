package gate

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/lane"
	requesttier "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/tier"
)

type LimitFunc func() (limit int, dynamic bool, rejectCode string)
type QueueWaitFunc func(code string) time.Duration

type Config struct {
	QueueWait time.Duration
	QueuePoll time.Duration
}

type Gate struct {
	cfg             Config
	global          *lane.Lane
	currentLimit    LimitFunc
	effectiveWait   QueueWaitFunc
	tierLimiter     tier.Limiter
	queueCurrent    atomic.Int64
	queueTotal      atomic.Uint64
	queueTimeout    atomic.Uint64
	queueWaitCount  atomic.Uint64
	queueWaitNs     atomic.Uint64
	dynamicRejected atomic.Uint64
	notifyMu        sync.Mutex
	notify          chan struct{}
}

func New(cfg Config, global *lane.Lane, currentLimit LimitFunc, effectiveWait QueueWaitFunc) *Gate {
	return &Gate{
		cfg:           cfg,
		global:        global,
		currentLimit:  currentLimit,
		effectiveWait: effectiveWait,
		notify:        make(chan struct{}),
	}
}

func (g *Gate) QueueCurrent() int64 {
	return g.queueCurrent.Load()
}

func (g *Gate) QueueTotal() uint64 {
	return g.queueTotal.Load()
}

func (g *Gate) QueueTimeout() uint64 {
	return g.queueTimeout.Load()
}

func (g *Gate) QueueWaitCount() uint64 {
	return g.queueWaitCount.Load()
}

func (g *Gate) QueueWaitSecondsSum() float64 {
	return float64(g.queueWaitNs.Load()) / float64(time.Second)
}

func (g *Gate) DynamicRejected() uint64 {
	return g.dynamicRejected.Load()
}

func (g *Gate) TierSnapshot(globalLimit int) tier.Snapshot {
	return g.tierLimiter.Snapshot(globalLimit)
}

func (g *Gate) ObserveAccepted(tier requesttier.Tier) {
	g.tierLimiter.ObserveAccepted(tier)
}

func (g *Gate) Notify() {
	g.notifyMu.Lock()
	defer g.notifyMu.Unlock()
	if g.notify == nil {
		g.notify = make(chan struct{})
		return
	}
	close(g.notify)
	g.notify = make(chan struct{})
}

func (g *Gate) WaitAcquire(ctx context.Context, ln *lane.Lane, tier requesttier.Tier) (func(), string, time.Duration) {
	release, code := g.tryAcquire(ln, tier)
	if release != nil {
		return release, "", 0
	}
	if g.cfg.QueueWait <= 0 {
		return nil, code, 0
	}
	queueWait := g.effectiveQueueWait(code)
	if queueWait <= 0 {
		return nil, code, 0
	}

	started := time.Now()
	deadline := time.NewTimer(queueWait)
	defer deadline.Stop()
	fallback := time.NewTimer(g.queueFallbackPoll())
	defer fallback.Stop()
	notify := g.waitNotify()

	g.queueTotal.Add(1)
	g.queueCurrent.Add(1)
	defer g.queueCurrent.Add(-1)
	doneTierWaiting := g.tierLimiter.MarkWaiting(tier)
	defer doneTierWaiting()

	tryAgain := func() (func(), bool) {
		release, nextCode := g.tryAcquire(ln, tier)
		if release != nil {
			g.observeQueueWait(time.Since(started))
			return release, true
		}
		code = nextCode
		return nil, false
	}

	for {
		select {
		case <-ctx.Done():
			waited := time.Since(started)
			g.observeQueueWait(waited)
			g.queueTimeout.Add(1)
			return nil, code, waited
		case <-deadline.C:
			waited := time.Since(started)
			g.observeQueueWait(waited)
			g.queueTimeout.Add(1)
			return nil, code, waited
		case <-notify:
			if release, ok := tryAgain(); ok {
				return release, "", time.Since(started)
			}
			notify = g.waitNotify()
		case <-fallback.C:
			if release, ok := tryAgain(); ok {
				return release, "", time.Since(started)
			}
			fallback.Reset(g.queueFallbackPoll())
		}
	}
}

func (g *Gate) ObserveReject(ln *lane.Lane, tier requesttier.Tier, code string) {
	g.tierLimiter.ObserveRejected(tier)
	switch code {
	case "global_limit":
		g.global.ObserveRejected()
	case "global_dynamic_limit":
		g.dynamicRejected.Add(1)
	case "tier_priority", "tier_basic_limit":
		g.dynamicRejected.Add(1)
		if ln != nil {
			ln.ObserveRejected()
		}
	default:
		if ln != nil {
			ln.ObserveRejected()
		}
	}
}

func (g *Gate) tryAcquire(ln *lane.Lane, tier requesttier.Tier) (func(), string) {
	limit, dynamic, rejectCode := g.currentLimit()
	if rejectCode != "" {
		return nil, rejectCode
	}
	if dynamic && g.global.Inflight() >= int64(limit) {
		return nil, "global_dynamic_limit"
	}
	releaseTier, tierReject := g.tierLimiter.Acquire(tier, limit)
	if releaseTier == nil {
		return nil, tierReject
	}
	if !g.global.TryAcquire() {
		releaseTier()
		return nil, "global_limit"
	}
	if dynamic && g.global.Inflight() > int64(limit) {
		g.global.Release()
		releaseTier()
		return nil, "global_dynamic_limit"
	}
	ln.AcquireUnbounded()
	return func() {
		ln.ReleaseUnbounded()
		g.global.Release()
		releaseTier()
		g.Notify()
	}, ""
}

func (g *Gate) effectiveQueueWait(code string) time.Duration {
	if g.effectiveWait == nil {
		return 0
	}
	return g.effectiveWait(code)
}

func (g *Gate) waitNotify() <-chan struct{} {
	g.notifyMu.Lock()
	defer g.notifyMu.Unlock()
	if g.notify == nil {
		g.notify = make(chan struct{})
	}
	return g.notify
}

func (g *Gate) queueFallbackPoll() time.Duration {
	poll := g.cfg.QueuePoll
	if poll <= 0 {
		return 250 * time.Millisecond
	}
	poll *= 10
	if poll < 50*time.Millisecond {
		return 50 * time.Millisecond
	}
	if poll > time.Second {
		return time.Second
	}
	return poll
}

func (g *Gate) observeQueueWait(waited time.Duration) {
	if waited <= 0 {
		return
	}
	g.queueWaitCount.Add(1)
	g.queueWaitNs.Add(uint64(waited.Nanoseconds()))
}
