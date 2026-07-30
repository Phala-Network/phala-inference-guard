package pigconfig

import (
	"fmt"
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
)

func validateKVAdmissionConfig(cfg Config) error {
	if cfg.KVAdmissionMode == "" {
		return nil
	}
	if cfg.KVAdmissionMode != "off" && cfg.KVAdmissionMode != "shadow" {
		return fmt.Errorf("KV_ADMISSION_MODE must be one of off or shadow; enforce is not supported in v0.9.0")
	}
	if err := validateKVAdmissionBudget("VLLM", cfg.KVAdmissionPolicy.VLLM); err != nil {
		return err
	}
	if err := validateKVAdmissionBudget("SGLANG", cfg.KVAdmissionPolicy.SGLang); err != nil {
		return err
	}
	if cfg.KVAdmissionPolicy.MaxMetricsAge <= 0 {
		return fmt.Errorf("KV_ADMISSION_MAX_METRICS_AGE_MS must be > 0")
	}
	if cfg.KVAdmissionPolicy.PreemptionCooldown < 0 {
		return fmt.Errorf("KV_ADMISSION_PREEMPTION_COOLDOWN_SECONDS must be >= 0")
	}
	if cfg.KVAdmissionPolicy.DecodeDriftTokens < 0 {
		return fmt.Errorf("KV_ADMISSION_DECODE_DRIFT_TOKENS must be >= 0")
	}
	if cfg.KVAdmissionPolicy.ReservationTTL <= 0 {
		return fmt.Errorf("KV_ADMISSION_RESERVATION_TTL_SECONDS must be > 0")
	}
	if err := validateKVEstimator(cfg.KVAdmissionEstimator); err != nil {
		return err
	}
	if cfg.KVAdmissionMode == "shadow" {
		if !cfg.DynamicEnabled {
			return fmt.Errorf("DYNAMIC_PIG_ENABLED must be true when KV_ADMISSION_MODE=shadow")
		}
		if cfg.JSONClassifyBodyBytes <= 0 {
			return fmt.Errorf("JSON_CLASSIFY_BODY_BYTES must be > 0 when KV_ADMISSION_MODE=shadow")
		}
	}
	return nil
}

func validateKVAdmissionBudget(name string, budget kvadmission.Budget) error {
	if !finiteFloat(budget.TargetRatio) || !finiteFloat(budget.HardRatio) || !finiteFloat(budget.EmergencyRatio) {
		return fmt.Errorf("KV_ADMISSION_%s ratios must be finite", name)
	}
	if budget.TargetRatio <= 0 || budget.TargetRatio > budget.HardRatio {
		return fmt.Errorf("KV_ADMISSION_%s_TARGET_RATIO must be > 0 and <= hard ratio", name)
	}
	if budget.HardRatio >= budget.EmergencyRatio {
		return fmt.Errorf("KV_ADMISSION_%s_HARD_RATIO must be < emergency ratio", name)
	}
	if budget.EmergencyRatio > 1 {
		return fmt.Errorf("KV_ADMISSION_%s_EMERGENCY_RATIO must be <= 1", name)
	}
	return nil
}

func finiteFloat(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateKVEstimator(cfg kvadmission.EstimatorConfig) error {
	if cfg.MinBytesPerToken <= 0 || cfg.MaxBytesPerToken < cfg.MinBytesPerToken {
		return fmt.Errorf("KV estimator text byte/token bounds are invalid")
	}
	if cfg.ToolMinBytesPerToken <= 0 || cfg.ToolMaxBytesPerToken < cfg.ToolMinBytesPerToken {
		return fmt.Errorf("KV estimator tool byte/token bounds are invalid")
	}
	if cfg.TemplateTokensPerMessageLow < 0 || cfg.TemplateTokensPerMessageHigh < cfg.TemplateTokensPerMessageLow {
		return fmt.Errorf("KV estimator template token bounds are invalid")
	}
	if cfg.ModalityTokensLow < 0 || cfg.ModalityTokensHigh < cfg.ModalityTokensLow {
		return fmt.Errorf("KV estimator modality token bounds are invalid")
	}
	if cfg.BlindOutputTokens < 0 {
		return fmt.Errorf("KV_ADMISSION_NEW_REQUEST_DECODE_TOKENS must be >= 0")
	}
	return nil
}
