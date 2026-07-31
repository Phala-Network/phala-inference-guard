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
	if cfg.PredictiveAdmissionMode != "off" || cfg.PredictiveAdmissionProfilePath != "" || cfg.PredictiveAdmissionProfileSHA256 != "" {
		t.Fatalf("predictive defaults = mode %q path %q SHA %q, want off with no profile", cfg.PredictiveAdmissionMode, cfg.PredictiveAdmissionProfilePath, cfg.PredictiveAdmissionProfileSHA256)
	}
}

func TestLoadPredictiveAdmissionProfileIdentity(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_PROFILE_PATH", " /profiles/gemma4.json ")
	t.Setenv("PREDICTIVE_ADMISSION_PROFILE_SHA256", " AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PredictiveAdmissionProfilePath != "/profiles/gemma4.json" || cfg.PredictiveAdmissionProfileSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("predictive profile identity = %q/%q", cfg.PredictiveAdmissionProfilePath, cfg.PredictiveAdmissionProfileSHA256)
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
