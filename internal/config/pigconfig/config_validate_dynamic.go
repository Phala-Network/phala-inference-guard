package pigconfig

import (
	"fmt"
	"net/url"
	"os"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
)

func validateDynamicMetricsURL(envName, rawURL string) error {
	metricsURL, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%s must be a valid URL: %w", envName, err)
	}
	if metricsURL.Scheme != "http" && metricsURL.Scheme != "https" {
		return fmt.Errorf("%s must start with http:// or https://", envName)
	}
	if metricsURL.Host == "" {
		return fmt.Errorf("%s must include a host", envName)
	}
	if metricsURL.RawQuery != "" || metricsURL.Fragment != "" {
		return fmt.Errorf("%s must not include query strings or fragments", envName)
	}
	return nil
}

func validateDynamicConfig(cfg Config) error {
	if cfg.DynamicPollInterval <= 0 {
		return fmt.Errorf("DYNAMIC_POLL_INTERVAL_MS must be > 0")
	}
	if !decision.ValidState(cfg.DynamicFailsafeState) {
		return fmt.Errorf("DYNAMIC_FAILSAFE_STATE must be one of green, yellow, red")
	}
	if cfg.DynamicKVYellow < 0 || cfg.DynamicKVRed < 0 || cfg.DynamicKVYellow > cfg.DynamicKVRed {
		return fmt.Errorf("dynamic KV thresholds must be non-negative and increasing")
	}
	if cfg.DynamicRunningYellow < 0 || cfg.DynamicRunningRed < 0 || cfg.DynamicRunningYellow > cfg.DynamicRunningRed {
		return fmt.Errorf("dynamic running thresholds must be non-negative and increasing")
	}
	if cfg.DynamicWaitingYellow < 0 || cfg.DynamicWaitingRed < 0 || cfg.DynamicWaitingYellow > cfg.DynamicWaitingRed {
		return fmt.Errorf("dynamic waiting thresholds must be non-negative and increasing")
	}
	if cfg.DynamicPressureEnabled {
		if err := validateDynamicPressureConfig(cfg); err != nil {
			return err
		}
	}
	if cfg.DynamicUserTPSEnabled {
		if err := validateDynamicUserTPSConfig(cfg); err != nil {
			return err
		}
	}
	if cfg.DynamicTTFTEnabled {
		if err := validateDynamicTTFTConfig(cfg); err != nil {
			return err
		}
	}
	for name, value := range map[string]int{
		"DYNAMIC_GLOBAL_GREEN_LIMIT":  cfg.DynamicGlobalGreen,
		"DYNAMIC_GLOBAL_YELLOW_LIMIT": cfg.DynamicGlobalYellow,
		"DYNAMIC_GLOBAL_RED_LIMIT":    cfg.DynamicGlobalRed,
	} {
		if err := validateNonNegative(name, value); err != nil {
			return err
		}
	}
	if cfg.DynamicEnabled {
		return validateDynamicMetricsConfig(cfg)
	}
	return nil
}

func validateDynamicTTFTConfig(cfg Config) error {
	policy := cfg.DynamicTTFTPolicy.Normalize()
	if policy.TargetSeconds <= 0 || policy.RedSeconds <= 0 || policy.P99TargetSeconds <= 0 || policy.P99RedSeconds <= 0 {
		return fmt.Errorf("dynamic TTFT thresholds must be > 0 when DYNAMIC_TTFT_ENABLED=true")
	}
	if policy.RedSeconds < policy.TargetSeconds {
		return fmt.Errorf("DYNAMIC_TTFT_RED_SECONDS must be >= DYNAMIC_TTFT_TARGET_SECONDS")
	}
	if policy.P99RedSeconds < policy.P99TargetSeconds {
		return fmt.Errorf("DYNAMIC_TTFT_P99_RED_SECONDS must be >= DYNAMIC_TTFT_P99_TARGET_SECONDS")
	}
	return nil
}

