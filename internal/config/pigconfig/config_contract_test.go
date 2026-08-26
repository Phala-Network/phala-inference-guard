package pigconfig

import (
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPredictiveAdmissionDefaultsToEnforceAndRejectsRetiredModes(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load default predictive config: %v", err)
	}
	if cfg.PredictiveAdmissionMode != "enforce" {
		t.Fatalf("default predictive mode = %q, want enforce", cfg.PredictiveAdmissionMode)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate default enforce config: %v", err)
	}

	for _, mode := range []string{"shadow", "enforce"} {
		t.Run("accept_"+mode, func(t *testing.T) {
			candidate := cfg
			candidate.PredictiveAdmissionMode = mode
			if err := Validate(candidate); err != nil {
				t.Fatalf("Validate mode %q: %v", mode, err)
			}
		})
	}
	for _, mode := range []string{"off", "disabled"} {
		t.Run("reject_"+mode, func(t *testing.T) {
			candidate := cfg
			candidate.PredictiveAdmissionMode = mode
			if err := Validate(candidate); err == nil || !strings.Contains(err.Error(), "shadow or enforce") {
				t.Fatalf("Validate mode %q error = %v, want retired-mode rejection", mode, err)
			}
		})
	}
}

func TestConfigHasNoRetiredModeOwnership(t *testing.T) {
	legacyFields := []string{
		"Backends", "BackendRouting", "GlobalLimit",
		"OpenAICompatStripEmptyToolCalls", "OpenAICompatBodyBytes", "OpenAICompatFailOpen",
		"ClassifyOutputTokens", "MediumBodyBytes", "LongBodyBytes", "VeryLongBodyBytes",
		"MediumOutputTokens", "LongOutputTokens", "VeryLongOutputTokens",
		"AdaptiveOutput", "AdaptiveOutputWindow", "AdaptiveOutputMin",
		"AdaptiveOutputMediumQ", "AdaptiveOutputLongQ", "AdaptiveOutputVeryQ",
		"AdaptiveOutputGreen", "AdaptiveOutputYellow", "AdaptiveOutputRed",
		"BackendPriorityInjectionEnabled", "BackendPriorityMode", "BackendPriorityRewriteStrategy",
		"BackendPriorityField", "BackendPriorityPremiumValue", "BackendPriorityBasicValue",
		"BackendPriorityBodyBytes", "BackendPriorityBufferBytes", "BackendPriorityStreamBufferBytes",
		"BackendPriorityRewriteLimit", "BackendPriorityFailOpen",
		"DynamicEnabled", "DynamicEnforce", "DynamicMetricsURL", "DynamicMetricsURLs",
		"DynamicPollInterval", "DynamicFailsafeState", "DynamicKVYellow", "DynamicKVRed",
		"DynamicRunningYellow", "DynamicRunningRed", "DynamicWaitingYellow", "DynamicWaitingRed",
		"DynamicPreemptRed", "DynamicPressureEnabled", "DynamicPressureHeadroom",
		"DynamicPressureMinLimit", "DynamicPressureLearnRatio", "DynamicPressureLearnMinRunning",
		"DynamicUserTPSEnabled", "DynamicTTFTEnabled", "DynamicUserTPSYellow", "DynamicUserTPSRed",
		"DynamicUserTPSMinRun", "DynamicUserTPSYellowN", "DynamicUserTPSRedN", "DynamicTTFTPolicy",
		"DynamicUserTPSGraceMin", "DynamicUserTPSGraceMax", "DynamicUserTPSGraceBps",
		"DynamicUserTPSGraceMul", "DynamicUserTPSCapacityRatio", "DynamicUserTPSCapacitySmoothing",
		"DynamicUserTPSCapacityLearn", "DynamicUserTPSCapacityRatioMax", "DynamicUserTPSCapacityStepUp",
		"DynamicUserTPSCapacityHealthyN", "DynamicUserTPSCapacityHealthyMul",
		"DynamicGlobalGreen", "DynamicGlobalYellow", "DynamicGlobalRed",
		"QoSQueueWait", "QoSQueuePoll", "KVAdmissionMode", "KVAdmissionPolicy",
		"PredictiveKVTargetRatio", "PredictivePreemptionCooldown",
		"PredictiveKVHardRatio", "PredictiveMaxModelLenTokens",
		"PredictivePrefillRegularTokens", "PredictivePrefillExclusiveTokens",
		"PredictivePrefillQuiescentTokens", "PredictivePrefillAggregateBudgetTokens",
		"SSEKeepAliveEnabled", "SSEEarlyBridgeEnabled",
	}
	typeOfConfig := reflect.TypeOf(Config{})
	for _, field := range legacyFields {
		if _, ok := typeOfConfig.FieldByName(field); ok {
			t.Errorf("legacy Config field %s still exists", field)
		}
	}
}

