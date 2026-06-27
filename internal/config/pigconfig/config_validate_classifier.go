package pigconfig

import (
	"fmt"
	"github.com/Phala-Network/phala-inference-guard/internal/support/names"
)

func validateClassifierConfig(cfg Config) error {
	for name, value := range map[string]int64{
		"MEDIUM_BODY_BYTES":    cfg.MediumBodyBytes,
		"LONG_BODY_BYTES":      cfg.LongBodyBytes,
		"VERY_LONG_BODY_BYTES": cfg.VeryLongBodyBytes,
	} {
		if err := validatePositive(name, value); err != nil {
			return err
		}
	}
	if !(cfg.MediumBodyBytes < cfg.LongBodyBytes && cfg.LongBodyBytes < cfg.VeryLongBodyBytes) {
		return fmt.Errorf("body thresholds must be strictly increasing")
	}
	if !cfg.ClassifyOutputTokens {
		return nil
	}
	if cfg.JSONClassifyBodyBytes <= 0 {
		return fmt.Errorf("JSON_CLASSIFY_BODY_BYTES must be > 0 when CLASSIFY_OUTPUT_TOKENS=true")
	}
	if cfg.JSONClassifyLimit <= 0 {
		return fmt.Errorf("JSON_CLASSIFY_LIMIT must be > 0 when CLASSIFY_OUTPUT_TOKENS=true")
	}
	if len(cfg.OutputTokenFields) == 0 {
		return fmt.Errorf("OUTPUT_TOKEN_FIELD_NAMES must not be empty when CLASSIFY_OUTPUT_TOKENS=true")
	}
	for _, field := range cfg.OutputTokenFields {
		if !names.OutputTokenField(field) {
			return fmt.Errorf("invalid output Token field %q", field)
		}
	}
	if err := validateUniqueStrings("OUTPUT_TOKEN_FIELD_NAMES", cfg.OutputTokenFields); err != nil {
		return err
	}
	for name, value := range map[string]int{
		"MEDIUM_OUTPUT_TOKENS":    cfg.MediumOutputTokens,
		"LONG_OUTPUT_TOKENS":      cfg.LongOutputTokens,
		"VERY_LONG_OUTPUT_TOKENS": cfg.VeryLongOutputTokens,
	} {
		if value <= 0 {
			return fmt.Errorf("%s must be > 0 when CLASSIFY_OUTPUT_TOKENS=true", name)
		}
	}
	if !(cfg.MediumOutputTokens < cfg.LongOutputTokens && cfg.LongOutputTokens < cfg.VeryLongOutputTokens) {
		return fmt.Errorf("output Token thresholds must be strictly increasing")
	}
	if cfg.AdaptiveOutput {
		return validateAdaptiveOutputConfig(cfg)
	}
	return nil
}

func validateAdaptiveOutputConfig(cfg Config) error {
	if cfg.AdaptiveOutputWindow <= 0 {
		return fmt.Errorf("ADAPTIVE_OUTPUT_WINDOW must be > 0 when ADAPTIVE_OUTPUT_CLASSIFICATION=true")
	}
	if cfg.AdaptiveOutputMin < 0 {
		return fmt.Errorf("ADAPTIVE_OUTPUT_MIN_SAMPLES must be >= 0")
	}
	if cfg.AdaptiveOutputMin > cfg.AdaptiveOutputWindow {
		return fmt.Errorf("ADAPTIVE_OUTPUT_MIN_SAMPLES must be <= ADAPTIVE_OUTPUT_WINDOW")
	}
	for name, value := range map[string]float64{
		"ADAPTIVE_OUTPUT_MEDIUM_QUANTILE": cfg.AdaptiveOutputMediumQ,
		"ADAPTIVE_OUTPUT_LONG_QUANTILE":   cfg.AdaptiveOutputLongQ,
		"ADAPTIVE_OUTPUT_VERY_QUANTILE":   cfg.AdaptiveOutputVeryQ,
	} {
		if err := validateRatio(name, value); err != nil {
			return err
		}
	}
	if !(cfg.AdaptiveOutputMediumQ <= cfg.AdaptiveOutputLongQ && cfg.AdaptiveOutputLongQ <= cfg.AdaptiveOutputVeryQ) {
		return fmt.Errorf("adaptive output quantiles must be increasing")
	}
	for name, value := range map[string]float64{
		"ADAPTIVE_OUTPUT_GREEN_RELAX":  cfg.AdaptiveOutputGreen,
		"ADAPTIVE_OUTPUT_YELLOW_RELAX": cfg.AdaptiveOutputYellow,
		"ADAPTIVE_OUTPUT_RED_RELAX":    cfg.AdaptiveOutputRed,
	} {
		if err := validateRatio(name, value); err != nil {
			return err
		}
	}
	if !(cfg.AdaptiveOutputRed <= cfg.AdaptiveOutputYellow && cfg.AdaptiveOutputYellow <= cfg.AdaptiveOutputGreen) {
		return fmt.Errorf("adaptive output relax factors must satisfy red <= yellow <= green")
	}
	return nil
}
