package server

import (
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/observability/metrics"
)

type admissionCapacityProjection struct {
	Active          bool
	Scope           coreadmission.ProtectionScope
	Reason          coreadmission.Reason
	Source          string
	MinimumRunning  int64
	InspectCapacity int64
	LatestRejectAt  time.Time
}

func projectAdmissionCapacity(
	mode string,
	capacity coreadmission.CapacitySnapshot,
	report admissionReportSnapshot,
) admissionCapacityProjection {
	projection := admissionCapacityProjection{
		Scope:          capacity.MinimumDecision.Scope,
		Reason:         capacity.MinimumDecision.Reason,
		Source:         admissionDecisionSource(capacity.MinimumDecision),
		LatestRejectAt: report.LastRejectAt,
	}
	if mode != "enforce" || capacity.Available {
		return projection
	}
	projection.Active = true
	projection.MinimumRunning = 1
	return projection
}

func projectRouterCompatibility(
	mode string,
	capacity coreadmission.CapacitySnapshot,
	projection admissionCapacityProjection,
) metrics.RouterCapacityCompatibility {
	running := nonnegativeInt(capacity.State.RawRunning)
	waiting := nonnegativeInt(capacity.State.RawWaiting)
	value := metrics.RouterCapacityCompatibility{
		ObservedRunningRaw: running,
		ObservedWaitingRaw: waiting,
		ObservedRunning:    running,
		ObservedWaiting:    waiting,
	}
	if mode != "enforce" {
		return value
	}
	value.ObservedWaiting = 0
	if !projection.Active {
		return value
	}
	minimumRunning := nonnegativeInt(projection.MinimumRunning)
	if value.ObservedRunning < minimumRunning {
		value.ObservedRunning = minimumRunning
	}
	if value.ObservedRunning < 1 {
		value.ObservedRunning = 1
	}
	inspect := nonnegativeInt(projection.InspectCapacity)
	value.GlobalLimitRaw = running + inspect
	value.GlobalLimit = value.ObservedRunning + inspect
	return value
}

func admissionDecisionSource(decision coreadmission.DecisionRecord) string {
	if decision.Scope == coreadmission.ProtectionAvailability {
		return "unavailable"
	}
	return "deterministic"
}

func nonnegativeInt(value int64) int {
	if value <= 0 {
		return 0
	}
	maximum := int64(^uint(0) >> 1)
	if value > maximum {
		return int(maximum)
	}
	return int(value)
}
