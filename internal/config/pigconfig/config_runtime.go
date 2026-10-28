package pigconfig

import (
	"fmt"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

func loadRuntimeConfig(cfg *Config) error {
	for _, setting := range []struct {
		name     string
		fallback int
		target   *time.Duration
	}{
		{"HTTP_BODY_READ_TIMEOUT_SECONDS", 120, &cfg.HTTPBodyReadTimeout},
		{"HTTP_WRITE_IDLE_TIMEOUT_SECONDS", 120, &cfg.HTTPWriteIdleTimeout},
		{"HTTP_IDLE_TIMEOUT_SECONDS", 60, &cfg.HTTPIdleTimeout},
	} {
		seconds, err := env.Int(setting.name, setting.fallback)
		if err != nil {
			return err
		}
		// Validate before duration conversion so very large integers cannot wrap.
		if seconds < 1 || seconds > 86400 {
			return fmt.Errorf("%s must be in [1, 86400]", setting.name)
		}
		*setting.target = time.Duration(seconds) * time.Second
	}
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