func TestRetiredEnvironmentCannotReenableRemovedModes(t *testing.T) {
	retired := map[string]string{
		"GLOBAL_LIMIT":                           "not-an-int",
		"DYNAMIC_ENABLED":                        "not-a-bool",
		"DYNAMIC_ENFORCE":                        "not-a-bool",
		"DYNAMIC_METRICS_URL":                    "http://retired.invalid/metrics",
		"KV_ADMISSION_MODE":                      "shadow",
		"BACKEND_PRIORITY_INJECTION_ENABLED":     "not-a-bool",
		"OPENAI_COMPAT_STRIP_EMPTY_TOOL_CALLS":   "not-a-bool",
		"CLASSIFY_OUTPUT_TOKENS":                 "not-a-bool",
		"ADAPTIVE_OUTPUT_ENABLED":                "not-a-bool",
		"SSE_KEEPALIVE_ENABLED":                  "not-a-bool",
		"SSE_EARLY_BRIDGE_ENABLED":               "not-a-bool",
		"PREDICTIVE_KV_TARGET_RATIO":             "not-a-float",
		"PREDICTIVE_PREEMPTION_COOLDOWN_SECONDS": "not-an-int",
		"UPSTREAMS":                              "http://retired-a.invalid,http://retired-b.invalid",
		"BACKENDS":                               "a=http://retired-a.invalid|http://retired-a.invalid/metrics",
	}
	for _, name := range []string{
		"PREDICTIVE_KV_HARD_RATIO",
		"PREDICTIVE_MAX_MODEL_LEN_TOKENS",
		"PREDICTIVE_PREFILL_REGULAR_TOKENS",
		"PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS",
		"PREDICTIVE_PREFILL_QUIESCENT_TOKENS",
		"PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS",
	} {
		retired[name] = "retired-value"
	}
	for name, value := range retired {
		t.Setenv(name, value)
	}
	t.Setenv("UPSTREAM", "http://backend:8000")
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("retired environment changed config loading: %v", err)
	}
	if cfg.Upstream != "http://backend:8000" || cfg.PredictiveAdmissionMode != "enforce" {
		t.Fatalf("retired environment changed active config: upstream=%q mode=%q", cfg.Upstream, cfg.PredictiveAdmissionMode)
	}
	field := reflect.ValueOf(cfg).FieldByName("PredictiveMetricsURL")
	if !field.IsValid() || field.Kind() != reflect.String || field.String() != "http://backend:8000/metrics" {
		t.Fatalf("derived predictive metrics URL = %v, want http://backend:8000/metrics", field)
	}
}

func TestValidationRequiresExactlyOneUpstream(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Upstream = "http://a.invalid,http://b.invalid"
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("Validate multi-upstream error = %v, want single-upstream rejection", err)
	}
}

func TestProductionDefaultsNeedNoPredictiveComposeOverrides(t *testing.T) {
	t.Setenv("UPSTREAM", "http://backend:8000/v1")
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "")
	t.Setenv("PREDICTIVE_METRICS_URL", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load minimal production config: %v", err)
	}
	if cfg.PredictiveAdmissionMode != "enforce" || cfg.PredictiveMetricsURL != "http://backend:8000/metrics" {
		t.Fatalf("minimal production derivation mode/url=%q/%q", cfg.PredictiveAdmissionMode, cfg.PredictiveMetricsURL)
	}
	if cfg.PredictiveObservationPollInterval != 500*time.Millisecond || cfg.PredictiveMaximumMetricsAge != 1500*time.Millisecond {
		t.Fatalf("default observer cadence/freshness=%s/%s", cfg.PredictiveObservationPollInterval, cfg.PredictiveMaximumMetricsAge)
	}
	if cfg.StatusLogInterval != 30*time.Second {
		t.Fatalf("default status log interval=%s, want low-noise 30s", cfg.StatusLogInterval)
	}
	if cfg.LogLevel != "info" {
		t.Fatalf("default log level=%q, want info", cfg.LogLevel)
	}
	if cfg.PredictiveTPSReference != 0 {
		t.Fatalf("default TPS reference=%v, want disabled zero", cfg.PredictiveTPSReference)
	}
	if cfg.PredictiveMaxModelLenTokens != 0 || cfg.PredictivePrefillRegularTokens != 0 || cfg.PredictivePrefillExclusiveTokens != 0 ||
		cfg.PredictivePrefillQuiescentTokens != 0 || cfg.PredictivePrefillAggregateBudgetTokens != 0 {
		t.Fatalf("minimal production config unexpectedly disables startup Prefill derivation: %+v", cfg)
	}
	minimum650KBodyWindow := int64(650_000 * cfg.PredictiveEstimator.MaxBytesPerToken)
	if cfg.PredictiveScannerBodyBytes < minimum650KBodyWindow {
		t.Fatalf("production scanner ceiling=%d does not cover the model-neutral 650K window=%d", cfg.PredictiveScannerBodyBytes, minimum650KBodyWindow)
	}
}

