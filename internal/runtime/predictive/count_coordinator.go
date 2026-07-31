package predictive

import (
	"fmt"
	"math"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type CountCoordinatorConfig struct {
	Identity           CoordinatorIdentity
	ModelMaximumLength int64
	Initial            domain.VirtualState
	Constraints        domain.Constraints
	Scheduler          Scheduler
}

type CountAdmissionProposal struct {
	RequestID          string
	Analysis           TokenCountAnalysis
	DecodeHorizonUpper int64
	Confidence         float64
}

type CountRequestCost struct {
	ManifestID               string
	BackendEpoch             string
	InputTokens              int64
	PhysicalKVUpper          int64
	ActiveKVUpper            int64
	UncachedPrefillUpper     int64
	DecodeHorizonUpper       int64
	DecodeSequencesUpper     int
	ActiveContextTokensUpper int64
	Confidence               float64
}

type CountAdmissionResult struct {
	Decision      domain.Decision
	Prediction    SchedulerPrediction
	HasPrediction bool
	Cost          CountRequestCost
	Reserved      bool
}

type CountCoordinatorSnapshot struct {
	Manager Snapshot
}

type CountCoordinator struct {
	identity           CoordinatorIdentity
	modelMaximumLength int64
	manager            *Manager
}

func NewCountCoordinator(config CountCoordinatorConfig) (*CountCoordinator, error) {
	if err := validateCoordinatorIdentity(config.Identity); err != nil {
		return nil, err
	}
	if config.Scheduler == nil {
		return nil, fmt.Errorf("count coordinator scheduler is required")
	}
	if config.ModelMaximumLength <= 0 {
		return nil, fmt.Errorf("count coordinator model maximum length must be positive")
	}
	if identity := config.Scheduler.Identity(); identity.Validate() != nil || identity != config.Identity.Scheduler {
		return nil, fmt.Errorf("count coordinator scheduler identity mismatch")
	}
	if err := validateInitialState(config.Initial); err != nil {
		return nil, err
	}
	if err := validateConstraints(config.Constraints); err != nil {
		return nil, err
	}
	return &CountCoordinator{
		identity:           config.Identity,
		modelMaximumLength: config.ModelMaximumLength,
		manager:            NewManager(config.Identity.ManifestID, config.Initial, config.Constraints, config.Scheduler),
	}, nil
}

func (c *CountCoordinator) DecideAndReserve(now time.Time, proposal CountAdmissionProposal) CountAdmissionResult {
	if c == nil || c.manager == nil {
		return countAdmissionFailure(domain.ReasonPredictorProfileUnknown)
	}
	if proposal.RequestID == "" || proposal.DecodeHorizonUpper < 0 || !positiveFinite(proposal.Confidence) || proposal.Confidence > 1 {
		return countAdmissionFailure(domain.ReasonPredictorProfileUnknown)
	}
	if err := proposal.Analysis.Validate(c.identity.ManifestID, c.identity.BackendEpoch); err != nil {
		return countAdmissionFailure(domain.ReasonTokenizerProfileUnknown)
	}
	if proposal.Analysis.ExactInputTokens > math.MaxInt64-proposal.DecodeHorizonUpper {
		return countAdmissionFailure(domain.ReasonPredictorProfileUnknown)
	}
	activeContext := proposal.Analysis.ExactInputTokens + proposal.DecodeHorizonUpper
	if activeContext > c.modelMaximumLength {
		return countAdmissionFailure(domain.ReasonPredictorProfileUnknown)
	}
	kvUpper, ok := roundUpCountCost(activeContext, int64(c.identity.BlockSize))
	if !ok {
		return countAdmissionFailure(domain.ReasonPredictorProfileUnknown)
	}
	cost := CountRequestCost{
		ManifestID:               c.identity.ManifestID,
		BackendEpoch:             c.identity.BackendEpoch,
		InputTokens:              proposal.Analysis.ExactInputTokens,
		PhysicalKVUpper:          kvUpper,
		ActiveKVUpper:            kvUpper,
		UncachedPrefillUpper:     proposal.Analysis.ExactInputTokens,
		DecodeHorizonUpper:       proposal.DecodeHorizonUpper,
		DecodeSequencesUpper:     1,
		ActiveContextTokensUpper: activeContext,
		Confidence:               proposal.Confidence,
	}
	managerResult := c.manager.decideAndReserve(now, proposal.RequestID, cost.managerCost())
	return CountAdmissionResult{
		Decision:      managerResult.Decision,
		Prediction:    managerResult.Prediction,
		HasPrediction: managerResult.HasPrediction,
		Cost:          cost,
		Reserved:      managerResult.Decision.Reason == domain.ReasonFit,
	}
}

func (c *CountCoordinator) MarkPrefillComplete(requestID string) bool {
	return c != nil && c.manager.MarkPrefillComplete(requestID)
}

func (c *CountCoordinator) Complete(requestID string) bool {
	return c.Terminate(requestID, TerminalCompleted)
}

func (c *CountCoordinator) Terminate(requestID string, cause TerminalCause) bool {
	return c != nil && c.manager.Terminate(requestID, cause)
}

func (c *CountCoordinator) EventSequence() uint64 {
	if c == nil {
		return 0
	}
	return c.manager.EventSequence()
}

func (c *CountCoordinator) ReconcileSample(sample SampleWindow) error {
	if c == nil {
		return fmt.Errorf("count coordinator is nil")
	}
	return c.manager.ReconcileSample(sample)
}

func (c *CountCoordinator) ObserveOutcome(requestID string, outcome SchedulerOutcome) bool {
	return c != nil && c.manager.ObserveOutcome(requestID, outcome)
}

func (c *CountCoordinator) Snapshot() CountCoordinatorSnapshot {
	if c == nil {
		return CountCoordinatorSnapshot{}
	}
	return CountCoordinatorSnapshot{Manager: c.manager.Snapshot()}
}

func countAdmissionFailure(reason domain.Reason) CountAdmissionResult {
	return CountAdmissionResult{Decision: domain.Decision{Reason: reason}}
}

func (c CountRequestCost) managerCost() domain.RequestCost {
	return domain.RequestCost{
		ManifestID:  c.ManifestID,
		InputTokens: c.InputTokens,
		KV: domain.KVIncrement{
			PhysicalKVUpper: c.PhysicalKVUpper,
			ActiveKVUpper:   c.ActiveKVUpper,
		},
		UncachedPrefillUpper:     c.UncachedPrefillUpper,
		DecodeHorizonUpper:       c.DecodeHorizonUpper,
		DecodeSequencesUpper:     c.DecodeSequencesUpper,
		ActiveContextTokensUpper: c.ActiveContextTokensUpper,
		Confidence:               c.Confidence,
	}
}

func roundUpCountCost(value, blockSize int64) (int64, bool) {
	if value <= 0 {
		return 0, true
	}
	if blockSize <= 0 || value > math.MaxInt64-(blockSize-1) {
		return 0, false
	}
	return ((value + blockSize - 1) / blockSize) * blockSize, true
}
