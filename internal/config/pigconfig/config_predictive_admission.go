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
	defaultPredictiveWindowConcurrency   = 32
	maximumPredictiveSequenceBound       = 1 << 20
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
	tpsReference, err := env.Float("PREDICTIVE_TPS_REFERENCE", 0)
	if err != nil {
		return err
	}
	windowConcurrency, err := env.Int("PREDICTIVE_WINDOW_CONCURRENCY", defaultPredictiveWindowConcurrency)
	if err != nil {
		return err
	}
	runningLimit, err := env.Int("PREDICTIVE_RUNNING_LIMIT", 0)
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
		{"PREDICTIVE_WINDOW_CONCURRENCY", windowConcurrency, 1, maximumPredictiveSequenceBound},
		{"PREDICTIVE_RUNNING_LIMIT", runningLimit, 0, maximumPredictiveSequenceBound},
	}
	for _, bound := range integerBounds {
		if bound.value < bound.minimum || bound.value > bound.maximum {
			return fmt.Errorf("%s must be in [%d, %d]", bound.name, bound.minimum, bound.maximum)
		}
	}
	if requestTimeoutMS > startupTimeoutMS {
		return fmt.Errorf("PREDICTIVE_METRICS_REQUEST_TIMEOUT_MS must not exceed PREDICTIVE_STARTUP_PROBE_TIMEOUT_MS")
	}
	cfg.PredictiveMetricsURL = metricsURL
	cfg.PredictiveScannerBodyBytes = defaultPredictiveScannerBodyBytes
	cfg.PredictiveScannerConcurrency = defaultPredictiveScannerConcurrency
	cfg.PredictiveStartupProbeTimeout = time.Duration(startupTimeoutMS) * time.Millisecond
	cfg.PredictiveMetricsRequestTimeout = time.Duration(requestTimeoutMS) * time.Millisecond
	cfg.PredictiveObservationPollInterval = time.Duration(pollIntervalMS) * time.Millisecond
	cfg.PredictiveMaximumMetricsAge = time.Duration(maximumAgeMS) * time.Millisecond
	cfg.PredictiveTPSReference = tpsReference
	cfg.PredictiveWindowConcurrency = int64(windowConcurrency)
	cfg.PredictiveRunningLimit = int64(runningLimit)
	return nil
}
