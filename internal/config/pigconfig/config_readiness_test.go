package pigconfig

import "testing"

func TestBackendReadinessPathConfigurationAllowsExactChoices(t *testing.T) {
	for _, value := range []string{"", "/ready", "/health"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BACKEND_READINESS_PATH", value)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.BackendReadinessPath != value {
				t.Fatalf("backend readiness path = %q, want %q", cfg.BackendReadinessPath, value)
			}
			if err := Validate(cfg); err != nil {
				t.Fatal(err)
			}
			cfg.BackendReadinessPath = "/healthz"
			if err := Validate(cfg); err == nil {
				t.Fatal("direct configuration accepted an unqualified readiness path")
			}
		})
	}
}

func TestBackendReadinessPathConfigurationRejectsUnqualifiedValues(t *testing.T) {
	for _, value := range []string{"/healthz", "health", "/health/", "/%68ealth", "/ready?x=1", "/ready#x", "http://backend/health", "//health", " /health", "/health\n", "auto"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BACKEND_READINESS_PATH", value)
			if _, err := Load(); err == nil {
				t.Fatal("unqualified backend readiness path was accepted")
			}
		})
	}
}
