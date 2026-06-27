package pigconfig

import (
	"fmt"

	"github.com/Phala-Network/phala-inference-guard/internal/support/names"
)

func validateUniqueStrings(label string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s contains duplicate value %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateNonNegative(name string, value int) error {
	if value < 0 {
		return fmt.Errorf("%s must be >= 0", name)
	}
	return nil
}

func validatePositive(name string, value int64) error {
	if value <= 0 {
		return fmt.Errorf("%s must be > 0", name)
	}
	return nil
}

func validateRatio(name string, value float64) error {
	if value < 0 || value > 1 {
		return fmt.Errorf("%s must be between 0 and 1", name)
	}
	return nil
}

func defaultClassifierLimit(globalLimit int) int {
	if globalLimit > 0 && globalLimit < 64 {
		return globalLimit
	}
	return 64
}

func Validate(cfg Config) error {
	if err := validateCoreConfig(cfg); err != nil {
		return err
	}
	if err := validateOpenAIConfig(cfg); err != nil {
		return err
	}
	if err := validateBackendConfig(cfg); err != nil {
		return err
	}
	if err := validateClassifierConfig(cfg); err != nil {
		return err
	}
	if err := validatePriorityConfig(cfg); err != nil {
		return err
	}
	if err := validateRuntimeConfig(cfg); err != nil {
		return err
	}
	return validateDynamicConfig(cfg)
}

func validateCoreConfig(cfg Config) error {
	if cfg.Listen == "" {
		return fmt.Errorf("LISTEN must not be empty")
	}
	if cfg.Upstream == "" {
		return fmt.Errorf("UPSTREAM must not be empty")
	}
	if len(cfg.QoSPaths) == 0 {
		return fmt.Errorf("PIG_PATHS must not be empty")
	}
	for _, path := range cfg.QoSPaths {
		if !names.QoSPath(path) {
			return fmt.Errorf("invalid PIG path %q: must start with / and contain only A-Z, a-z, 0-9, _, ., -, or /", path)
		}
	}
	if err := validateUniqueStrings("PIG_PATHS", cfg.QoSPaths); err != nil {
		return err
	}
	return validateNonNegative("GLOBAL_LIMIT", cfg.GlobalLimit)
}
