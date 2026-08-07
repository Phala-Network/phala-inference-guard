package pigconfig

import "fmt"

func validatePredictiveAdmissionConfig(cfg Config) error {
	if cfg.PredictiveAdmissionMode == "" {
		return nil
	}
	if cfg.PredictiveAdmissionMode != "off" && cfg.PredictiveAdmissionMode != "shadow" && cfg.PredictiveAdmissionMode != "enforce" {
		return fmt.Errorf("PREDICTIVE_ADMISSION_MODE must be one of off, shadow, or enforce")
	}
	if cfg.PredictiveAdmissionMode != "off" && cfg.JSONClassifyBodyBytes <= 0 {
		return fmt.Errorf("JSON_CLASSIFY_BODY_BYTES must be > 0 when predictive admission is enabled")
	}
	if cfg.PredictiveAdmissionMode != "off" && cfg.JSONClassifyLimit <= 0 {
		return fmt.Errorf("JSON_CLASSIFY_LIMIT must be > 0 when predictive admission is enabled")
	}
	if cfg.PredictiveAdmissionMode != "off" {
		if cfg.DynamicTTFTEnabled {
			return fmt.Errorf("DYNAMIC_TTFT_ENABLED must be false when predictive admission is enabled; TTFT is observation-only")
		}
		if err := validateKVEstimator(cfg.KVAdmissionEstimator); err != nil {
			return fmt.Errorf("predictive request-size estimator: %w", err)
		}
		if err := validateKVAdmissionBudget("VLLM", cfg.KVAdmissionPolicy.VLLM); err != nil {
			return fmt.Errorf("predictive protected KV budget: %w", err)
		}
		if cfg.PredictiveStartupProbeTimeout <= 0 || cfg.PredictiveStartupProbeTimeout > predictiveMaximumStartupProbeTimeout ||
			cfg.PredictiveMetricsRequestTimeout <= 0 || cfg.PredictiveMetricsRequestTimeout > predictiveMaximumMetricsRequestTime ||
			cfg.PredictiveMetricsRequestTimeout > cfg.PredictiveStartupProbeTimeout {
			return fmt.Errorf("predictive startup and metrics request timeouts are invalid")
		}
		if cfg.PredictiveObservationPollInterval <= 0 || cfg.PredictiveObservationPollInterval > predictiveMaximumMetricsRequestTime ||
			cfg.PredictiveMaximumMetricsAge < cfg.PredictiveObservationPollInterval || cfg.PredictiveMaximumMetricsAge > predictiveMaximumMetricsRequestTime {
			return fmt.Errorf("PREDICTIVE_MAX_METRICS_AGE must be >= PREDICTIVE_OBSERVATION_POLL_INTERVAL and both must be positive and bounded")
		}
		if !finiteFloat(cfg.PredictiveTPSTarget) || cfg.PredictiveTPSTarget <= 0 ||
			!finiteFloat(cfg.PredictiveTPSFloor) || cfg.PredictiveTPSFloor <= 0 ||
			cfg.PredictiveTPSFloor >= cfg.PredictiveTPSTarget {
			return fmt.Errorf("PREDICTIVE_TPS_FLOOR must be > 0 and < PREDICTIVE_TPS_TARGET")
		}
		regular, exclusive, quiescent, aggregate := predictivePrefillBounds(cfg)
		automatic := regular == 0 && exclusive == 0 && quiescent == 0 && aggregate == 0
		if !automatic && (regular <= 0 || exclusive <= regular || quiescent <= exclusive ||
			aggregate < exclusive || aggregate > quiescent) {
			return fmt.Errorf("predictive prefill tokens must all be omitted or satisfy 0 < regular < exclusive < quiescent and exclusive <= aggregate budget <= quiescent")
		}
	}
	return nil
}

func predictivePrefillBounds(cfg Config) (regular, exclusive, quiescent, aggregate int64) {
	regular = cfg.PredictivePrefillRegularTokens
	exclusive = cfg.PredictivePrefillExclusiveTokens
	quiescent = cfg.PredictivePrefillQuiescentTokens
	aggregate = cfg.PredictivePrefillAggregateBudgetTokens
	return regular, exclusive, quiescent, aggregate
}
