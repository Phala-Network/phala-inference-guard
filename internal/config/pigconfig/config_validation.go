package pigconfig

import (
	"fmt"
	"math"
	"net/url"
	"strings"
)

func Validate(cfg Config) error {
	if cfg.Listen == "" {
		return fmt.Errorf("LISTEN must not be empty")
	}
	if err := validateHTTPURL("UPSTREAM", cfg.Upstream); err != nil {
		return err
	}
	metricsURLs := cfg.PredictiveMetricsURLs
	if len(metricsURLs) == 0 {
		metricsURLs = []string{cfg.PredictiveMetricsURL}
	}
	if len(metricsURLs) > maximumPredictiveMetricsURLs {
		return fmt.Errorf("PREDICTIVE_METRICS_URLS must contain at most %d endpoints", maximumPredictiveMetricsURLs)
	}
	if cfg.PredictiveMetricsURL != "" && strings.TrimRight(cfg.PredictiveMetricsURL, "/") != strings.TrimRight(metricsURLs[0], "/") {
		return fmt.Errorf("PREDICTIVE_METRICS_URL must match the first PREDICTIVE_METRICS_URLS endpoint")
	}
	for _, metricsURL := range metricsURLs {
		if err := validateHTTPURL("PREDICTIVE_METRICS_URLS", metricsURL); err != nil {
			return err
		}
	}
	if err := validatePredictiveMetricsURLUniqueness(metricsURLs); err != nil {
		return err
	}
	if cfg.APIAuthEnabled && cfg.Token == "" {
		return fmt.Errorf("API_AUTH_ENABLED requires TOKEN")
	}
	if cfg.ProxyTimeout <= 0 {
		return fmt.Errorf("PROXY_TIMEOUT_SECONDS must be > 0")
	}
	if cfg.StatusLogInterval < 0 {
		return fmt.Errorf("PIG_STATUS_LOG_INTERVAL_SECONDS must be >= 0")
	}
	if cfg.LogLevel != "info" && cfg.LogLevel != "debug" {
		return fmt.Errorf("PIG_LOG_LEVEL must be info or debug")
	}
	if cfg.AttestationEnabled && cfg.AttestationNVIDIACommandTimeout <= 0 {
		return fmt.Errorf("ATTESTATION_NVIDIA_COMMAND_TIMEOUT_SECONDS must be > 0 when ATTESTATION_ENABLED=true")
	}
	return validatePredictiveAdmissionConfig(cfg)
}

func parsePredictiveMetricsURLs(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	if len(parts) > maximumPredictiveMetricsURLs {
		return nil, fmt.Errorf("PREDICTIVE_METRICS_URLS must contain at most %d endpoints", maximumPredictiveMetricsURLs)
	}
	metricsURLs := make([]string, 0, len(parts))
	for _, part := range parts {
		metricsURL := strings.TrimRight(strings.TrimSpace(part), "/")
		if metricsURL == "" {
			return nil, fmt.Errorf("PREDICTIVE_METRICS_URLS must not contain empty endpoints")
		}
		if err := validateHTTPURL("PREDICTIVE_METRICS_URLS", metricsURL); err != nil {
			return nil, err
		}
		metricsURLs = append(metricsURLs, metricsURL)
	}
	if err := validatePredictiveMetricsURLUniqueness(metricsURLs); err != nil {
		return nil, err
	}
	return metricsURLs, nil
}

func validatePredictiveMetricsURLUniqueness(metricsURLs []string) error {
	seen := make(map[string]struct{}, len(metricsURLs))
	for _, metricsURL := range metricsURLs {
		parsed, err := url.Parse(metricsURL)
		if err != nil {
			return fmt.Errorf("PREDICTIVE_METRICS_URLS endpoint is invalid")
		}
		key := strings.ToLower(parsed.Scheme+"://"+parsed.Host) + parsed.EscapedPath()
		if _, exists := seen[key]; exists {
			return fmt.Errorf("PREDICTIVE_METRICS_URLS must not contain duplicate endpoints")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validatePredictiveAdmissionConfig(cfg Config) error {
	if cfg.PredictiveAdmissionMode != "shadow" && cfg.PredictiveAdmissionMode != "enforce" {
		return fmt.Errorf("PREDICTIVE_ADMISSION_MODE must be shadow or enforce")
	}
	if cfg.PredictiveScannerBodyBytes <= 0 || cfg.PredictiveScannerConcurrency <= 0 {
		return fmt.Errorf("predictive scanner bounds must be positive")
	}
	if cfg.PredictiveStartupProbeTimeout <= 0 || cfg.PredictiveStartupProbeTimeout > predictiveMaximumStartupProbeTimeout ||
		cfg.PredictiveMetricsRequestTimeout <= 0 || cfg.PredictiveMetricsRequestTimeout > predictiveMaximumMetricsRequestTime ||
		cfg.PredictiveMetricsRequestTimeout > cfg.PredictiveStartupProbeTimeout {
		return fmt.Errorf("predictive startup and metrics request timeouts are invalid")
	}
	if cfg.PredictiveObservationPollInterval <= 0 || cfg.PredictiveObservationPollInterval > predictiveMaximumMetricsRequestTime ||
		cfg.PredictiveMaximumMetricsAge < cfg.PredictiveObservationPollInterval || cfg.PredictiveMaximumMetricsAge > predictiveMaximumMetricsRequestTime {
		return fmt.Errorf("predictive metrics freshness bounds are invalid")
	}
	if !finite(cfg.PredictiveTPSReference) || cfg.PredictiveTPSReference < 0 || cfg.PredictiveTPSReference > 1_000_000 {
		return fmt.Errorf("PREDICTIVE_TPS_REFERENCE must be finite and in [0, 1000000]")
	}
	if cfg.PredictiveWindowConcurrency <= 0 || cfg.PredictiveWindowConcurrency > maximumPredictiveSequenceBound {
		return fmt.Errorf("PREDICTIVE_WINDOW_CONCURRENCY must be in [1, %d]", maximumPredictiveSequenceBound)
	}
	if cfg.PredictiveRunningLimit < 0 || cfg.PredictiveRunningLimit > maximumPredictiveSequenceBound {
		return fmt.Errorf("PREDICTIVE_RUNNING_LIMIT must be in [0, %d]", maximumPredictiveSequenceBound)
	}
	return nil
}

func validateHTTPURL(name, value string) error {
	if strings.Contains(value, ",") {
		return fmt.Errorf("%s must be exactly one absolute HTTP URL", name)
	}
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("%s must be one absolute HTTP URL without query or fragment", name)
	}
	return nil
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
