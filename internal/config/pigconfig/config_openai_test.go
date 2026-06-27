package pigconfig

import "testing"

func TestLoadOpenAIConfigDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.APIAuthEnabled {
		t.Fatalf("APIAuthEnabled = true without TOKEN, want false")
	}
	if !cfg.OpenAICompatStripEmptyToolCalls {
		t.Fatalf("OpenAICompatStripEmptyToolCalls = false, want true")
	}
	if cfg.OpenAICompatBodyBytes != defaultOpenAICompatBodyBytes {
		t.Fatalf("OpenAICompatBodyBytes = %d, want %d", cfg.OpenAICompatBodyBytes, defaultOpenAICompatBodyBytes)
	}
	if !cfg.AttestationEnabled {
		t.Fatalf("AttestationEnabled = false, want true")
	}
	if cfg.AttestationNVIDIACommandTimeout <= 0 {
		t.Fatalf("AttestationNVIDIACommandTimeout = %s, want > 0", cfg.AttestationNVIDIACommandTimeout)
	}
	wantArgs := []string{"--nonce", "{nonce}", "--arch", "HOPPER"}
	if len(cfg.AttestationNVIDIACommandArgs) != len(wantArgs) {
		t.Fatalf("AttestationNVIDIACommandArgs len=%d want %d: %#v", len(cfg.AttestationNVIDIACommandArgs), len(wantArgs), cfg.AttestationNVIDIACommandArgs)
	}
	for i := range wantArgs {
		if cfg.AttestationNVIDIACommandArgs[i] != wantArgs[i] {
			t.Fatalf("AttestationNVIDIACommandArgs[%d]=%q want %q", i, cfg.AttestationNVIDIACommandArgs[i], wantArgs[i])
		}
	}
	if cfg.Upstream != "http://backend:8000" {
		t.Fatalf("Upstream = %q, want http://backend:8000", cfg.Upstream)
	}
}

func TestLoadOpenAIConfigUsesGPUArchInDefaultNVIDIACommandArgs(t *testing.T) {
	t.Setenv("ATTESTATION_GPU_ARCH", "BLACKWELL")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	wantArgs := []string{"--nonce", "{nonce}", "--arch", "BLACKWELL"}
	if len(cfg.AttestationNVIDIACommandArgs) != len(wantArgs) {
		t.Fatalf("AttestationNVIDIACommandArgs len=%d want %d: %#v", len(cfg.AttestationNVIDIACommandArgs), len(wantArgs), cfg.AttestationNVIDIACommandArgs)
	}
	for i := range wantArgs {
		if cfg.AttestationNVIDIACommandArgs[i] != wantArgs[i] {
			t.Fatalf("AttestationNVIDIACommandArgs[%d]=%q want %q", i, cfg.AttestationNVIDIACommandArgs[i], wantArgs[i])
		}
	}
}

func TestLoadOpenAIConfigEnablesAPIAuthWithToken(t *testing.T) {
	t.Setenv("TOKEN", "secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.APIAuthEnabled {
		t.Fatalf("APIAuthEnabled = false with TOKEN, want true")
	}
	if len(cfg.APIAuthPaths) != len(cfg.QoSPaths) {
		t.Fatalf("APIAuthPaths len=%d want %d", len(cfg.APIAuthPaths), len(cfg.QoSPaths))
	}
}

func TestValidateOpenAIConfigRejectsAPIAuthWithoutToken(t *testing.T) {
	cfg := Config{
		APIAuthEnabled: true,
		APIAuthPaths:   []string{"/v1/chat/completions"},
	}
	if err := validateOpenAIConfig(cfg); err == nil {
		t.Fatalf("validateOpenAIConfig accepted API auth without token")
	}
}

func TestValidateOpenAIConfigRejectsRequiredNVIDIAEvidenceWithoutSource(t *testing.T) {
	cfg := Config{
		AttestationEnabled:               true,
		AttestationRequireNVIDIAEvidence: true,
		AttestationNVIDIACommandTimeout:  1,
	}
	if err := validateOpenAIConfig(cfg); err == nil {
		t.Fatalf("validateOpenAIConfig accepted required NVIDIA evidence without source")
	}
}
