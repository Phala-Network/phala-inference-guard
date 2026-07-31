package predictive

import "fmt"

type TokenCountAnalysis struct {
	ManifestID       string
	BackendEpoch     string
	ExactInputTokens int64
}

func (a TokenCountAnalysis) Validate(manifestID, backendEpoch string) error {
	if manifestID == "" || a.ManifestID != manifestID {
		return fmt.Errorf("token count analysis manifest does not match")
	}
	if backendEpoch == "" || a.BackendEpoch != backendEpoch {
		return fmt.Errorf("token count analysis backend epoch does not match")
	}
	if a.ExactInputTokens < 0 {
		return fmt.Errorf("token count analysis input tokens must be non-negative")
	}
	return nil
}
