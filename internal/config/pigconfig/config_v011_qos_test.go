package pigconfig

import (
	"strings"
	"testing"
	"time"
)

func TestV011ObservationDefaultsAre500MillisecondsAndThreePollsFresh(t *testing.T) {
	t.Setenv("PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS", "")
	t.Setenv("PREDICTIVE_MAX_METRICS_AGE_MS", "")
	// The v0.11 controller must not silently inherit the deleted policy
	// surfaces, even when operators still have old variables in an environment.
	t.Setenv("DYNAMIC_POLL_INTERVAL_MS", "17")
	t.Setenv("KV_ADMISSION_MAX_METRICS_AGE_MS", "19")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PredictiveObservationPollInterval != 500*time.Millisecond {
		t.Fatalf("predictive observation poll interval = %s, want 500ms", cfg.PredictiveObservationPollInterval)
	}
	if cfg.PredictiveMaximumMetricsAge != 1500*time.Millisecond {
		t.Fatalf("predictive maximum metrics age = %s, want 1.5s", cfg.PredictiveMaximumMetricsAge)
	}
}

func TestV011ObservationCanonicalOverridesWin(t *testing.T) {
	t.Setenv("PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS", "250")
	t.Setenv("PREDICTIVE_MAX_METRICS_AGE_MS", "1250")
	t.Setenv("DYNAMIC_POLL_INTERVAL_MS", "9")
	t.Setenv("KV_ADMISSION_MAX_METRICS_AGE_MS", "11")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PredictiveObservationPollInterval != 250*time.Millisecond || cfg.PredictiveMaximumMetricsAge != 1250*time.Millisecond {
		t.Fatalf("canonical observation config = %s/%s, want 250ms/1.25s", cfg.PredictiveObservationPollInterval, cfg.PredictiveMaximumMetricsAge)
	}
}

func TestV011DeterministicTPSDefaultsIgnoreLegacyDynamicThresholds(t *testing.T) {
	t.Setenv("PREDICTIVE_TPS_TARGET", "")
	t.Setenv("PREDICTIVE_TPS_FLOOR", "")
	t.Setenv("DYNAMIC_SINGLE_USER_TPS_YELLOW", "99")
	t.Setenv("DYNAMIC_SINGLE_USER_TPS_RED", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PredictiveTPSTarget != 25 || cfg.PredictiveTPSFloor != 20 {
		t.Fatalf(
			"deterministic TPS target/floor = %.3f/%.3f, want independent defaults 25/20",
			cfg.PredictiveTPSTarget, cfg.PredictiveTPSFloor,
		)
	}
}

func TestV011DeterministicTPSCanonicalOverrides(t *testing.T) {
	t.Setenv("PREDICTIVE_TPS_TARGET", "27.5")
	t.Setenv("PREDICTIVE_TPS_FLOOR", "18.25")
	t.Setenv("DYNAMIC_SINGLE_USER_TPS_YELLOW", "99")
	t.Setenv("DYNAMIC_SINGLE_USER_TPS_RED", "1")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PredictiveTPSTarget != 27.5 || cfg.PredictiveTPSFloor != 18.25 {
		t.Fatalf(
			"deterministic TPS target/floor = %.3f/%.3f, want 27.5/18.25",
			cfg.PredictiveTPSTarget, cfg.PredictiveTPSFloor,
		)
	}
}

func TestValidateV011DeterministicTPSRejectsInvalidBounds(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "enforce")
	t.Setenv("PREDICTIVE_TPS_TARGET", "20")
	t.Setenv("PREDICTIVE_TPS_FLOOR", "20")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "PREDICTIVE_TPS") {
		t.Fatalf("Validate error = %v, want deterministic TPS bounds rejection", err)
	}
}

