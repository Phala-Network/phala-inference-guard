package pigconfig

import "github.com/Phala-Network/phala-inference-guard/internal/infra/env"

func Load() (Config, error) {
	cfg := Config{
		Listen:            env.String("LISTEN", ":8000"),
		Token:             env.String("TOKEN", ""),
		QoSPaths:          env.CSV("PIG_PATHS", "/v1/chat/completions,/v1/completions,/v1/responses"),
		OutputTokenFields: env.CSV("OUTPUT_TOKEN_FIELD_NAMES", "max_tokens,max_completion_tokens,max_output_tokens"),
	}
	if err := loadBaseConfig(&cfg); err != nil {
		return Config{}, err
	}
	if err := loadOpenAIConfig(&cfg); err != nil {
		return Config{}, err
	}
	if err := loadClassifierConfig(&cfg); err != nil {
		return Config{}, err
	}
	if err := loadPriorityConfig(&cfg); err != nil {
		return Config{}, err
	}
	if err := loadRuntimeConfig(&cfg); err != nil {
		return Config{}, err
	}
	loadBackendRoutingConfig(&cfg)
	if err := loadDynamicConfig(&cfg); err != nil {
		return Config{}, err
	}
	if err := loadSSEConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
