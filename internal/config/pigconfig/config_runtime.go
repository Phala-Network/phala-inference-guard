package pigconfig

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

func loadRuntimeConfig(cfg *Config) error {
	proxyTimeoutSeconds, err := env.Int("PROXY_TIMEOUT_SECONDS", 1800)
	if err != nil {
		return err
	}
	statusLogIntervalSeconds, err := env.Int("PIG_STATUS_LOG_INTERVAL_SECONDS", 5)
	if err != nil {
		return err
	}
	cfg.ProxyTimeout = time.Duration(proxyTimeoutSeconds) * time.Second
	cfg.StatusLogInterval = time.Duration(statusLogIntervalSeconds) * time.Second
	return nil
}
