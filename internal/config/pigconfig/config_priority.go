package pigconfig

import (
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

const defaultBackendPriorityBodyBytes = 32 * 1024 * 1024
const defaultBackendPriorityStreamBufferBytes = 2 * 1024 * 1024

func loadPriorityConfig(cfg *Config) error {
	enabled, err := env.Bool("BACKEND_PRIORITY_INJECTION_ENABLED", true)
	if err != nil {
		return err
	}
	premiumValue, err := env.Int("BACKEND_PRIORITY_PREMIUM_VALUE", -100)
	if err != nil {
		return err
	}
	basicValue, err := env.Int("BACKEND_PRIORITY_BASIC_VALUE", 0)
	if err != nil {
		return err
	}
	bodyBytes, err := env.Int("BACKEND_PRIORITY_BODY_BYTES", defaultBackendPriorityBodyBytes)
	if err != nil {
		return err
	}
	bufferBytes, err := env.Int("BACKEND_PRIORITY_BUFFER_BYTES", 0)
	if err != nil {
		return err
	}
	streamBufferBytes, err := env.Int("BACKEND_PRIORITY_STREAM_BUFFER_BYTES", defaultBackendPriorityStreamBufferBytes)
	if err != nil {
		return err
	}
	limit, err := env.Int("BACKEND_PRIORITY_REWRITE_LIMIT", defaultClassifierLimit(cfg.GlobalLimit))
	if err != nil {
		return err
	}
	failOpen, err := env.Bool("BACKEND_PRIORITY_FAIL_OPEN", true)
	if err != nil {
		return err
	}

	cfg.BackendPriorityInjectionEnabled = enabled
	cfg.BackendPriorityMode = strings.ToLower(strings.TrimSpace(env.String("BACKEND_PRIORITY_MODE", "all")))
	cfg.BackendPriorityRewriteStrategy = strings.ToLower(strings.TrimSpace(env.String("BACKEND_PRIORITY_REWRITE_STRATEGY", "field_scan")))
	cfg.BackendPriorityField = strings.TrimSpace(env.String("BACKEND_PRIORITY_FIELD", "priority"))
	cfg.BackendPriorityPremiumValue = premiumValue
	cfg.BackendPriorityBasicValue = basicValue
	cfg.BackendPriorityBodyBytes = int64(bodyBytes)
	cfg.BackendPriorityBufferBytes = int64(bufferBytes)
	cfg.BackendPriorityStreamBufferBytes = streamBufferBytes
	cfg.BackendPriorityRewriteLimit = limit
	cfg.BackendPriorityFailOpen = failOpen
	return nil
}
