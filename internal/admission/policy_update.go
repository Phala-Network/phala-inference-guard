package admission

import (
	"errors"
	"math"
	"time"
)

var (
	ErrPolicyInvalid          = errors.New("predictive policy update is invalid")
	ErrPolicyRevisionConflict = errors.New("predictive policy revision conflict")
	ErrPolicyUnavailable      = errors.New("predictive policy is unavailable")
)

type PolicySnapshot struct {
	Revision           uint64
	TPSReference       float64
	WindowConcurrency  int64
	RunningLimit       int64
	RunningLimitSource RunningLimitSource
	UpdatedAt          time.Time
}

type PolicyUpdate struct {
	ExpectedRevision  uint64
	TPSReference      *float64
	WindowConcurrency *int64
	RunningLimit      *int64
	UpdatedAt         time.Time
}

type PolicyUpdateResult struct {
	Policy         PolicySnapshot
	PreviousPolicy PolicySnapshot
	TPSWindowReset bool
}

func (c *AdmissionController) UpdatePolicy(update PolicyUpdate) (PolicyUpdateResult, error) {
	if c == nil {
		return PolicyUpdateResult{}, ErrPolicyUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.policySnapshotLocked()
	result := PolicyUpdateResult{Policy: current, PreviousPolicy: current}
	if !validPolicyUpdate(update) {
		return result, ErrPolicyInvalid
	}
	if c.closedReason != "" || c.policyRevision == math.MaxUint64 {
		return result, ErrPolicyUnavailable
	}
	if update.ExpectedRevision != c.policyRevision {
		return result, ErrPolicyRevisionConflict
	}

	next := current
	if update.TPSReference != nil {
		next.TPSReference = *update.TPSReference
	}
	if update.WindowConcurrency != nil {
		next.WindowConcurrency = *update.WindowConcurrency
	}
	if update.RunningLimit != nil {
		next.RunningLimit = *update.RunningLimit
		next.RunningLimitSource = RunningLimitSourceAdmin
	}
	result.TPSWindowReset = next.TPSReference != current.TPSReference
	c.policyRevision++
	c.policyUpdatedAt = update.UpdatedAt
	c.windowConcurrency = next.WindowConcurrency
	c.runningLimit = next.RunningLimit
	c.runningLimitSource = next.RunningLimitSource
	if result.TPSWindowReset {
		c.tpsPolicyEpoch++
		nextWindow := newTPSWindow(next.TPSReference)
		nextWindow.denominator = c.tpsWindow.denominator
		c.tpsWindow = nextWindow
	}
	result.Policy = c.policySnapshotLocked()
	return result, nil
}

func validPolicyUpdate(update PolicyUpdate) bool {
	if update.ExpectedRevision == 0 || update.UpdatedAt.IsZero() ||
		(update.TPSReference == nil && update.WindowConcurrency == nil && update.RunningLimit == nil) {
		return false
	}
	if update.TPSReference != nil &&
		(!finiteNonnegative(*update.TPSReference) || *update.TPSReference > 1_000_000) {
		return false
	}
	if update.WindowConcurrency != nil && *update.WindowConcurrency <= 0 {
		return false
	}
	return update.RunningLimit == nil || *update.RunningLimit >= 0
}

func (c *AdmissionController) policySnapshotLocked() PolicySnapshot {
	if c == nil {
		return PolicySnapshot{}
	}
	return PolicySnapshot{
		Revision:           c.policyRevision,
		TPSReference:       c.tpsWindow.reference,
		WindowConcurrency:  c.windowConcurrency,
		RunningLimit:       c.runningLimit,
		RunningLimitSource: c.runningLimitSource,
		UpdatedAt:          c.policyUpdatedAt,
	}
}
