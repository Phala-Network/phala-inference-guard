package pigconfig

import (
	"fmt"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

func normalizeDynamicMetricsURLs(rawURLs []string) []string {
	values := make([]string, 0, len(rawURLs))
	seen := map[string]struct{}{}
	for _, rawURL := range rawURLs {
		value := strings.TrimRight(strings.TrimSpace(rawURL), "/")
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func normalizeUpstreams(rawURLs []string) []string {
	values := make([]string, 0, len(rawURLs))
	seen := map[string]struct{}{}
	for _, rawURL := range rawURLs {
		value := strings.TrimRight(strings.TrimSpace(rawURL), "/")
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
	}
	return values
}

func parseBackends(rawSpec string, upstream string, dynamicMetricsURLs []string) []Backend {
	spec := strings.TrimSpace(rawSpec)
	if spec != "" {
		parts := env.CSVValue(spec)
		backends := make([]Backend, 0, len(parts))
		for index, part := range parts {
			name := fmt.Sprintf("backend%d", index+1)
			body := strings.TrimSpace(part)
			if eq := strings.Index(body, "="); eq >= 0 {
				name = strings.TrimSpace(body[:eq])
				body = strings.TrimSpace(body[eq+1:])
			}
			fields := strings.Split(body, "|")
			if len(fields) < 2 {
				backends = append(backends, Backend{Name: name, Upstream: strings.TrimRight(strings.TrimSpace(body), "/")})
				continue
			}
			backends = append(backends, Backend{
				Name:       name,
				Upstream:   strings.TrimRight(strings.TrimSpace(fields[0]), "/"),
				MetricsURL: strings.TrimRight(strings.TrimSpace(fields[1]), "/"),
			})
		}
		return backends
	}

	upstreams := normalizeUpstreams(env.CSV("UPSTREAMS", ""))
	if len(upstreams) == 0 && upstream != "" {
		upstreams = []string{strings.TrimRight(upstream, "/")}
	}
	backends := make([]Backend, 0, len(upstreams))
	for index, upstreamURL := range upstreams {
		metricsURL := ""
		if index < len(dynamicMetricsURLs) {
			metricsURL = dynamicMetricsURLs[index]
		}
		backends = append(backends, Backend{
			Name:       fmt.Sprintf("backend%d", index+1),
			Upstream:   upstreamURL,
			MetricsURL: metricsURL,
		})
	}
	return backends
}

func backendMetricsURLs(backends []Backend) []string {
	values := make([]string, 0, len(backends))
	for _, backend := range backends {
		if backend.MetricsURL != "" {
			values = append(values, backend.MetricsURL)
		}
	}
	return normalizeDynamicMetricsURLs(values)
}
