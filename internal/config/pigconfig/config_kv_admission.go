package pigconfig

import (
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

func loadKVAdmissionConfig(cfg *Config) error {
	policy := kvadmission.DefaultPolicy()
	estimator := kvadmission.DefaultEstimatorConfig()
	mode := strings.ToLower(strings.TrimSpace(env.String("KV_ADMISSION_MODE", "off")))

	var err error
	if policy.VLLM.TargetRatio, err = env.Float("KV_ADMISSION_VLLM_TARGET_RATIO", policy.VLLM.TargetRatio); err != nil {
		return err
	}
	if policy.VLLM.HardRatio, err = env.Float("KV_ADMISSION_VLLM_HARD_RATIO", policy.VLLM.HardRatio); err != nil {
		return err
	}
	if policy.VLLM.EmergencyRatio, err = env.Float("KV_ADMISSION_VLLM_EMERGENCY_RATIO", policy.VLLM.EmergencyRatio); err != nil {
		return err
	}
	if policy.SGLang.TargetRatio, err = env.Float("KV_ADMISSION_SGLANG_TARGET_RATIO", policy.SGLang.TargetRatio); err != nil {
		return err
	}
	if policy.SGLang.HardRatio, err = env.Float("KV_ADMISSION_SGLANG_HARD_RATIO", policy.SGLang.HardRatio); err != nil {
		return err
	}
	if policy.SGLang.EmergencyRatio, err = env.Float("KV_ADMISSION_SGLANG_EMERGENCY_RATIO", policy.SGLang.EmergencyRatio); err != nil {
		return err
	}

	maxAgeMs, err := env.Int("KV_ADMISSION_MAX_METRICS_AGE_MS", int((3*cfg.DynamicPollInterval)/time.Millisecond))
	if err != nil {
		return err
	}
	cooldownSeconds, err := env.Int("KV_ADMISSION_PREEMPTION_COOLDOWN_SECONDS", int(policy.PreemptionCooldown/time.Second))
	if err != nil {
		return err
	}
	decodeDriftTokens, err := env.Int("KV_ADMISSION_DECODE_DRIFT_TOKENS", int(policy.DecodeDriftTokens))
	if err != nil {
		return err
	}
	reservationTTLSeconds, err := env.Int("KV_ADMISSION_RESERVATION_TTL_SECONDS", int(policy.ReservationTTL/time.Second))
	if err != nil {
		return err
	}
	policy.MaxMetricsAge = time.Duration(maxAgeMs) * time.Millisecond
	policy.PreemptionCooldown = time.Duration(cooldownSeconds) * time.Second
	policy.DecodeDriftTokens = int64(decodeDriftTokens)
	policy.ReservationTTL = time.Duration(reservationTTLSeconds) * time.Second

	if estimator.MinBytesPerToken, err = env.Int("KV_ESTIMATOR_MIN_BYTES_PER_TOKEN", estimator.MinBytesPerToken); err != nil {
		return err
	}
	if estimator.MaxBytesPerToken, err = env.Int("KV_ESTIMATOR_MAX_BYTES_PER_TOKEN", estimator.MaxBytesPerToken); err != nil {
		return err
	}
	if estimator.ToolMinBytesPerToken, err = env.Int("KV_ESTIMATOR_TOOL_MIN_BYTES_PER_TOKEN", estimator.ToolMinBytesPerToken); err != nil {
		return err
	}
	if estimator.ToolMaxBytesPerToken, err = env.Int("KV_ESTIMATOR_TOOL_MAX_BYTES_PER_TOKEN", estimator.ToolMaxBytesPerToken); err != nil {
		return err
	}
	if estimator.TemplateTokensPerMessageLow, err = env.Int("KV_ESTIMATOR_TEMPLATE_TOKENS_PER_MESSAGE_LOW", estimator.TemplateTokensPerMessageLow); err != nil {
		return err
	}
	if estimator.TemplateTokensPerMessageHigh, err = env.Int("KV_ESTIMATOR_TEMPLATE_TOKENS_PER_MESSAGE_HIGH", estimator.TemplateTokensPerMessageHigh); err != nil {
		return err
	}
	if estimator.ModalityTokensLow, err = env.Int("KV_ESTIMATOR_MODALITY_TOKENS_LOW", estimator.ModalityTokensLow); err != nil {
		return err
	}
	if estimator.ModalityTokensHigh, err = env.Int("KV_ESTIMATOR_MODALITY_TOKENS_HIGH", estimator.ModalityTokensHigh); err != nil {
		return err
	}
	if estimator.BlindOutputTokens, err = env.Int("KV_ADMISSION_NEW_REQUEST_DECODE_TOKENS", estimator.BlindOutputTokens); err != nil {
		return err
	}

	cfg.KVAdmissionMode = mode
	cfg.KVAdmissionPolicy = policy
	cfg.KVAdmissionEstimator = estimator
	return nil
}
