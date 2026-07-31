package predictive

import (
	"fmt"
	"time"

	domain "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

type CountCoordinatorConfig struct {
	Identity    CoordinatorIdentity
	Initial     domain.VirtualState
	Constraints domain.Constraints
	Scheduler   Scheduler
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
	identity CoordinatorIdentity
	manager  *Manager
}

func NewCountCoordinator(config CountCoordinatorConfig) (*CountCoordinator, error) {
	if err := validateCoordinatorIdentity(config.Identity); err != nil {
		return nil, err
	}
	if config.Scheduler == nil {
		return nil, fmt.Errorf("count coordinator scheduler is required")
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
		identity: config.Identity,
		manager:  NewManager(config.Identity.ManifestID, config.Initial, config.Constraints, config.Scheduler),
	}, nil
}

func (c *CountCoordinator) DecideAndReserve(time.Time, CountAdmissionProposal) CountAdmissionResult {
	return countAdmissionFailure(domain.ReasonPredictorProfileUnknown)
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
