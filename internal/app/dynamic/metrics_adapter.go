package dynamic

import (
	"errors"
	"fmt"
	"time"

	runtimebackend "github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

var errMetricsURLEmpty = errors.New("metrics_url_empty")

type backendMetricPoll struct {
	Backend      Backend
	Sample       telemetry.Sample
	Err          error
	CountFailure bool
}

type staticMetricPoll struct {
	Key    string
	Sample telemetry.Sample
	Err    error
}

type staticMetricState map[string]runtimebackend.Runtime

func staticMetricKey(index int, metricsURL string) string {
	return fmt.Sprintf("metrics-url-%d:%s", index+1, metricsURL)
}

func (c *Controller) previousStaticMetricState() staticMetricState {
	raw := c.staticMetricsState.Load()
	previous, ok := raw.(staticMetricState)
	if !ok || previous == nil {
		return nil
	}
	copy := make(staticMetricState, len(previous))
	for key, status := range previous {
		copy[key] = status
	}
	return copy
}

func (c *Controller) storeStaticMetricState(state staticMetricState) {
	c.staticMetricsState.Store(state)
}

func normalizeStaticMetricPolls(previous staticMetricState, polls []staticMetricPoll, now time.Time) ([]telemetry.Sample, staticMetricState, int) {
	samples := make([]telemetry.Sample, 0, len(polls))
	next := make(staticMetricState, len(polls))
	failed := 0
	for _, poll := range polls {
		if poll.Err != nil {
			failed++
			next[poll.Key] = runtimebackend.Runtime{
				Name:    poll.Key,
				Failed:  true,
				Error:   poll.Err.Error(),
				Updated: now,
			}
			continue
		}
		status := runtimebackend.FromSample(poll.Key, poll.Sample, previous[poll.Key], now)
		next[poll.Key] = status
		samples = append(samples, runtimebackend.NormalizeSample(poll.Sample, status))
	}
	return samples, next, failed
}

func normalizeBackendMetricPolls(polls []backendMetricPoll, now time.Time) ([]telemetry.Sample, int) {
	samples := make([]telemetry.Sample, 0, len(polls))
	failed := 0
	for _, poll := range polls {
		if poll.Err != nil {
			failed++
			if poll.CountFailure {
				poll.Backend.ObserveMetricsFailure()
			}
			poll.Backend.StoreStatus(runtimebackend.Runtime{
				Name:    poll.Backend.Name(),
				Failed:  true,
				Error:   poll.Err.Error(),
				Updated: now,
			})
			continue
		}
		status := poll.Backend.UpdateStatusFromSample(poll.Sample)
		samples = append(samples, runtimebackend.NormalizeSample(poll.Sample, status))
	}
	return samples, failed
}
