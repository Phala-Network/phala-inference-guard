package pigconfig

import "testing"

func TestLoadAdminToken(t *testing.T) {
	t.Setenv("ADMIN_TOKEN", "admin-secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AdminToken != "admin-secret" {
		t.Fatalf("AdminToken = %q", cfg.AdminToken)
	}
}
