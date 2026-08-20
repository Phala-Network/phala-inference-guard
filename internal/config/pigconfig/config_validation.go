package pigconfig

import (
	"fmt"
	"math"
	"net/url"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	"github.com/Phala-Network/phala-inference-guard/internal/support/names"
)

func Validate(cfg Config) error {
	if cfg.Listen == "" {
		return fmt.Errorf("LISTEN must not be empty")
	}
	if err := validateHTTPURL("UPSTREAM", cfg.Upstream); err != nil {
		return err
	}
	if err := validateHTTPURL("PREDICTIVE_METRICS_URL", cfg.PredictiveMetricsURL); err != nil {
		return err
	}
	if len(cfg.QoSPaths) == 0 {
		return fmt.Errorf("PIG_PATHS must not be empty")
	}
	if err := validatePaths("PIG_PATHS", cfg.QoSPaths); err != nil {
		return err
	}
	if cfg.APIAuthEnabled && cfg.Token == "" {
		return fmt.Errorf("API_AUTH_ENABLED requires TOKEN")
	}
	if cfg.APIAuthEnabled && len(cfg.APIAuthPaths) == 0 {
		return fmt.Errorf("API_AUTH_PATHS must not be empty when API_AUTH_ENABLED=true")
	}
	if err := validatePaths("API_AUTH_PATHS", cfg.APIAuthPaths); err != nil {
		return err
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

func validatePredictiveAdmissionConfig(cfg Config) error {
	if cfg.PredictiveAdmissionMode != "shadow" && cfg.PredictiveAdmissionMode != "enforce" {
		return fmt.Errorf("PREDICTIVE_ADMISSION_MODE must be shadow or enforce")
	}
	if cfg.PredictiveScannerBodyBytes <= 0 || cfg.PredictiveScannerConcurrency <= 0 {
		return fmt.Errorf("predictive scanner bounds must be positive")
	}
	if len(cfg.OutputTokenFields) == 0 {
		return fmt.Errorf("OUTPUT_TOKEN_FIELD_NAMES must not be empty")
	}
	for _, field := range cfg.OutputTokenFields {
		if !names.OutputTokenField(field) {
			return fmt.Errorf("invalid output token field %q", field)
		}
	}
	if err := validateUniqueStrings("OUTPUT_TOKEN_FIELD_NAMES", cfg.OutputTokenFields); err != nil {
		return err
	}
	if err := validateEstimator(cfg.PredictiveEstimator); err != nil {
		return err
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
	if !finite(cfg.PredictiveKVHardRatio) || cfg.PredictiveKVHardRatio <= 0 || cfg.PredictiveKVHardRatio >= 1 {
		return fmt.Errorf("PREDICTIVE_KV_HARD_RATIO must be between 0 and 1")
	}
	if !finite(cfg.PredictiveTPSReference) || cfg.PredictiveTPSReference < 0 || cfg.PredictiveTPSReference > 1_000_000 {
		return fmt.Errorf("PREDICTIVE_TPS_REFERENCE must be finite and in [0, 1000000]")
	}
	regular, exclusive, quiescent, aggregate := predictivePrefillBounds(cfg)
	automatic := cfg.PredictiveMaxModelLenTokens == 0 && regular == 0 && exclusive == 0 && quiescent == 0 && aggregate == 0
	if !automatic && (cfg.PredictiveMaxModelLenTokens <= 0 || regular <= 0 || exclusive <= regular ||
		quiescent <= exclusive || aggregate < regular || aggregate > quiescent) {
		return fmt.Errorf("predictive capability overrides must all be omitted or include max model length and ordered Prefill bounds/budget")
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

func validatePaths(name string, paths []string) error {
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if !names.QoSPath(path) {
			return fmt.Errorf("invalid %s path %q", name, path)
		}
		if _, exists := seen[path]; exists {
			return fmt.Errorf("%s contains duplicate value %q", name, path)
		}
		seen[path] = struct{}{}
	}
	return nil
}

func validateUniqueStrings(label string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate value %q", label, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func validateEstimator(cfg kvadmission.EstimatorConfig) error {
	if cfg.MinBytesPerToken <= 0 || cfg.MaxBytesPerToken < cfg.MinBytesPerToken ||
		cfg.ToolMinBytesPerToken <= 0 || cfg.ToolMaxBytesPerToken < cfg.ToolMinBytesPerToken ||
		cfg.TemplateTokensPerMessageLow < 0 || cfg.TemplateTokensPerMessageHigh < cfg.TemplateTokensPerMessageLow ||
		cfg.ModalityTokensLow < 0 || cfg.ModalityTokensHigh < cfg.ModalityTokensLow || cfg.BlindOutputTokens < 0 {
		return fmt.Errorf("predictive estimator bounds are invalid")
	}
	return nil
}

func predictivePrefillBounds(cfg Config) (int64, int64, int64, int64) {
	return cfg.PredictivePrefillRegularTokens, cfg.PredictivePrefillExclusiveTokens,
		cfg.PredictivePrefillQuiescentTokens, cfg.PredictivePrefillAggregateBudgetTokens
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
