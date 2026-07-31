package pigconfig

import (
	"strings"
	"testing"
)

func TestLoadPredictiveAdmissionDefaultsOff(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PredictiveAdmissionMode != "off" {
		t.Fatalf("predictive mode = %q, want off", cfg.PredictiveAdmissionMode)
	}
}

func TestValidateRejectsPredictiveAdmissionEnforce(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "enforce")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "off or shadow") {
		t.Fatalf("Validate error = %v, want explicit off/shadow-only rejection", err)
	}
}

func TestValidatePredictiveShadowRequiresBoundedJSONBody(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "shadow")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.JSONClassifyBodyBytes = 0
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "JSON_CLASSIFY_BODY_BYTES") {
		t.Fatalf("Validate error = %v, want bounded JSON body requirement", err)
	}
}
