package pigconfig

import (
	"fmt"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

const (
	predictiveMaximumStartupProbeTimeout = 5 * time.Minute
	predictiveMaximumMetricsRequestTime  = time.Minute
	defaultPredictiveScannerBodyBytes    = 4 * 1024 * 1024
	defaultPredictiveScannerConcurrency  = 64
)

func loadPredictiveAdmissionConfig(cfg *Config) error {
	cfg.PredictiveAdmissionMode = strings.ToLower(strings.TrimSpace(env.String("PREDICTIVE_ADMISSION_MODE", "enforce")))
	metricsURL := strings.TrimRight(strings.TrimSpace(env.String("PREDICTIVE_METRICS_URL", cfg.PredictiveMetricsURL)), "/")
	startupTimeoutMS, err := env.Int("PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS", 10_000)
	if err != nil {
		return err
	}
	requestTimeoutMS, err := env.Int("PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS", 500)
	if err != nil {
		return err
	}
	pollIntervalMS, err := env.Int("PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS", 500)
	if err != nil {
		return err
	}
	maximumAgeMS, err := env.Int("PREDICTIVE_MAX_METRICS_AGE_MS", 3*pollIntervalMS)
	if err != nil {
		return err
	}
	kvHardRatio, err := env.Float("PREDICTIVE_KV_HARD_RATIO", 0.88)
	if err != nil {
		return err
	}
	prefillRegular, err := env.Int("PREDICTIVE_PREFILL_REGULAR_TOKENS", 0)
	if err != nil {
		return err
	}
	prefillExclusive, err := env.Int("PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS", 0)
	if err != nil {
		return err
	}
	prefillQuiescent, err := env.Int("PREDICTIVE_PREFILL_QUIESCENT_TOKENS", 0)
	if err != nil {
		return err
	}
	prefillAggregate, err := env.Int("PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS", 0)
	if err != nil {
		return err
	}

	integerBounds := []struct {
		name    string
		value   int
		minimum int
		maximum int
	}{
		{"PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS", startupTimeoutMS, 1, int(predictiveMaximumStartupProbeTimeout / time.Millisecond)},
		{"PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS", requestTimeoutMS, 1, int(predictiveMaximumMetricsRequestTime / time.Millisecond)},
		{"PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS", pollIntervalMS, 1, int(predictiveMaximumMetricsRequestTime / time.Millisecond)},
		{"PREDICTIVE_MAX_METRICS_AGE_MS", maximumAgeMS, 1, int(predictiveMaximumMetricsRequestTime / time.Millisecond)},
	}
	for _, bound := range integerBounds {
		if bound.value < bound.minimum || bound.value > bound.maximum {
			return fmt.Errorf("%s must be in [%d, %d]", bound.name, bound.minimum, bound.maximum)
		}
	}
	if requestTimeoutMS > startupTimeoutMS {
		return fmt.Errorf("PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS must not exceed PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS")
	}
	prefillValues := []int{prefillRegular, prefillExclusive, prefillQuiescent, prefillAggregate}
	configured := 0
	for _, value := range prefillValues {
		if value < 0 {
			return fmt.Errorf("predictive Prefill overrides must be non-negative")
		}
		if value > 0 {
			configured++
		}
	}
	if configured != 0 && configured != len(prefillValues) {
		return fmt.Errorf("predictive Prefill overrides must all be set or all be omitted")
	}

	cfg.PredictiveMetricsURL = metricsURL
	cfg.PredictiveScannerBodyBytes = defaultPredictiveScannerBodyBytes
	cfg.PredictiveScannerConcurrency = defaultPredictiveScannerConcurrency
	cfg.PredictiveStartupProbeTimeout = time.Duration(startupTimeoutMS) * time.Millisecond
	cfg.PredictiveMetricsRequestTimeout = time.Duration(requestTimeoutMS) * time.Millisecond
	cfg.PredictiveObservationPollInterval = time.Duration(pollIntervalMS) * time.Millisecond
	cfg.PredictiveMaximumMetricsAge = time.Duration(maximumAgeMS) * time.Millisecond
	cfg.PredictiveKVHardRatio = kvHardRatio
	cfg.PredictivePrefillRegularTokens = int64(prefillRegular)
	cfg.PredictivePrefillExclusiveTokens = int64(prefillExclusive)
	cfg.PredictivePrefillQuiescentTokens = int64(prefillQuiescent)
	cfg.PredictivePrefillAggregateBudgetTokens = int64(prefillAggregate)
	return nil
}
