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
	generation := c.configGeneration.Load()
	cfg := c.AdmissionConfig()
	if cfg.BackendRouting && len(c.deps.Backends) > 0 {
		c.pollBackendMetricsForGeneration(client, generation)
		return
	}
	c.pollStaticMetricsURLsForGeneration(client, cfg.MetricsURLs, generation)
}

func (c *Controller) pollBackendMetrics(client *http.Client) {
	c.pollBackendMetricsForGeneration(client, c.configGeneration.Load())
}

func (c *Controller) pollBackendMetricsForGeneration(client *http.Client, generation uint64) {
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
	c.publishMu.Lock()
	if generation != c.configGeneration.Load() {
		c.publishMu.Unlock()
		return
	}
	samples, failed := normalizeBackendMetricPolls(polls, time.Now())
	c.publishMu.Unlock()
	if len(samples) == 0 {
		c.storeErrorForGeneration(fmt.Errorf("all backend metrics unavailable"), generation)
		return
	}
	c.updateFromMetricSamplesForGeneration(samples, failed, generation)
}

func (c *Controller) pollStaticMetricsURLs(client *http.Client) {
	cfg := c.AdmissionConfig()
	c.pollStaticMetricsURLsForGeneration(client, cfg.MetricsURLs, c.configGeneration.Load())
}

func (c *Controller) pollStaticMetricsURLsForGeneration(client *http.Client, metricsURLs []string, generation uint64) {
	now := time.Now()
	polls := make([]staticMetricPoll, 0, len(metricsURLs))
	for index, metricsURL := range metricsURLs {
		sample, err := prometheus.FetchSample(client, metricsURL)
		polls = append(polls, staticMetricPoll{
			Key:    staticMetricKey(index, metricsURL),
			Sample: sample,
			Err:    err,
		})
	}
	samples, state, failed := normalizeStaticMetricPolls(c.previousStaticMetricState(), polls, now)
	c.publishMu.Lock()
	if generation != c.configGeneration.Load() {
		c.publishMu.Unlock()
		return
	}
	c.storeStaticMetricState(state)
	c.publishMu.Unlock()
	if len(samples) == 0 {
		c.storeErrorForGeneration(fmt.Errorf("all static metrics unavailable"), generation)
		return
	}
	c.updateFromMetricSamplesForGeneration(samples, failed, generation)
}
