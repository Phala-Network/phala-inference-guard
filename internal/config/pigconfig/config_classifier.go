package pigconfig

import "github.com/Phala-Network/phala-inference-guard/internal/infra/env"

func loadClassifierConfig(cfg *Config) error {
	classifyOutputTokens, err := env.Bool("CLASSIFY_OUTPUT_TOKENS", false)
	if err != nil {
		return err
	}
	jsonClassifyBodyBytes, err := env.Int("JSON_CLASSIFY_BODY_BYTES", 2*1024*1024)
	if err != nil {
		return err
	}
	jsonClassifyLimit, err := env.Int("JSON_CLASSIFY_LIMIT", defaultClassifierLimit(cfg.GlobalLimit))
	if err != nil {
		return err
	}
	mediumBodyBytes, err := env.Int("MEDIUM_BODY_BYTES", 60000)
	if err != nil {
		return err
	}
	longBodyBytes, err := env.Int("LONG_BODY_BYTES", 100000)
	if err != nil {
		return err
	}
	veryLongBodyBytes, err := env.Int("VERY_LONG_BODY_BYTES", 524288)
	if err != nil {
		return err
	}
	mediumOutputTokens, err := env.Int("MEDIUM_OUTPUT_TOKENS", 1024)
	if err != nil {
		return err
	}
	longOutputTokens, err := env.Int("LONG_OUTPUT_TOKENS", 4096)
	if err != nil {
		return err
	}
	veryLongOutputTokens, err := env.Int("VERY_LONG_OUTPUT_TOKENS", 8192)
	if err != nil {
		return err
	}
	adaptiveOutput, err := env.Bool("ADAPTIVE_OUTPUT_CLASSIFICATION", false)
	if err != nil {
		return err
	}
	adaptiveOutputWindow, err := env.Int("ADAPTIVE_OUTPUT_WINDOW", 512)
	if err != nil {
		return err
	}
	adaptiveOutputMin, err := env.Int("ADAPTIVE_OUTPUT_MIN_SAMPLES", 32)
	if err != nil {
		return err
	}
	adaptiveOutputMediumQ, err := env.Float("ADAPTIVE_OUTPUT_MEDIUM_QUANTILE", 0.50)
	if err != nil {
		return err
	}
	adaptiveOutputLongQ, err := env.Float("ADAPTIVE_OUTPUT_LONG_QUANTILE", 0.90)
	if err != nil {
		return err
	}
	adaptiveOutputVeryQ, err := env.Float("ADAPTIVE_OUTPUT_VERY_QUANTILE", 0.99)
	if err != nil {
		return err
	}
	adaptiveOutputGreen, err := env.Float("ADAPTIVE_OUTPUT_GREEN_RELAX", 1.0)
	if err != nil {
		return err
	}
	adaptiveOutputYellow, err := env.Float("ADAPTIVE_OUTPUT_YELLOW_RELAX", 0.5)
	if err != nil {
		return err
	}
	adaptiveOutputRed, err := env.Float("ADAPTIVE_OUTPUT_RED_RELAX", 0.0)
	if err != nil {
		return err
	}
	cfg.ClassifyOutputTokens = classifyOutputTokens
	cfg.JSONClassifyBodyBytes = int64(jsonClassifyBodyBytes)
	cfg.JSONClassifyLimit = jsonClassifyLimit
	cfg.MediumBodyBytes = int64(mediumBodyBytes)
	cfg.LongBodyBytes = int64(longBodyBytes)
	cfg.VeryLongBodyBytes = int64(veryLongBodyBytes)
	cfg.MediumOutputTokens = mediumOutputTokens
	cfg.LongOutputTokens = longOutputTokens
	cfg.VeryLongOutputTokens = veryLongOutputTokens
	cfg.AdaptiveOutput = adaptiveOutput
	cfg.AdaptiveOutputWindow = adaptiveOutputWindow
	cfg.AdaptiveOutputMin = adaptiveOutputMin
	cfg.AdaptiveOutputMediumQ = adaptiveOutputMediumQ
	cfg.AdaptiveOutputLongQ = adaptiveOutputLongQ
	cfg.AdaptiveOutputVeryQ = adaptiveOutputVeryQ
	cfg.AdaptiveOutputGreen = adaptiveOutputGreen
	cfg.AdaptiveOutputYellow = adaptiveOutputYellow
	cfg.AdaptiveOutputRed = adaptiveOutputRed
	return nil
}
