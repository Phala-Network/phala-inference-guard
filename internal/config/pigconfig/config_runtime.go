package pigconfig

import (
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

func loadRuntimeConfig(cfg *Config) error {
	proxyTimeoutSeconds, err := env.Int("PROXY_TIMEOUT_SECONDS", 1800)
	if err != nil {
		return err
	}
	statusLogIntervalSeconds, err := env.Int("PIG_STATUS_LOG_INTERVAL_SECONDS", 30)
	if err != nil {
		return err
	}
	cfg.ProxyTimeout = time.Duration(proxyTimeoutSeconds) * time.Second
	cfg.StatusLogInterval = time.Duration(statusLogIntervalSeconds) * time.Second
	cfg.LogLevel = strings.ToLower(strings.TrimSpace(env.String("PIG_LOG_LEVEL", "info")))
	return nil
}
