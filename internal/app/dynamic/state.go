package dynamic

import (
	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
	domaintier "github.com/Phala-Network/phala-inference-guard/internal/domain/tier"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

func (c *Controller) Snapshot() dynamic.Snapshot {
	if c == nil {
		return dynamic.Snapshot{}
	}
	raw := c.snapshot.Load()
	snapshot, ok := raw.(dynamic.Snapshot)
	if !ok {
		return dynamic.Snapshot{}
	}
	return snapshot
}

func (c *Controller) State() string {
	if c == nil || !c.cfg.Enabled {
		return "green"
	}
	state := c.Snapshot().DecisionState()
	if !decision.ValidState(state) {
		return c.cfg.FailsafeState
	}
	return state
}

func (c *Controller) GlobalLimit() int {
	if c == nil || !c.cfg.Enabled {
		return 0
	}
	return c.Snapshot().GlobalLimit
}

func (c *Controller) BackendUnavailableActive() bool {
	if c == nil {
		return false
	}
	snapshot := c.Snapshot()
	if snapshot.BackendCount > 0 && snapshot.BackendFailed >= snapshot.BackendCount {
		return true
	}
	return decision.ContainsReason(snapshot.DecisionYellowReasons(), "backend_unavailable") ||
		decision.ContainsReason(snapshot.DecisionRedReasons(), "backend_unavailable")
}

func (c *Controller) CapacityRatio() float64 {
	if c == nil {
		return 0
	}
	ratio := c.cfg.UserTPSCapacityRatio
	if snapshot := c.Snapshot(); snapshot.CapacityRatio > 0 {
		ratio = snapshot.CapacityRatio
	}
	return num.ClampFloat(ratio, c.cfg.UserTPSCapacityRatio, c.cfg.UserTPSCapacityRatioMax)
}

func (c *Controller) Counters() Counters {
	if c == nil {
		return Counters{}
	}
	return Counters{
		PollOK:     c.pollOK.Load(),
		PollFailed: c.pollFailed.Load(),
	}
}

func (c *Controller) PressureCap() *capacity.PressureCap {
	if c == nil {
		return nil
	}
	return &c.pressureCap
}

func (c *Controller) backendCount() int {
	if c.cfg.BackendRouting && len(c.deps.Backends) > 0 {
		return len(c.deps.Backends)
	}
	return len(c.cfg.MetricsURLs)
}

func (c *Controller) semanticTTFT() telemetry.HistogramSample {
	if c.deps.SemanticTTFT == nil {
		return telemetry.HistogramSample{}
	}
	return c.deps.SemanticTTFT()
}

func (c *Controller) globalLimit() int {
	if c.deps.GlobalLimit == nil {
		return 0
	}
	return c.deps.GlobalLimit()
}

func (c *Controller) queueCurrent() int64 {
	if c.deps.QueueCurrent == nil {
		return 0
	}
	return c.deps.QueueCurrent()
}

func (c *Controller) dynamicRejected() uint64 {
	if c.deps.DynamicRejected == nil {
		return 0
	}
	return c.deps.DynamicRejected()
}

func (c *Controller) tierSnapshot(activeLimit int) domaintier.Snapshot {
	if c.deps.TierSnapshot == nil {
		return domaintier.Snapshot{}
	}
	if activeLimit <= 0 {
		if snapshot := c.Snapshot(); snapshot.GlobalLimit > 0 {
			activeLimit = snapshot.GlobalLimit
		}
	}
	if activeLimit <= 0 {
		activeLimit = c.globalLimit()
	}
	return c.deps.TierSnapshot(activeLimit)
}

func (c *Controller) notify() {
	if c.deps.Notify != nil {
		c.deps.Notify()
	}
}
