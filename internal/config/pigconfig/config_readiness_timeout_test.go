package pigconfig

import (
	"testing"
	"time"
)

func TestBackendReadinessTimeoutIsBoundedAndExplicit(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", time.Second}, {"1", time.Second}, {"3", 3 * time.Second}, {"5", 5 * time.Second},
	} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("BACKEND_READINESS_TIMEOUT_SECONDS", tc.value)
			cfg, err := Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.BackendReadinessTimeout != tc.want {
				t.Fatalf("timeout=%s want=%s", cfg.BackendReadinessTimeout, tc.want)
			}
			if err := Validate(cfg); err != nil {
				t.Fatal(err)
			}
			for _, invalid := range []time.Duration{-time.Second, time.Millisecond, 6 * time.Second} {
				cfg.BackendReadinessTimeout = invalid
				if err := Validate(cfg); err == nil {
					t.Fatalf("direct configuration accepted timeout %s", invalid)
				}
			}
		})
	}
	for _, value := range []string{"0", "-1", "6", "1.5", "5s", " 5", "999999999999999999999999999999999999"} {
		t.Run("reject_"+value, func(t *testing.T) {
			t.Setenv("BACKEND_READINESS_TIMEOUT_SECONDS", value)
			if _, err := Load(); err == nil {
				t.Fatal("invalid readiness deadline accepted")
			}
		})
	}
}
