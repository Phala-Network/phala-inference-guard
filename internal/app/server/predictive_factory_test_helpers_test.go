package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePredictiveFactoryTestProfile(t *testing.T, cfg *config) {
	t.Helper()
	tokenizerPath, err := filepath.Abs(filepath.Join("..", "..", "..", "native", "tokenizer", "fixtures", "ffi-wordlevel-tokenizer.json"))
	if err != nil {
		t.Fatalf("absolute tokenizer fixture: %v", err)
	}
	tokenizerConfigPath := filepath.Join(t.TempDir(), "tokenizer_config.json")
	templatePath := filepath.Join(t.TempDir(), "chat_template.jinja")
	if err := os.WriteFile(tokenizerConfigPath, []byte(`{"model_max_length":262144}`), 0o600); err != nil {
		t.Fatalf("write tokenizer config: %v", err)
	}
	if err := os.WriteFile(templatePath, []byte("fixture-gemma4-template\n"), 0o600); err != nil {
		t.Fatalf("write chat template: %v", err)
	}
	profile := map[string]any{
		"schema_version":                          1,
		"manifest_id":                             "factory-test-manifest",
		"profile_id":                              "factory-test-profile",
		"predictor_version":                       "factory-test-predictor-v1",
		"served_model":                            "google/gemma-4-fixture",
		"model_revision":                          "fixture-model-revision",
		"backend_kind":                            "vllm",
		"backend_version":                         "fixture-vllm-version",
		"backend_source_revision":                 "fixture-vllm-revision",
		"backend_image_digest":                    "sha256:" + strings.Repeat("0", 64),
		"backend_epoch":                           "factory-test-backend-epoch",
		"renderer_version":                        "gemma4-text-v1",
		"bos_token":                               "<bos>",
		"tokenizer":                               predictiveFactoryTestAsset(t, tokenizerPath),
		"tokenizer_config":                        predictiveFactoryTestAsset(t, tokenizerConfigPath),
		"chat_template":                           predictiveFactoryTestAsset(t, templatePath),
		"completion_add_special_tokens":           true,
		"chat_add_special_tokens":                 false,
		"block_size":                              4,
		"model_maximum_length":                    262144,
		"maximum_kv_tokens":                       1000000,
		"protected_kv_tokens":                     900000,
		"default_decode_horizon":                  128,
		"maximum_decode_horizon":                  262144,
		"base_completion_tps":                     120.0,
		"prefill_tps_penalty_per_k_token":         0.5,
		"base_ttft_milliseconds":                  10,
		"ttft_per_prefill_token_microseconds":     1,
		"base_tpot_milliseconds":                  5,
		"tpot_per_existing_sequence_milliseconds": 1,
		"workspace_risk_upper":                    0.0,
		"preemption_risk_upper":                   0.0,
		"profile_confidence":                      0.99,
		"user_tps_target":                         20.0,
		"ttft_slo_milliseconds":                   5000,
		"tpot_slo_milliseconds":                   100,
		"workspace_risk_budget":                   1.0,
		"preemption_risk_budget":                  0.0,
		"minimum_confidence":                      0.95,
		"calibrator_minimum_samples":              4,
		"calibrator_maximum_samples_per_cell":     16,
		"calibrator_max_age_seconds":              300,
		"calibrator_lower_quantile":               0.1,
		"calibrator_upper_quantile":               0.9,
		"calibrator_minimum_tps_multiplier":       0.5,
		"calibrator_maximum_tps_multiplier":       1.5,
		"calibrator_minimum_latency_multiplier":   0.5,
		"calibrator_maximum_latency_multiplier":   2.0,
		"calibrator_confidence":                   0.98,
		"calibrator_decode_sequence_bucket":       1,
		"calibrator_context_token_bucket":         4096,
		"calibrator_prefill_token_bucket":         4096,
		"calibrator_kv_token_bucket":              4096,
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		t.Fatalf("marshal predictive profile: %v", err)
	}
	profilePath := filepath.Join(t.TempDir(), "predictive-profile.json")
	if err := os.WriteFile(profilePath, encoded, 0o600); err != nil {
		t.Fatalf("write predictive profile: %v", err)
	}
	cfg.PredictiveAdmissionProfilePath = profilePath
	cfg.PredictiveAdmissionProfileSHA256 = predictiveFactoryTestSHA(encoded)
}

func predictiveFactoryTestAsset(t *testing.T, path string) map[string]any {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read predictive profile asset %q: %v", path, err)
	}
	return map[string]any{"path": path, "sha256": predictiveFactoryTestSHA(content)}
}

func predictiveFactoryTestSHA(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