func validateDynamicPressureConfig(cfg Config) error {
	if cfg.DynamicPressureHeadroom < 0 {
		return fmt.Errorf("DYNAMIC_PRESSURE_HEADROOM must be >= 0 when DYNAMIC_PRESSURE_LIMIT_ENABLED=true")
	}
	if cfg.DynamicPressureMinLimit < 0 {
		return fmt.Errorf("DYNAMIC_PRESSURE_MIN_LIMIT must be >= 0 when DYNAMIC_PRESSURE_LIMIT_ENABLED=true")
	}
	if cfg.DynamicPressureLearnRatio <= 0 || cfg.DynamicPressureLearnRatio > 1 {
		return fmt.Errorf("DYNAMIC_PRESSURE_LEARN_RATIO must be > 0 and <= 1 when DYNAMIC_PRESSURE_LIMIT_ENABLED=true")
	}
	if cfg.DynamicPressureLearnMinRunning < 0 {
		return fmt.Errorf("DYNAMIC_PRESSURE_LEARN_MIN_RUNNING must be >= 0 when DYNAMIC_PRESSURE_LIMIT_ENABLED=true")
	}
	return nil
}

func validateDynamicUserTPSConfig(cfg Config) error {
	if cfg.DynamicUserTPSYellow <= 0 || cfg.DynamicUserTPSRed <= 0 {
		return fmt.Errorf("dynamic single-user TPS thresholds must be > 0 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSRed > cfg.DynamicUserTPSYellow {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_RED must be <= DYNAMIC_SINGLE_USER_TPS_YELLOW")
	}
	if cfg.DynamicUserTPSMinRun <= 0 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_MIN_RUNNING must be > 0 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSYellowN <= 0 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_YELLOW_CONSECUTIVE must be > 0 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSRedN <= 0 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_RED_CONSECUTIVE must be > 0 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSGraceMin < 0 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_MIN_SECONDS must be >= 0 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSGraceMax < cfg.DynamicUserTPSGraceMin {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_MAX_SECONDS must be >= min grace when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSGraceBps <= 0 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_BODY_BYTES_PER_SECOND must be > 0 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSGraceMul <= 0 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_MULTIPLIER must be > 0 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSCapacityRatio <= 0 || cfg.DynamicUserTPSCapacityRatio > 1 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO must be > 0 and <= 1 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSCapacitySmoothing < 0 || cfg.DynamicUserTPSCapacitySmoothing >= 1 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_CAPACITY_SMOOTHING must be >= 0 and < 1 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSCapacityRatioMax < cfg.DynamicUserTPSCapacityRatio || cfg.DynamicUserTPSCapacityRatioMax > 1 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO_MAX must be >= DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO and <= 1 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSCapacityStepUp <= 0 || cfg.DynamicUserTPSCapacityStepUp > 1 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO_STEP_UP must be > 0 and <= 1 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSCapacityHealthyN <= 0 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_CAPACITY_HEALTHY_CONSECUTIVE must be > 0 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	if cfg.DynamicUserTPSCapacityHealthyMul < 1 {
		return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_CAPACITY_HEALTHY_MULTIPLIER must be >= 1 when DYNAMIC_SINGLE_USER_TPS_ENABLED=true")
	}
	return nil
}

func validateDynamicMetricsConfig(cfg Config) error {
	if len(cfg.DynamicMetricsURLs) == 0 {
		return fmt.Errorf("DYNAMIC_METRICS_URL or DYNAMIC_METRICS_URLS must not be empty when DYNAMIC_PIG_ENABLED=true")
	}
	envName := "DYNAMIC_METRICS_URL"
	if os.Getenv("DYNAMIC_METRICS_URLS") != "" {
		envName = "DYNAMIC_METRICS_URLS"
	}
	for _, metricsURL := range cfg.DynamicMetricsURLs {
		if err := validateDynamicMetricsURL(envName, metricsURL); err != nil {
			return err
		}
	}
	return nil
}
