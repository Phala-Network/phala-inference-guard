package requestaware

import (
	"crypto/sha256"
	_ "embed"
	"encoding/json"
	"fmt"
)

const (
	frozenV01210SourceCommit  = "caa0138c70282f6876fe1cc4669f48703e177d35"
	frozenV01210SourceSHA256  = "c678b7a2734df0b68a8e22fa5cd9f8ad64323c3c883a2564e83cd4dea1e09075"
	frozenV01210FixtureSHA256 = "cb1a57553e3f709fd3825e01e56bf6f8eb6d6f0f30883cfa8df280f5cd16f462"
)

//go:embed testdata/v0.12.10.json
var frozenV01210JSON []byte

type frozenHistoricalPolicy struct {
	Policy            string             `json:"policy"`
	SourceCommit      string             `json:"source_commit"`
	SourceSuiteSHA256 string             `json:"source_suite_sha256"`
	Scenarios         map[string]Metrics `json:"scenarios"`
}

func loadFrozenV01210() (map[string]Metrics, error) {
	if digest := fmt.Sprintf("%x", sha256.Sum256(frozenV01210JSON)); digest != frozenV01210FixtureSHA256 {
		return nil, fmt.Errorf("frozen v0.12.10 baseline fixture hash is invalid")
	}
	var frozen frozenHistoricalPolicy
	if err := json.Unmarshal(frozenV01210JSON, &frozen); err != nil {
		return nil, fmt.Errorf("decode frozen v0.12.10 baseline: %w", err)
	}
	if frozen.Policy != string(PolicyV01210) || frozen.SourceCommit != frozenV01210SourceCommit ||
		frozen.SourceSuiteSHA256 != frozenV01210SourceSHA256 || len(frozen.Scenarios) != 36 {
		return nil, fmt.Errorf("frozen v0.12.10 baseline provenance is invalid")
	}
	for name, metrics := range frozen.Scenarios {
		if name == "" {
			return nil, fmt.Errorf("frozen v0.12.10 baseline contains an empty scenario")
		}
		if err := validateSimulationMetrics(name, PolicyV01210, metrics); err != nil {
			return nil, err
		}
	}
	return frozen.Scenarios, nil
}
