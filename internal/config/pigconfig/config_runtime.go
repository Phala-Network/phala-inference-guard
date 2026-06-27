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
	qosQueueWaitSeconds, err := env.Float("PIG_QUEUE_WAIT_SECONDS", 0.5)
	if err != nil {
		return err
	}
	qosQueuePollMs, err := env.Int("PIG_QUEUE_POLL_MS", 100)
	if err != nil {
		return err
	}
	cfg.ProxyTimeout = time.Duration(proxyTimeoutSeconds) * time.Second
	cfg.StatusLogInterval = time.Duration(statusLogIntervalSeconds) * time.Second
	cfg.QoSQueueWait = time.Duration(qosQueueWaitSeconds * float64(time.Second))
	cfg.QoSQueuePoll = time.Duration(qosQueuePollMs) * time.Millisecond
	return nil
}
