package pigconfig

import (
	"os"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

func loadBackendRoutingConfig(cfg *Config) {
	dynamicMetricsURL := strings.TrimRight(env.String("DYNAMIC_METRICS_URL", ""), "/")
	dynamicMetricsURLs := normalizeDynamicMetricsURLs([]string{dynamicMetricsURL})
	if os.Getenv("DYNAMIC_METRICS_URLS") != "" {
		dynamicMetricsURLs = normalizeDynamicMetricsURLs(env.CSV("DYNAMIC_METRICS_URLS", ""))
	}
	upstream := strings.TrimRight(env.String("UPSTREAM", "http://vllm:8000"), "/")
	backendRouting := strings.TrimSpace(os.Getenv("BACKENDS")) != "" || strings.TrimSpace(os.Getenv("UPSTREAMS")) != ""
	backends := parseBackends(env.String("BACKENDS", ""), upstream, dynamicMetricsURLs)
	if backendRouting {
		if backendURLs := backendMetricsURLs(backends); len(backendURLs) > 0 {
			dynamicMetricsURLs = backendURLs
		}
	}
	if len(dynamicMetricsURLs) == 0 {
		if backendURLs := backendMetricsURLs(backends); len(backendURLs) > 0 {
			dynamicMetricsURLs = backendURLs
		}
	}
	if backendRouting && len(backends) == 1 {
		backendRouting = false
	}
	if len(dynamicMetricsURLs) > 0 {
		dynamicMetricsURL = dynamicMetricsURLs[0]
	}
	cfg.Upstream = upstream
	cfg.Backends = backends
	cfg.BackendRouting = backendRouting
	cfg.DynamicMetricsURL = dynamicMetricsURL
	cfg.DynamicMetricsURLs = dynamicMetricsURLs
}
