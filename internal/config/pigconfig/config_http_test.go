package pigconfig

import (
	"testing"
	"time"
)

func TestHTTPTimeoutDefaultsAndBounds(t *testing.T) {
	names := []string{"HTTP_BODY_READ_TIMEOUT_SECONDS", "HTTP_WRITE_IDLE_TIMEOUT_SECONDS", "HTTP_IDLE_TIMEOUT_SECONDS"}
	for _, name := range names {
		t.Setenv(name, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPBodyReadTimeout != 120*time.Second || cfg.HTTPWriteIdleTimeout != 120*time.Second || cfg.HTTPIdleTimeout != 60*time.Second {
		t.Fatalf("unexpected HTTP timeout defaults: %s/%s/%s", cfg.HTTPBodyReadTimeout, cfg.HTTPWriteIdleTimeout, cfg.HTTPIdleTimeout)
	}
	for _, name := range names {
		for _, value := range []string{"0", "-1", "86401", "9223372036854775807", "invalid"} {
			t.Run(name+"/"+value, func(t *testing.T) {
				t.Setenv(name, value)
				if _, err := Load(); err == nil {
					t.Fatal("invalid or overflowing HTTP timeout was accepted")
				}
			})
		}
	}
}
