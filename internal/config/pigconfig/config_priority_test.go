package pigconfig

import "testing"

func TestLoadPriorityConfigDefaultsEnabled(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.BackendPriorityInjectionEnabled {
		t.Fatalf("BackendPriorityInjectionEnabled = false, want true")
	}
	if cfg.BackendPriorityMode != "all" {
		t.Fatalf("BackendPriorityMode = %q, want all", cfg.BackendPriorityMode)
	}
	if cfg.BackendPriorityRewriteStrategy != "field_scan" {
		t.Fatalf("BackendPriorityRewriteStrategy = %q, want field_scan", cfg.BackendPriorityRewriteStrategy)
	}
	if cfg.BackendPriorityField != "priority" {
		t.Fatalf("BackendPriorityField = %q, want priority", cfg.BackendPriorityField)
	}
	if cfg.BackendPriorityPremiumValue != -100 {
		t.Fatalf("BackendPriorityPremiumValue = %d, want -100", cfg.BackendPriorityPremiumValue)
	}
	if cfg.BackendPriorityBasicValue != 0 {
		t.Fatalf("BackendPriorityBasicValue = %d, want 0", cfg.BackendPriorityBasicValue)
	}
	if cfg.BackendPriorityBodyBytes != 32*1024*1024 {
		t.Fatalf("BackendPriorityBodyBytes = %d, want 33554432", cfg.BackendPriorityBodyBytes)
	}
	if cfg.BackendPriorityBufferBytes != 0 {
		t.Fatalf("BackendPriorityBufferBytes = %d, want 0", cfg.BackendPriorityBufferBytes)
	}
	if cfg.BackendPriorityStreamBufferBytes != 2*1024*1024 {
		t.Fatalf("BackendPriorityStreamBufferBytes = %d, want 2097152", cfg.BackendPriorityStreamBufferBytes)
	}
	if !cfg.BackendPriorityFailOpen {
		t.Fatalf("BackendPriorityFailOpen = false, want true")
	}
}

func TestValidatePriorityConfigRejectsNegativeBufferBytes(t *testing.T) {
	cfg := Config{
		BackendPriorityInjectionEnabled: true,
		BackendPriorityMode:             "all",
		BackendPriorityField:            "priority",
		BackendPriorityBodyBytes:        1,
		BackendPriorityBufferBytes:      -1,
		BackendPriorityRewriteLimit:     1,
	}
	if err := validatePriorityConfig(cfg); err == nil {
		t.Fatalf("validatePriorityConfig accepted negative buffer bytes")
	}
}

func TestValidatePriorityConfigRejectsBadRewriteStrategy(t *testing.T) {
	cfg := Config{
		BackendPriorityInjectionEnabled: true,
		BackendPriorityMode:             "all",
		BackendPriorityRewriteStrategy:  "bad",
		BackendPriorityField:            "priority",
		BackendPriorityBodyBytes:        1,
		BackendPriorityRewriteLimit:     1,
	}
	if err := validatePriorityConfig(cfg); err == nil {
		t.Fatalf("validatePriorityConfig accepted bad rewrite strategy")
	}
}

func TestValidatePriorityConfigRejectsBadMode(t *testing.T) {
	cfg := Config{
		BackendPriorityInjectionEnabled: true,
		BackendPriorityMode:             "bad",
		BackendPriorityField:            "priority",
		BackendPriorityBodyBytes:        1,
		BackendPriorityRewriteLimit:     1,
	}
	if err := validatePriorityConfig(cfg); err == nil {
		t.Fatalf("validatePriorityConfig accepted bad mode")
	}
}

func TestValidatePriorityConfigRejectsBadField(t *testing.T) {
	cfg := Config{
		BackendPriorityInjectionEnabled: true,
		BackendPriorityMode:             "all",
		BackendPriorityField:            "1priority",
		BackendPriorityBodyBytes:        1,
		BackendPriorityRewriteLimit:     1,
	}
	if err := validatePriorityConfig(cfg); err == nil {
		t.Fatalf("validatePriorityConfig accepted bad field")
	}
}
