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
	// Canonical v0.11 observation settings do not consult legacy dynamic/KV or
	// removed learning names.
	observationPollIntervalMS, err := env.Int("PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS", 500)
	if err != nil {
		return err
	}
	maximumMetricsAgeMS, err := env.Int("PREDICTIVE_MAX_METRICS_AGE_MS", 3*observationPollIntervalMS)
	if err != nil {
		return err
	}
	predictiveTPSTarget, err := env.Float("PREDICTIVE_TPS_TARGET", 25)
	if err != nil {
		return err
	}
	predictiveTPSFloor, err := env.Float("PREDICTIVE_TPS_FLOOR", 20)
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
		{name: "PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS", value: observationPollIntervalMS, minimum: 1, maximum: int(predictiveMaximumMetricsRequestTime / time.Millisecond)},
		{name: "PREDICTIVE_MAX_METRICS_AGE_MS", value: maximumMetricsAgeMS, minimum: 1, maximum: int(predictiveMaximumMetricsRequestTime / time.Millisecond)},
	}
	for _, bound := range integerBounds {
		if bound.value < bound.minimum || bound.value > bound.maximum {
			return fmt.Errorf("%s must be in [%d, %d]", bound.name, bound.minimum, bound.maximum)
		}
	}
	if requestTimeoutMS > startupTimeoutMS {
		return fmt.Errorf("PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS must not exceed PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS")
	}
	cfg.PredictiveStartupProbeTimeout = time.Duration(startupTimeoutMS) * time.Millisecond
	cfg.PredictiveMetricsRequestTimeout = time.Duration(requestTimeoutMS) * time.Millisecond
	cfg.PredictiveObservationPollInterval = time.Duration(observationPollIntervalMS) * time.Millisecond
	cfg.PredictiveMaximumMetricsAge = time.Duration(maximumMetricsAgeMS) * time.Millisecond
	cfg.PredictiveTPSTarget = predictiveTPSTarget
	cfg.PredictiveTPSFloor = predictiveTPSFloor
	return nil
}
