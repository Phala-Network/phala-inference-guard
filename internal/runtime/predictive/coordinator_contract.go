package predictive

import (
	"fmt"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type CoordinatorIdentity struct {
	ManifestID   string
	BackendEpoch string
	Scheduler    ModelIdentity
	BlockSize    int
}

func validateCoordinatorIdentity(identity CoordinatorIdentity) error {
	if identity.ManifestID == "" {
		return fmt.Errorf("predictive coordinator manifest id is required")
	}
	if identity.BackendEpoch == "" {
		return fmt.Errorf("predictive coordinator backend epoch is required")
	}
	if identity.BlockSize <= 0 {
		return fmt.Errorf("predictive coordinator block size must be positive")
	}
	if err := identity.Scheduler.Validate(); err != nil {
		return err
	}
	if identity.Scheduler.BackendEpoch != identity.BackendEpoch {
		return fmt.Errorf("predictive coordinator backend and scheduler epochs differ")
	}
	return nil
}

func validateInitialState(state domain.VirtualState) error {
	if state.PhysicalKVUpper < 0 || state.ActiveKVUpper < 0 || state.DecodeSequences < 0 || state.ActiveContextTokens < 0 || state.UncachedPrefillTokens < 0 {
		return fmt.Errorf("predictive coordinator initial state must be non-negative")
	}
	return nil
}

func validateConstraints(constraints domain.Constraints) error {
	if constraints.PhysicalKVHard < 0 || constraints.ActiveKVHard < 0 || !nonNegativeFinite(constraints.UserTPSTarget) || constraints.TPOTSLO < 0 || !nonNegativeFinite(constraints.WorkspaceRiskBudget) || !nonNegativeFinite(constraints.PreemptionRiskBudget) || !nonNegativeFinite(constraints.MinimumConfidence) || constraints.MinimumConfidence > 1 {
		return fmt.Errorf("predictive coordinator constraints are invalid")
	}
	return nil
}
