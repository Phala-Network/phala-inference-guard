package pigconfig

import "fmt"

func validatePredictiveAdmissionConfig(cfg Config) error {
	if cfg.PredictiveAdmissionMode == "" {
		return nil
	}
	if cfg.PredictiveAdmissionMode != "off" && cfg.PredictiveAdmissionMode != "shadow" {
		return fmt.Errorf("PREDICTIVE_ADMISSION_MODE must be one of off or shadow; enforce is not supported")
	}
	if cfg.PredictiveAdmissionMode == "shadow" && cfg.JSONClassifyBodyBytes <= 0 {
		return fmt.Errorf("JSON_CLASSIFY_BODY_BYTES must be > 0 when PREDICTIVE_ADMISSION_MODE=shadow")
	}
	return nil
}
