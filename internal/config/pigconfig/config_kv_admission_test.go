package pigconfig

import (
	"math"
	"strings"
	"testing"
)

func TestValidateRejectsKVAdmissionEnforceInV090(t *testing.T) {
	t.Setenv("KV_ADMISSION_MODE", "enforce")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "enforce is not supported") {
		t.Fatalf("Validate error=%v want explicit enforce rejection", err)
	}
}

func TestValidateRejectsNonFiniteKVAdmissionRatio(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.KVAdmissionPolicy.VLLM.TargetRatio = math.NaN()
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "ratios must be finite") {
		t.Fatalf("Validate error=%v want non-finite ratio rejection", err)
	}
}

func TestValidateKVAdmissionShadowRequiresDynamicMetrics(t *testing.T) {
	t.Setenv("KV_ADMISSION_MODE", "shadow")
	t.Setenv("DYNAMIC_PIG_ENABLED", "false")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "DYNAMIC_PIG_ENABLED") {
		t.Fatalf("Validate error=%v want dynamic requirement", err)
	}
}

func TestLoadKVAdmissionDefaultsAreShadowSafe(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.KVAdmissionMode != "off" {
		t.Fatalf("mode=%q want off", cfg.KVAdmissionMode)
	}
	if cfg.KVAdmissionPolicy.VLLM.HardRatio != 0.88 || cfg.KVAdmissionPolicy.SGLang.HardRatio != 0.84 {
		t.Fatalf("unexpected default policy: %#v", cfg.KVAdmissionPolicy)
	}
	if cfg.KVAdmissionEstimator.BlindOutputTokens != 256 {
		t.Fatalf("decode allowance=%d want 256", cfg.KVAdmissionEstimator.BlindOutputTokens)
	}
}
