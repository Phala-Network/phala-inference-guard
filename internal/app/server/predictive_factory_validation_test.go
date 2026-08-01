package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestPredictiveProfilePinsObserverTiming(t *testing.T) {
	cfg := config{}
	writePredictiveFactoryTestProfile(t, &cfg)
	content, err := os.ReadFile(cfg.PredictiveAdmissionProfilePath)
	if err != nil {
		t.Fatalf("read profile: %v", err)
	}
	var profile map[string]any
	if err := json.Unmarshal(content, &profile); err != nil {
		t.Fatalf("decode profile fixture: %v", err)
	}
	for _, field := range []string{
		"metrics_poll_interval_milliseconds",
		"metrics_maximum_age_milliseconds",
		"metrics_request_timeout_milliseconds",
		"preemption_cooldown_milliseconds",
		"calibrator_maximum_cells",
	} {
		if _, ok := profile[field]; !ok {
			t.Fatalf("profile fixture is missing immutable observer field %s", field)
		}
	}
	if _, err := loadPredictiveProfile(cfg.PredictiveAdmissionProfilePath, cfg.PredictiveAdmissionProfileSHA256); err != nil {
		t.Fatalf("load schema-3 predictive profile with pinned observer timing and learner bound: %v", err)
	}
}

func TestPredictiveProfileRejectsHashStructureAssetAndModelMismatch(t *testing.T) {
	for name, test := range map[string]struct {
		mutate func(*config, map[string]any)
		want   string
	}{
		"profile-sha": {
			mutate: func(cfg *config, _ map[string]any) { cfg.PredictiveAdmissionProfileSHA256 = strings.Repeat("f", 64) },
			want:   "profile SHA-256 mismatch",
		},
		"unknown-field": {
			mutate: func(_ *config, profile map[string]any) { profile["unknown_predictive_field"] = true },
			want:   "unknown field",
		},
		"tokenizer-sha": {
			mutate: func(_ *config, profile map[string]any) {
				profile["tokenizer"].(map[string]any)["sha256"] = strings.Repeat("f", 64)
			},
			want: "verify predictive tokenizer",
		},
		"tokenizer-config-sha": {
			mutate: func(_ *config, profile map[string]any) {
				profile["tokenizer_config"].(map[string]any)["sha256"] = strings.Repeat("f", 64)
			},
			want: "verify predictive tokenizer config",
		},
		"template-sha": {
			mutate: func(_ *config, profile map[string]any) {
				profile["chat_template"].(map[string]any)["sha256"] = strings.Repeat("f", 64)
			},
			want: "verify predictive chat template",
		},
		"backend-image": {
			mutate: func(_ *config, profile map[string]any) { profile["backend_image_digest"] = "latest" },
			want:   "pinned sha256 digest",
		},
		"renderer": {
			mutate: func(_ *config, profile map[string]any) { profile["renderer_version"] = "other" },
			want:   "renderer_version",
		},
		"special-token-policy": {
			mutate: func(_ *config, profile map[string]any) { profile["chat_add_special_tokens"] = true },
			want:   "special-token policy",
		},
		"unaligned-protected-kv": {
			mutate: func(_ *config, profile map[string]any) { profile["protected_kv_tokens"] = 900001 },
			want:   "KV capacities",
		},
		"protected-kv-above-maximum": {
			mutate: func(_ *config, profile map[string]any) { profile["protected_kv_tokens"] = 1000004 },
			want:   "KV capacities",
		},
		"metrics-age-before-poll": {
			mutate: func(_ *config, profile map[string]any) { profile["metrics_maximum_age_milliseconds"] = 100 },
			want:   "maximum age must be >= poll interval",
		},
		"metrics-timeout-zero": {
			mutate: func(_ *config, profile map[string]any) { profile["metrics_request_timeout_milliseconds"] = 0 },
			want:   "metrics_request_timeout_milliseconds is invalid",
		},
		"decode-horizon": {
			mutate: func(_ *config, profile map[string]any) { profile["default_decode_horizon"] = 300000 },
			want:   "decode horizons",
		},
		"scheduler": {
			mutate: func(_ *config, profile map[string]any) { profile["base_completion_tps"] = -1.0 },
			want:   "scheduler",
		},
		"calibrator-global-cell-bound": {
			mutate: func(_ *config, profile map[string]any) { profile["calibrator_maximum_cells"] = 0 },
			want:   "global cell bound",
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := config{}
			writePredictiveFactoryTestProfile(t, &cfg)
			profile := readPredictiveFactoryTestProfile(t, cfg.PredictiveAdmissionProfilePath)
			test.mutate(&cfg, profile)
			if name != "profile-sha" {
				rewritePredictiveFactoryTestProfile(t, &cfg, profile)
			}
			_, err := loadPredictiveProfile(cfg.PredictiveAdmissionProfilePath, cfg.PredictiveAdmissionProfileSHA256)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("load error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestPredictiveProfileRejectsDuplicateJSONKeys(t *testing.T) {
	cfg := config{}
	writePredictiveFactoryTestProfile(t, &cfg)
	content, err := os.ReadFile(cfg.PredictiveAdmissionProfilePath)
	if err != nil {
		t.Fatalf("read predictive profile: %v", err)
	}
	duplicate := append([]byte(`{"schema_version":3,`), content[1:]...)
	if err := os.WriteFile(cfg.PredictiveAdmissionProfilePath, duplicate, 0o600); err != nil {
		t.Fatalf("write duplicate-key profile: %v", err)
	}
	cfg.PredictiveAdmissionProfileSHA256 = predictiveFactoryTestSHA(duplicate)
	_, err = loadPredictiveProfile(cfg.PredictiveAdmissionProfilePath, cfg.PredictiveAdmissionProfileSHA256)
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("duplicate-key load error = %v", err)
	}
}

func readPredictiveFactoryTestProfile(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read predictive profile: %v", err)
	}
	var profile map[string]any
	if err := json.Unmarshal(content, &profile); err != nil {
		t.Fatalf("decode predictive profile: %v", err)
	}
	return profile
}

func rewritePredictiveFactoryTestProfile(t *testing.T, cfg *config, profile map[string]any) {
	t.Helper()
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal predictive profile: %v", err)
	}
	if err := os.WriteFile(cfg.PredictiveAdmissionProfilePath, encoded, 0o600); err != nil {
		t.Fatalf("rewrite predictive profile: %v", err)
	}
	cfg.PredictiveAdmissionProfileSHA256 = predictiveFactoryTestSHA(encoded)
}
