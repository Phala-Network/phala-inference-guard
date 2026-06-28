package pigconfig

import (
	"fmt"

	"github.com/Phala-Network/phala-inference-guard/internal/support/names"
)

func validateOpenAIConfig(cfg Config) error {
	if cfg.APIAuthEnabled && cfg.Token == "" {
		return fmt.Errorf("API_AUTH_ENABLED requires TOKEN")
	}
	if cfg.APIAuthEnabled && len(cfg.APIAuthPaths) == 0 {
		return fmt.Errorf("API_AUTH_PATHS must not be empty when API_AUTH_ENABLED=true")
	}
	for _, path := range cfg.APIAuthPaths {
		if !names.QoSPath(path) {
			return fmt.Errorf("invalid api auth path %q: must start with / and contain only A-Z, a-z, 0-9, _, ., -, or /", path)
		}
	}
	if err := validateUniqueStrings("API_AUTH_PATHS", cfg.APIAuthPaths); err != nil {
		return err
	}
	if cfg.OpenAICompatStripEmptyToolCalls && cfg.OpenAICompatBodyBytes <= 0 {
		return fmt.Errorf("OPENAI_COMPAT_BODY_BYTES must be > 0 when OPENAI_COMPAT_STRIP_EMPTY_TOOL_CALLS=true")
	}
	if cfg.AttestationEnabled && cfg.AttestationNVIDIACommandTimeout <= 0 {
		return fmt.Errorf("ATTESTATION_NVIDIA_COMMAND_TIMEOUT_SECONDS must be > 0 when ATTESTATION_ENABLED=true")
	}
	return nil
}
