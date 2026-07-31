package pigconfig

import (
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

func loadPredictiveAdmissionConfig(cfg *Config) {
	cfg.PredictiveAdmissionMode = strings.ToLower(strings.TrimSpace(env.String("PREDICTIVE_ADMISSION_MODE", "off")))
}
