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
		if cfg.PredictiveLearningMinimumSamples <= 0 || cfg.PredictiveLearningMinimumSamples > predictiveMaximumLearningSamples ||
			cfg.PredictiveLearningMaximumSamples < cfg.PredictiveLearningMinimumSamples || cfg.PredictiveLearningMaximumSamples > predictiveMaximumLearningSamples ||
			cfg.PredictiveLearningMaximumCells <= 0 || cfg.PredictiveLearningMaximumCells > predictiveMaximumLearningCells ||
			cfg.PredictiveLearningMaxAge <= 0 || cfg.PredictiveLearningMaxAge > predictiveMaximumLearningAge {
			return fmt.Errorf("predictive learning sample, cell, or age bounds are invalid")
		}
		if cfg.PredictiveShadowObservationLimit <= 0 || cfg.PredictiveShadowObservationLimit > predictiveMaximumShadowObservations {
			return fmt.Errorf("predictive shadow observation bound must be in [1, 4096]")
		}
		if !validPredictiveUnitInterval(cfg.PredictiveMinimumConfidence) || !validPredictiveUnitInterval(cfg.PredictiveColdConfidence) || !validPredictiveUnitInterval(cfg.PredictiveLearnedConfidence) || cfg.PredictiveColdConfidence < cfg.PredictiveMinimumConfidence || cfg.PredictiveLearnedConfidence < cfg.PredictiveColdConfidence {
			return fmt.Errorf("predictive confidence bounds are invalid")
		}
		if !validPredictiveUnitInterval(cfg.PredictiveInputUpperQuantile) || !finiteFloat(cfg.PredictiveInputSafetyMargin) || cfg.PredictiveInputSafetyMargin < 1 || !finiteFloat(cfg.PredictiveInputMinimumMultiplier) || cfg.PredictiveInputMinimumMultiplier <= 0 || cfg.PredictiveInputMinimumMultiplier > 1 || !finiteFloat(cfg.PredictiveInputMaximumMultiplier) || cfg.PredictiveInputMaximumMultiplier < 1 || cfg.PredictiveInputMaximumMultiplier < cfg.PredictiveInputMinimumMultiplier {
			return fmt.Errorf("predictive input-size calibration bounds are invalid")
		}
		if !finiteFloat(cfg.PredictiveTPSMinimumMultiplier) || cfg.PredictiveTPSMinimumMultiplier <= 0 || cfg.PredictiveTPSMinimumMultiplier > 1 || !finiteFloat(cfg.PredictiveTPSMaximumMultiplier) || cfg.PredictiveTPSMaximumMultiplier < 1 || cfg.PredictiveTPSMaximumMultiplier < cfg.PredictiveTPSMinimumMultiplier {
			return fmt.Errorf("predictive TPS calibration bounds are invalid")
		}
		if !finiteFloat(cfg.PredictiveLatencyMinimumMultiplier) || cfg.PredictiveLatencyMinimumMultiplier <= 0 || cfg.PredictiveLatencyMinimumMultiplier > 1 || !finiteFloat(cfg.PredictiveLatencyMaximumMultiplier) || cfg.PredictiveLatencyMaximumMultiplier < 1 || cfg.PredictiveLatencyMaximumMultiplier < cfg.PredictiveLatencyMinimumMultiplier {
			return fmt.Errorf("predictive latency calibration bounds are invalid")
		}
		if !finiteFloat(cfg.DynamicUserTPSRed) || cfg.DynamicUserTPSRed <= 0 {
			return fmt.Errorf("DYNAMIC_SINGLE_USER_TPS_RED must be > 0 when predictive admission is enabled")
		}
		if !finiteFloat(cfg.DynamicTTFTPolicy.TargetSeconds) || cfg.DynamicTTFTPolicy.TargetSeconds <= 0 {
			return fmt.Errorf("DYNAMIC_TTFT_TARGET_SECONDS must be > 0 when predictive admission is enabled")
		}
	}
	return nil
}

func validPredictiveUnitInterval(value float64) bool {
	return finiteFloat(value) && value > 0 && value <= 1
}
