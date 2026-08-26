package pigconfig

import (
	"net/url"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

func Load() (Config, error) {
	upstream := strings.TrimRight(strings.TrimSpace(env.String("UPSTREAM", "http://backend:8000")), "/")
	cfg := Config{
		Listen:               env.String("LISTEN", ":8000"),
		Upstream:             upstream,
		PredictiveMetricsURL: deriveMetricsURL(upstream),
		Token:                env.String("TOKEN", ""),
	}
	if err := loadOpenAIConfig(&cfg); err != nil {
		return Config{}, err
	}
	if err := loadRuntimeConfig(&cfg); err != nil {
		return Config{}, err
	}
	if err := loadPredictiveAdmissionConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func deriveMetricsURL(upstream string) string {
	parsed, err := url.Parse(upstream)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.Path = "/metrics"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/")
}
