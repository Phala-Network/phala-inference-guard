package pigconfig

import "github.com/Phala-Network/phala-inference-guard/internal/infra/env"

func loadBaseConfig(cfg *Config) error {
	globalLimit, err := env.Int("GLOBAL_LIMIT", 512)
	if err != nil {
		return err
	}
	pathSuffixMatch, err := env.Bool("PIG_PATH_SUFFIX_MATCH", false)
	if err != nil {
		return err
	}
	cfg.GlobalLimit = globalLimit
	cfg.PathSuffixMatch = pathSuffixMatch
	return nil
}
