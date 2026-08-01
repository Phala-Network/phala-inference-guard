package predictive

import (
	"fmt"
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

type CountAdmissionResult struct {
	Decision   domain.Decision
	Prediction SchedulerPrediction
	Cost       CountRequestCost
	Reserved   bool
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
	cost, reason := buildCountRequestCost(c.identity, c.modelMaximumLength, proposal)
	if reason != domain.ReasonFit {
		return countAdmissionFailure(reason)
	}
	managerResult := c.manager.decideAndReserve(now, proposal.RequestID, cost.managerCost())
	return CountAdmissionResult{
		Decision:   managerResult.Decision,
		Prediction: managerResult.Prediction,
		Cost:       cost,
		Reserved:   managerResult.Decision.Reason == domain.ReasonFit,
	}
}

func (c *CountCoordinator) DecideUpperBoundAndReserve(now time.Time, proposal UpperBoundAdmissionProposal) CountAdmissionResult {
	if c == nil || c.manager == nil {
		return countAdmissionFailure(domain.ReasonPredictorProfileUnknown)
	}
	cost, reason := buildUpperBoundRequestCost(c.identity, c.modelMaximumLength, proposal)
	if reason != domain.ReasonFit {
		return countAdmissionFailure(reason)
	}
	managerResult := c.manager.decideAndReserve(now, proposal.RequestID, cost.managerCost())
	return CountAdmissionResult{
		Decision:   managerResult.Decision,
		Prediction: managerResult.Prediction,
		Cost:       cost,
		Reserved:   managerResult.Decision.Reason == domain.ReasonFit,
	}
}

func (c *CountCoordinator) MarkPrefillComplete(requestID string) bool {
	return c != nil && c.manager.MarkPrefillComplete(requestID)
}

func (c *CountCoordinator) MarkForwarded(requestID string) bool {
	return c != nil && c.manager.MarkForwarded(requestID)
}

func (c *CountCoordinator) Complete(requestID string) bool {
	return c.Terminate(requestID, TerminalCompleted)
}

func (c *CountCoordinator) Terminate(requestID string, cause TerminalCause) bool {
	return c != nil && c.manager.Terminate(requestID, cause)
}

func (c *CountCoordinator) TerminateWithOutcome(requestID string, cause TerminalCause, outcome *SchedulerOutcome) bool {
	return c != nil && c.manager.TerminateWithOutcome(requestID, cause, outcome)
}

func (c *CountCoordinator) EventSequence() uint64 {
	if c == nil {
		return 0
	}
	return c.manager.EventSequence()
}

func (c *CountCoordinator) StartSampleWindow() uint64 {
	if c == nil || c.manager == nil {
		return 0
	}
	return c.manager.StartSampleWindow()
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

func (c *CountCoordinator) ObserveUnreservedOutcome(prediction SchedulerPrediction, cause TerminalCause, forwarded bool, outcome SchedulerOutcome) bool {
	return c != nil && c.manager.ObserveUnreservedOutcome(prediction, cause, forwarded, outcome)
}

func (c *CountCoordinator) MarkLiveOutcomesInterfered() int {
	if c == nil || c.manager == nil {
		return 0
	}
	return c.manager.MarkLiveOutcomesInterfered()
}

func (c *CountCoordinator) InvalidateLearning() {
	if c != nil && c.manager != nil {
		c.manager.InvalidateLearning()
	}
}

func (c *CountCoordinator) InvalidateEpoch() bool {
	return c != nil && c.manager != nil && c.manager.InvalidateEpoch()
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
