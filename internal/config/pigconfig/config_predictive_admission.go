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
	predictiveMaximumLearningSamples     = 256
	predictiveMaximumLearningCells       = 256
	predictiveMaximumShadowObservations  = 4_096
	predictiveMaximumLearningAge         = 24 * time.Hour
)

func loadPredictiveAdmissionConfig(cfg *Config) error {
	cfg.PredictiveAdmissionMode = strings.ToLower(strings.TrimSpace(env.String("PREDICTIVE_ADMISSION_MODE", "off")))
	startupTimeoutMS, err := env.Int("PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS", 10_000)
	if err != nil {
		return err
	}
	requestTimeoutMS, err := env.Int("PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS", 500)
	if err != nil {
		return err
	}
	minimumSamples, err := env.Int("PREDICTIVE_LEARNING_MINIMUM_SAMPLES", 3)
	if err != nil {
		return err
	}
	maximumSamples, err := env.Int("PREDICTIVE_LEARNING_MAXIMUM_SAMPLES", 64)
	if err != nil {
		return err
	}
	maximumCells, err := env.Int("PREDICTIVE_LEARNING_MAXIMUM_CELLS", 64)
	if err != nil {
		return err
	}
	maximumShadowObservations, err := env.Int("PREDICTIVE_SHADOW_MAXIMUM_OBSERVATIONS", 256)
	if err != nil {
		return err
	}
	maxAgeSeconds, err := env.Int("PREDICTIVE_LEARNING_MAX_AGE_SECONDS", 1_800)
	if err != nil {
		return err
	}
	integerBounds := []struct {
		name    string
		value   int
		minimum int
		maximum int
	}{
		{name: "PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS", value: startupTimeoutMS, minimum: 1, maximum: int(predictiveMaximumStartupProbeTimeout / time.Millisecond)},
		{name: "PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS", value: requestTimeoutMS, minimum: 1, maximum: int(predictiveMaximumMetricsRequestTime / time.Millisecond)},
		{name: "PREDICTIVE_LEARNING_MINIMUM_SAMPLES", value: minimumSamples, minimum: 1, maximum: predictiveMaximumLearningSamples},
		{name: "PREDICTIVE_LEARNING_MAXIMUM_SAMPLES", value: maximumSamples, minimum: 1, maximum: predictiveMaximumLearningSamples},
		{name: "PREDICTIVE_LEARNING_MAXIMUM_CELLS", value: maximumCells, minimum: 1, maximum: predictiveMaximumLearningCells},
		{name: "PREDICTIVE_SHADOW_MAXIMUM_OBSERVATIONS", value: maximumShadowObservations, minimum: 1, maximum: predictiveMaximumShadowObservations},
		{name: "PREDICTIVE_LEARNING_MAX_AGE_SECONDS", value: maxAgeSeconds, minimum: 1, maximum: int(predictiveMaximumLearningAge / time.Second)},
	}
	for _, bound := range integerBounds {
		if bound.value < bound.minimum || bound.value > bound.maximum {
			return fmt.Errorf("%s must be in [%d, %d]", bound.name, bound.minimum, bound.maximum)
		}
	}
	if requestTimeoutMS > startupTimeoutMS {
		return fmt.Errorf("PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS must not exceed PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS")
	}
	if maximumSamples < minimumSamples {
		return fmt.Errorf("PREDICTIVE_LEARNING_MAXIMUM_SAMPLES must not be smaller than PREDICTIVE_LEARNING_MINIMUM_SAMPLES")
	}
	if cfg.PredictiveMinimumConfidence, err = env.Float("PREDICTIVE_MINIMUM_CONFIDENCE", 0.90); err != nil {
		return err
	}
	if cfg.PredictiveColdConfidence, err = env.Float("PREDICTIVE_COLD_CONFIDENCE", 0.95); err != nil {
		return err
	}
	if cfg.PredictiveLearnedConfidence, err = env.Float("PREDICTIVE_LEARNED_CONFIDENCE", 0.99); err != nil {
		return err
	}
	if cfg.PredictiveInputUpperQuantile, err = env.Float("PREDICTIVE_INPUT_UPPER_QUANTILE", 0.95); err != nil {
		return err
	}
	if cfg.PredictiveInputSafetyMargin, err = env.Float("PREDICTIVE_INPUT_SAFETY_MARGIN", 1.10); err != nil {
		return err
	}
	if cfg.PredictiveInputMinimumMultiplier, err = env.Float("PREDICTIVE_INPUT_MINIMUM_MULTIPLIER", 0.25); err != nil {
		return err
	}
	if cfg.PredictiveInputMaximumMultiplier, err = env.Float("PREDICTIVE_INPUT_MAXIMUM_MULTIPLIER", 8.0); err != nil {
		return err
	}
	if cfg.PredictiveTPSMinimumMultiplier, err = env.Float("PREDICTIVE_TPS_MINIMUM_MULTIPLIER", 0.10); err != nil {
		return err
	}
	if cfg.PredictiveTPSMaximumMultiplier, err = env.Float("PREDICTIVE_TPS_MAXIMUM_MULTIPLIER", 8.0); err != nil {
		return err
	}
	if cfg.PredictiveLatencyMinimumMultiplier, err = env.Float("PREDICTIVE_LATENCY_MINIMUM_MULTIPLIER", 0.50); err != nil {
		return err
	}
	if cfg.PredictiveLatencyMaximumMultiplier, err = env.Float("PREDICTIVE_LATENCY_MAXIMUM_MULTIPLIER", 4.0); err != nil {
		return err
	}
	cfg.PredictiveStartupProbeTimeout = time.Duration(startupTimeoutMS) * time.Millisecond
	cfg.PredictiveMetricsRequestTimeout = time.Duration(requestTimeoutMS) * time.Millisecond
	cfg.PredictiveLearningMinimumSamples = minimumSamples
	cfg.PredictiveLearningMaximumSamples = maximumSamples
	cfg.PredictiveLearningMaximumCells = maximumCells
	cfg.PredictiveShadowObservationLimit = maximumShadowObservations
	cfg.PredictiveLearningMaxAge = time.Duration(maxAgeSeconds) * time.Second
	return nil
}
