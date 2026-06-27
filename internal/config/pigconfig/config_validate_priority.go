package pigconfig

import (
	"fmt"

	requestclass "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
	"github.com/Phala-Network/phala-inference-guard/internal/support/names"
)

func validatePriorityConfig(cfg Config) error {
	if !cfg.BackendPriorityInjectionEnabled {
		return nil
	}
	switch cfg.BackendPriorityMode {
	case requestclass.PriorityModeAll, requestclass.PriorityModePremiumOnly:
	default:
		return fmt.Errorf("BACKEND_PRIORITY_MODE must be %q or %q", requestclass.PriorityModeAll, requestclass.PriorityModePremiumOnly)
	}
	switch cfg.BackendPriorityRewriteStrategy {
	case requestclass.PriorityRewriteStrategyFieldScan, requestclass.PriorityRewriteStrategyAppendLast:
	default:
		return fmt.Errorf("BACKEND_PRIORITY_REWRITE_STRATEGY must be %q or %q", requestclass.PriorityRewriteStrategyFieldScan, requestclass.PriorityRewriteStrategyAppendLast)
	}
	if !names.OutputTokenField(cfg.BackendPriorityField) {
		return fmt.Errorf("invalid BACKEND_PRIORITY_FIELD %q: must contain only A-Z, a-z, 0-9, or _ and must not start with a digit", cfg.BackendPriorityField)
	}
	if cfg.BackendPriorityBodyBytes <= 0 {
		return fmt.Errorf("BACKEND_PRIORITY_BODY_BYTES must be > 0 when BACKEND_PRIORITY_INJECTION_ENABLED=true")
	}
	if cfg.BackendPriorityBufferBytes < 0 {
		return fmt.Errorf("BACKEND_PRIORITY_BUFFER_BYTES must be >= 0 when BACKEND_PRIORITY_INJECTION_ENABLED=true")
	}
	if cfg.BackendPriorityStreamBufferBytes <= 0 {
		return fmt.Errorf("BACKEND_PRIORITY_STREAM_BUFFER_BYTES must be > 0 when BACKEND_PRIORITY_INJECTION_ENABLED=true")
	}
	if cfg.BackendPriorityRewriteLimit <= 0 {
		return fmt.Errorf("BACKEND_PRIORITY_REWRITE_LIMIT must be > 0 when BACKEND_PRIORITY_INJECTION_ENABLED=true")
	}
	return nil
}
