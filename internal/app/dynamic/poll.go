package dynamic

import (
	"fmt"
	"net/http"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
)

func (c *Controller) pollLoop() {
	client := &http.Client{Timeout: 2 * time.Second}
	ticker := time.NewTicker(c.cfg.PollInterval)
	defer ticker.Stop()
	c.pollMetrics(client)
	for range ticker.C {
		c.pollMetrics(client)
	}
}

func (c *Controller) pollMetrics(client *http.Client) {
	if c.cfg.BackendRouting && len(c.deps.Backends) > 0 {
		c.pollBackendMetrics(client)
		return
	}
	c.pollStaticMetricsURLs(client)
}

func (c *Controller) pollBackendMetrics(client *http.Client) {
	polls := make([]backendMetricPoll, 0, len(c.deps.Backends))
	for _, backend := range c.deps.Backends {
		if backend.MetricsURL() == "" {
			polls = append(polls, backendMetricPoll{
				Backend: backend,
				Err:     errMetricsURLEmpty,
			})
			continue
		}
		sample, err := prometheus.FetchSample(client, backend.MetricsURL())
		if err != nil {
			polls = append(polls, backendMetricPoll{
				Backend:      backend,
				Err:          err,
				CountFailure: true,
			})
			continue
		}
		polls = append(polls, backendMetricPoll{
			Backend: backend,
			Sample:  sample,
		})
	}
	samples, failed := normalizeBackendMetricPolls(polls, time.Now())
	if len(samples) == 0 {
		c.storeError(fmt.Errorf("all backend metrics unavailable"))
		return
	}
	c.updateFromMetricSamplesWithFailed(samples, failed)
}

func (c *Controller) pollStaticMetricsURLs(client *http.Client) {
	now := time.Now()
	polls := make([]staticMetricPoll, 0, len(c.cfg.MetricsURLs))
	for index, metricsURL := range c.cfg.MetricsURLs {
		sample, err := prometheus.FetchSample(client, metricsURL)
		polls = append(polls, staticMetricPoll{
			Key:    staticMetricKey(index, metricsURL),
			Sample: sample,
			Err:    err,
		})
	}
	samples, state, failed := normalizeStaticMetricPolls(c.previousStaticMetricState(), polls, now)
	c.storeStaticMetricState(state)
	if len(samples) == 0 {
		c.storeError(fmt.Errorf("all static metrics unavailable"))
		return
	}
	c.updateFromMetricSamplesWithFailed(samples, failed)
}