func TestV012PrefillAdmissionAutoSentinelAndCanonicalOverrides(t *testing.T) {
	for _, name := range []string{
		"PREDICTIVE_PREFILL_REGULAR_TOKENS",
		"PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS",
		"PREDICTIVE_PREFILL_QUIESCENT_TOKENS",
		"PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS",
	} {
		t.Setenv(name, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	if cfg.PredictivePrefillRegularTokens != 0 || cfg.PredictivePrefillExclusiveTokens != 0 ||
		cfg.PredictivePrefillQuiescentTokens != 0 || cfg.PredictivePrefillAggregateBudgetTokens != 0 {
		t.Fatalf("prefill automatic sentinel=%d/%d/%d/%d", cfg.PredictivePrefillRegularTokens,
			cfg.PredictivePrefillExclusiveTokens, cfg.PredictivePrefillQuiescentTokens,
			cfg.PredictivePrefillAggregateBudgetTokens)
	}

	t.Setenv("PREDICTIVE_PREFILL_REGULAR_TOKENS", "1024")
	t.Setenv("PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS", "2048")
	t.Setenv("PREDICTIVE_PREFILL_QUIESCENT_TOKENS", "4096")
	t.Setenv("PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS", "3072")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load overrides: %v", err)
	}
	if cfg.PredictivePrefillRegularTokens != 1024 || cfg.PredictivePrefillExclusiveTokens != 2048 ||
		cfg.PredictivePrefillQuiescentTokens != 4096 || cfg.PredictivePrefillAggregateBudgetTokens != 3072 {
		t.Fatalf("prefill overrides=%d/%d/%d/%d, want 1024/2048/4096/3072",
			cfg.PredictivePrefillRegularTokens, cfg.PredictivePrefillExclusiveTokens,
			cfg.PredictivePrefillQuiescentTokens, cfg.PredictivePrefillAggregateBudgetTokens)
	}
}

func TestValidateV011PrefillAdmissionRejectsInvalidOrdering(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "enforce")
	t.Setenv("PREDICTIVE_PREFILL_REGULAR_TOKENS", "65536")
	t.Setenv("PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS", "262144")
	t.Setenv("PREDICTIVE_PREFILL_QUIESCENT_TOKENS", "524288")
	t.Setenv("PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS", "131072")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "prefill tokens") {
		t.Fatalf("Validate error=%v, want invalid prefill ordering", err)
	}
}

func TestLoadV011IgnoresRemovedLearningEnvironment(t *testing.T) {
	for _, name := range []string{
		"PREDICTIVE_LEARNING_MINIMUM_SAMPLES",
		"PREDICTIVE_LEARNING_MAXIMUM_SAMPLES",
		"PREDICTIVE_LEARNING_MAXIMUM_CELLS",
		"PREDICTIVE_LEARNING_MAX_AGE_SECONDS",
		"PREDICTIVE_SHADOW_MAXIMUM_OBSERVATIONS",
		"PREDICTIVE_MINIMUM_CONFIDENCE",
		"PREDICTIVE_COLD_CONFIDENCE",
		"PREDICTIVE_LEARNED_CONFIDENCE",
		"PREDICTIVE_INPUT_UPPER_QUANTILE",
		"PREDICTIVE_INPUT_SAFETY_MARGIN",
		"PREDICTIVE_INPUT_MINIMUM_MULTIPLIER",
		"PREDICTIVE_INPUT_MAXIMUM_MULTIPLIER",
		"PREDICTIVE_TPS_MINIMUM_MULTIPLIER",
		"PREDICTIVE_TPS_MAXIMUM_MULTIPLIER",
		"PREDICTIVE_LATENCY_MINIMUM_MULTIPLIER",
		"PREDICTIVE_LATENCY_MAXIMUM_MULTIPLIER",
	} {
		t.Setenv(name, "removed-v0.11-value")
	}
	if _, err := Load(); err != nil {
		t.Fatalf("removed learning environment still controls v0.11 Load: %v", err)
	}
}

func TestValidateV011IgnoresRemovedLearningFields(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "enforce")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.DynamicUserTPSRed = 0
	cfg.DynamicTTFTPolicy.TargetSeconds = 0
	if err := Validate(cfg); err != nil {
		t.Fatalf("legacy dynamic QoS fields still control v0.11 Validate: %v", err)
	}
}

func TestValidateV011EnforceIgnoresDisabledBackendPriorityOnlyFields(t *testing.T) {
	base, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	base.BackendPriorityInjectionEnabled = true
	base.BackendPriorityMode = "retired-mode"
	base.BackendPriorityRewriteStrategy = "retired-strategy"
	base.BackendPriorityField = "1-invalid-retired-field"
	base.BackendPriorityBodyBytes = 0
	base.BackendPriorityBufferBytes = -1
	base.BackendPriorityStreamBufferBytes = 0
	base.BackendPriorityRewriteLimit = 0

	enforce := base
	enforce.PredictiveAdmissionMode = "enforce"
	if err := Validate(enforce); err != nil {
		t.Fatalf("request-aware enforce remained coupled to disabled backend priority config: %v", err)
	}

	shadow := base
	shadow.PredictiveAdmissionMode = "shadow"
	if err := Validate(shadow); err == nil || !strings.Contains(err.Error(), "BACKEND_PRIORITY") {
		t.Fatalf("shadow Validate error=%v, want active backend priority config rejection", err)
	}
}

func TestValidateV011RejectsMetricsAgeShorterThanPollInterval(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "enforce")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.PredictiveObservationPollInterval = 500 * time.Millisecond
	cfg.PredictiveMaximumMetricsAge = 499 * time.Millisecond
	err = Validate(cfg)
	if err == nil || !strings.Contains(err.Error(), "PREDICTIVE_MAX_METRICS_AGE") {
		t.Fatalf("Validate error=%v, want canonical freshness/cadence rejection", err)
	}
}
