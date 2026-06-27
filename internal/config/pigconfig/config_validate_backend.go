package pigconfig

import (
	"fmt"
	"net/url"
)

func validateBackendConfig(cfg Config) error {
	if len(cfg.Backends) == 0 {
		return fmt.Errorf("at least one backend must be configured")
	}
	for index, backend := range cfg.Backends {
		name := backend.Name
		if name == "" {
			return fmt.Errorf("backend %d name must not be empty", index+1)
		}
		if backend.Upstream == "" {
			return fmt.Errorf("backend %q Upstream must not be empty", name)
		}
		upstreamURL, err := url.Parse(backend.Upstream)
		if err != nil {
			return fmt.Errorf("backend %q Upstream must be a valid URL: %w", name, err)
		}
		if upstreamURL.Scheme != "http" && upstreamURL.Scheme != "https" {
			return fmt.Errorf("backend %q Upstream must start with http:// or https://", name)
		}
		if upstreamURL.Host == "" {
			return fmt.Errorf("backend %q Upstream must include a host", name)
		}
		if upstreamURL.RawQuery != "" || upstreamURL.Fragment != "" {
			return fmt.Errorf("backend %q Upstream must not include query strings or fragments", name)
		}
	}
	return nil
}