func TestRuntimeLogLevelIsBounded(t *testing.T) {
	for _, level := range []string{"info", "INFO", " debug "} {
		t.Run(strings.TrimSpace(level), func(t *testing.T) {
			t.Setenv("PIG_LOG_LEVEL", level)
			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load log level %q: %v", level, err)
			}
			if err := Validate(cfg); err != nil {
				t.Fatalf("Validate log level %q as %q: %v", level, cfg.LogLevel, err)
			}
		})
	}
	t.Setenv("PIG_LOG_LEVEL", "trace")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load invalid log level: %v", err)
	}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "PIG_LOG_LEVEL") {
		t.Fatalf("Validate trace error=%v, want bounded log-level rejection", err)
	}
}

func TestTestsCanExplicitlyOverrideTypedPredictivePolicy(t *testing.T) {
	t.Setenv("PREDICTIVE_ADMISSION_MODE", "shadow")
	t.Setenv("PREDICTIVE_METRICS_URL", "http://fixture:9000/custom-metrics")
	t.Setenv("PREDICTIVE_OBSERVATION_POLL_INTERVAL_MS", "20")
	t.Setenv("PREDICTIVE_MAX_METRICS_AGE_MS", "100")
	t.Setenv("PREDICTIVE_KV_HARD_RATIO", "0.90")
	t.Setenv("PREDICTIVE_TPS_REFERENCE", "23.5")
	t.Setenv("PREDICTIVE_MAX_MODEL_LEN_TOKENS", "8192")
	t.Setenv("PREDICTIVE_PREFILL_REGULAR_TOKENS", "1024")
	t.Setenv("PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS", "2048")
	t.Setenv("PREDICTIVE_PREFILL_QUIESCENT_TOKENS", "4096")
	t.Setenv("PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS", "2048")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load explicit test policy: %v", err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate explicit test policy: %v", err)
	}
	if cfg.PredictiveAdmissionMode != "shadow" || cfg.PredictiveObservationPollInterval != 20*time.Millisecond ||
		cfg.PredictiveMaximumMetricsAge != 100*time.Millisecond || cfg.PredictiveKVHardRatio != 0.90 ||
		cfg.PredictiveTPSReference != 23.5 ||
		cfg.PredictiveMaxModelLenTokens != 8192 ||
		cfg.PredictivePrefillRegularTokens != 1024 || cfg.PredictivePrefillExclusiveTokens != 2048 ||
		cfg.PredictivePrefillQuiescentTokens != 4096 || cfg.PredictivePrefillAggregateBudgetTokens != 2048 {
		t.Fatalf("explicit test policy was not loaded exactly: %+v", cfg)
	}
}

func TestPredictiveTPSReferenceValidation(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, reference := range []float64{0, 0.001, 20, 1_000_000} {
		candidate := cfg
		candidate.PredictiveTPSReference = reference
		if err := Validate(candidate); err != nil {
			t.Fatalf("Validate reference %v: %v", reference, err)
		}
	}
	for _, reference := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1), 1_000_000.001} {
		candidate := cfg
		candidate.PredictiveTPSReference = reference
		if err := Validate(candidate); err == nil || !strings.Contains(err.Error(), "PREDICTIVE_TPS_REFERENCE") {
			t.Fatalf("Validate reference %v error=%v, want bounded finite rejection", reference, err)
		}
	}
}

func TestPredictiveCapabilityEnvironmentOverridesAreAllOrNone(t *testing.T) {
	overrides := []struct {
		name  string
		value string
	}{
		{name: "PREDICTIVE_MAX_MODEL_LEN_TOKENS", value: "8192"},
		{name: "PREDICTIVE_PREFILL_REGULAR_TOKENS", value: "1024"},
		{name: "PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS", value: "2048"},
		{name: "PREDICTIVE_PREFILL_QUIESCENT_TOKENS", value: "4096"},
		{name: "PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS", value: "2048"},
	}
	for _, override := range overrides {
		t.Run(override.name, func(t *testing.T) {
			for _, value := range overrides {
				t.Setenv(value.name, "")
			}
			t.Setenv(override.name, override.value)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), "all be set or all be omitted") {
				t.Fatalf("partial capability override error = %v", err)
			}
		})
	}
}
