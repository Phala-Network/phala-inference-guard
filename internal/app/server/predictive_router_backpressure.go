package server

import (
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const requestAwareRouterRejectProjectionHold = 1500 * time.Millisecond

type predictiveRouterBackpressureSnapshot struct {
	Active          bool
	Activation      uint64
	Scope           predictiveProtectionScope
	Reason          domainpredictive.Reason
	Source          runtimepredictive.PredictionSource
	ActivatedAt     time.Time
	MinimumRunning  int
	InspectCapacity int
	Activations     uint64
	LatestRejectAt  time.Time
}

func recentRequestAwareRejectProjection(
	now time.Time,
	attempts predictiveAttemptSnapshot,
) (predictiveRouterBackpressureSnapshot, bool) {
	if attempts.LastRejectScope != predictiveProtectionScopeLoad &&
		attempts.LastRejectScope != predictiveProtectionScopeAvailability {
		return predictiveRouterBackpressureSnapshot{}, false
	}
	elapsed := now.Sub(attempts.LastRejectAt)
	if attempts.LastRejectAt.IsZero() || elapsed < 0 || elapsed >= requestAwareRouterRejectProjectionHold {
		return predictiveRouterBackpressureSnapshot{}, false
	}
	inspectCapacity := 1
	if attempts.LastRejectScope == predictiveProtectionScopeAvailability {
		inspectCapacity = 0
	}
	return predictiveRouterBackpressureSnapshot{
		Active:          true,
		Scope:           attempts.LastRejectScope,
		Reason:          attempts.LastRejectReason,
		Source:          attempts.LastRejectSource,
		MinimumRunning:  1,
		InspectCapacity: inspectCapacity,
		LatestRejectAt:  attempts.LastRejectAt,
	}, true
}
