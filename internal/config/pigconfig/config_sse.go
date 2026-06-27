package pigconfig

import "github.com/Phala-Network/phala-inference-guard/internal/infra/env"

func loadSSEConfig(cfg *Config) error {
	sseKeepAliveEnabled, err := env.Bool("SSE_KEEPALIVE_ENABLED", false)
	if err != nil {
		return err
	}
	sseEarlyBridgeEnabled, err := env.Bool("SSE_EARLY_BRIDGE_ENABLED", false)
	if err != nil {
		return err
	}
	cfg.SSEKeepAliveEnabled = sseKeepAliveEnabled
	cfg.SSEEarlyBridgeEnabled = sseEarlyBridgeEnabled
	return nil
}
