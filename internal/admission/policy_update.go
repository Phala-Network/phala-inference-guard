package admission

import (
	"errors"
	"math"
	"time"
)

var (
	ErrTPSPolicyInvalid          = errors.New("TPS policy update is invalid")
	ErrTPSPolicyRevisionConflict = errors.New("TPS policy revision conflict")
	ErrTPSPolicyUnavailable      = errors.New("TPS policy is unavailable")
)

type TPSPolicySnapshot struct {
	Revision  uint64
	Reference float64
	UpdatedAt time.Time
}

type TPSPolicyUpdate struct {
	ExpectedRevision uint64
	Reference        float64
	UpdatedAt        time.Time
}

type TPSPolicyUpdateResult struct {
	Policy            TPSPolicySnapshot
	PreviousReference float64
	WindowReset       bool
}

func (c *AdmissionController) UpdateTPSPolicy(update TPSPolicyUpdate) (TPSPolicyUpdateResult, error) {
	if c == nil {
		return TPSPolicyUpdateResult{}, ErrTPSPolicyUnavailable
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	current := c.tpsPolicySnapshotLocked()
	result := TPSPolicyUpdateResult{
		Policy:            current,
		PreviousReference: current.Reference,
	}
	if update.ExpectedRevision == 0 || !finiteNonnegative(update.Reference) ||
		update.Reference > 1_000_000 || update.UpdatedAt.IsZero() {
		return result, ErrTPSPolicyInvalid
	}
	if c.closedReason != "" || c.policyRevision == math.MaxUint64 {
		return result, ErrTPSPolicyUnavailable
	}
	if update.ExpectedRevision != c.policyRevision {
		return result, ErrTPSPolicyRevisionConflict
	}

	result.WindowReset = update.Reference != current.Reference
	c.policyRevision++
	c.policyUpdatedAt = update.UpdatedAt
	if result.WindowReset {
		c.tpsPolicyEpoch++
		nextWindow := newTPSWindow(update.Reference)
		nextWindow.denominator = c.tpsWindow.denominator
		c.tpsWindow = nextWindow
	}
	result.Policy = c.tpsPolicySnapshotLocked()
	return result, nil
}

func (c *AdmissionController) tpsPolicySnapshotLocked() TPSPolicySnapshot {
	if c == nil {
		return TPSPolicySnapshot{}
	}
	return TPSPolicySnapshot{
		Revision:  c.policyRevision,
		Reference: c.tpsWindow.reference,
		UpdatedAt: c.policyUpdatedAt,
	}
}
