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
	return nil
}
