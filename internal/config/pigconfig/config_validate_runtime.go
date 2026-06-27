package pigconfig

import "fmt"

func validateRuntimeConfig(cfg Config) error {
	if cfg.ProxyTimeout <= 0 {
		return fmt.Errorf("PROXY_TIMEOUT_SECONDS must be > 0")
	}
	if cfg.StatusLogInterval < 0 {
		return fmt.Errorf("PIG_STATUS_LOG_INTERVAL_SECONDS must be >= 0")
	}
	if cfg.QoSQueueWait < 0 {
		return fmt.Errorf("PIG_QUEUE_WAIT_SECONDS must be >= 0")
	}
	if cfg.QoSQueueWait > 0 && cfg.QoSQueuePoll <= 0 {
		return fmt.Errorf("PIG_QUEUE_POLL_MS must be > 0 when PIG_QUEUE_WAIT_SECONDS > 0")
	}
	return nil
}
