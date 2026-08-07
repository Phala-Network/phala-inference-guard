package pigconfig

import (
	"strings"
	"testing"
)

func TestV012OmittedPrefillBoundsSelectAutomaticCapabilityProfile(t *testing.T) {
	for _, name := range predictivePrefillEnvironmentNamesForTest() {
		t.Setenv(name, "")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load automatic profile: %v", err)
	}
	if cfg.PredictivePrefillRegularTokens != 0 || cfg.PredictivePrefillExclusiveTokens != 0 ||
		cfg.PredictivePrefillQuiescentTokens != 0 || cfg.PredictivePrefillAggregateBudgetTokens != 0 {
		t.Fatalf(
			"omitted Prefill profile = %d/%d/%d/%d, want automatic zero sentinel",
			cfg.PredictivePrefillRegularTokens,
			cfg.PredictivePrefillExclusiveTokens,
			cfg.PredictivePrefillQuiescentTokens,
			cfg.PredictivePrefillAggregateBudgetTokens,
		)
	}
}

func TestV012PrefillOverrideMustBeAllOrNone(t *testing.T) {
	for _, name := range predictivePrefillEnvironmentNamesForTest() {
		t.Setenv(name, "")
	}
	t.Setenv("PREDICTIVE_PREFILL_REGULAR_TOKENS", "32768")

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "all be set") {
		t.Fatalf("partial Prefill override error = %v, want all-or-none rejection", err)
	}
}

func TestV012CompletePrefillOverrideRemainsSupported(t *testing.T) {
	values := map[string]string{
		"PREDICTIVE_PREFILL_REGULAR_TOKENS":          "32768",
		"PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS":        "131072",
		"PREDICTIVE_PREFILL_QUIESCENT_TOKENS":        "262144",
		"PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS": "196608",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load explicit profile: %v", err)
	}
	if cfg.PredictivePrefillRegularTokens != 32768 || cfg.PredictivePrefillExclusiveTokens != 131072 ||
		cfg.PredictivePrefillQuiescentTokens != 262144 || cfg.PredictivePrefillAggregateBudgetTokens != 196608 {
		t.Fatalf(
			"explicit Prefill profile = %d/%d/%d/%d",
			cfg.PredictivePrefillRegularTokens,
			cfg.PredictivePrefillExclusiveTokens,
			cfg.PredictivePrefillQuiescentTokens,
			cfg.PredictivePrefillAggregateBudgetTokens,
		)
	}
}

func predictivePrefillEnvironmentNamesForTest() []string {
	return []string{
		"PREDICTIVE_PREFILL_REGULAR_TOKENS",
		"PREDICTIVE_PREFILL_EXCLUSIVE_TOKENS",
		"PREDICTIVE_PREFILL_QUIESCENT_TOKENS",
		"PREDICTIVE_PREFILL_AGGREGATE_BUDGET_TOKENS",
	}
}
