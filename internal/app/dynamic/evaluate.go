package dynamic

import (
	"time"

	domaindynamic "github.com/Phala-Network/phala-inference-guard/internal/domain/dynamic"
	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func (c *Controller) updateFromMetricSamplesWithFailed(samples []telemetry.Sample, backendFailed int) runtimedynamic.Snapshot {
	now := time.Now()
	prefillProtected := 0
	if c.cfg.UserTPSEnabled && c.deps.PrefillProtected != nil {
		prefillProtected = c.deps.PrefillProtected(now)
	}
	previous := c.previousMetrics()
	snapshot := domaindynamic.Evaluate(c.cfg.policyConfig(), domaindynamic.Input{
		Now:              now,
		Samples:          samples,
		BackendFailed:    backendFailed,
		Previous:         previous,
		SemanticTTFT:     c.semanticTTFT(),
		PrefillProtected: prefillProtected,
		GlobalLimit:      c.globalLimit(),
		QueueCurrent:     c.queueCurrent(),
		DynamicRejected:  c.dynamicRejected(),
		Tier:             c.tierSnapshot(previous.Snapshot.GlobalLimit),
		PressureCap:      &c.pressureCap,
	})
	c.snapshot.Store(snapshot)
	c.lastMetricsSnapshot.Store(snapshot)
	c.pollOK.Add(1)
	c.notify()
	return snapshot
}

func (c *Controller) previousMetrics() domaindynamic.PreviousMetrics {
	previous := domaindynamic.PreviousMetrics{}
	if raw := c.lastMetricsSnapshot.Load(); raw != nil {
		if snapshot, ok := raw.(runtimedynamic.Snapshot); ok && snapshot.Source == "metrics" {
			previous = domaindynamic.PreviousMetrics{
				Snapshot:           snapshot,
				UserTPSYellowCount: snapshot.UserTPSYellowCount,
				UserTPSRedCount:    snapshot.UserTPSRedCount,
			}
		}
	}
	if raw := c.snapshot.Load(); raw != nil {
		if snapshot, ok := raw.(runtimedynamic.Snapshot); ok && snapshot.Source == "metrics" {
			previous = domaindynamic.PreviousMetrics{
				Snapshot:           snapshot,
				UserTPSYellowCount: snapshot.UserTPSYellowCount,
				UserTPSRedCount:    snapshot.UserTPSRedCount,
			}
		}
	}
	return previous
}
