package server

import (
	"fmt"
	"log"
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/metrics"
	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
)

type predictiveRouterCapacityProjection struct {
	Activation           uint64
	Scope                predictiveProtectionScope
	BackpressureActive   bool
	BackpressureApplied  bool
	PredictiveRunning    int
	RawRunning           int
	EffectiveRunning     int
	RawWaiting           int
	EffectiveWaiting     int
	RawGlobalLimit       int
	EffectiveGlobalLimit int
	InspectCapacity      int
}

type predictiveRouterCapacityEvent struct {
	Activation           uint64
	Scope                string
	Reason               string
	Source               string
	PredictiveRunning    int
	RawRunning           int
	EffectiveRunning     int
	RawWaiting           int
	EffectiveWaiting     int
	RawGlobalLimit       int
	EffectiveGlobalLimit int
	ActivatedAt          time.Time
}

type predictiveRouterCapacityLogState struct {
	loggedActivation atomic.Uint64
}

func predictiveRouterBackpressureFromMetrics(input metrics.PredictiveRouterBackpressureInput) predictiveRouterBackpressureSnapshot {
	return predictiveRouterBackpressureSnapshot{
		Active:          input.Active,
		Activation:      input.Activation,
		Scope:           predictiveProtectionScope(input.Scope),
		MinimumRunning:  input.MinimumRunning,
		InspectCapacity: input.InspectCapacity,
	}
}

func predictiveRouterCapacity(
	mode string,
	backpressure predictiveRouterBackpressureSnapshot,
	predictiveRunning int,
	snapshot runtimedynamic.Snapshot,
) predictiveRouterCapacityProjection {
	capacity := predictiveRouterCapacityProjection{
		RawRunning:           snapshot.Running,
		EffectiveRunning:     snapshot.Running,
		RawWaiting:           snapshot.Waiting,
		EffectiveWaiting:     snapshot.Waiting,
		RawGlobalLimit:       snapshot.GlobalLimit,
		EffectiveGlobalLimit: snapshot.GlobalLimit,
	}
	if mode != "enforce" {
		return capacity
	}
	// Request-aware enforce is the only effective admission authority. Preserve
	// legacy dynamic values as raw telemetry, but do not let Router reapply the
	// old waiting/global-limit policy before request size can reach PIG.
	capacity.EffectiveWaiting = 0
	capacity.EffectiveGlobalLimit = 0
	capacity.Activation = backpressure.Activation
	capacity.Scope = backpressure.Scope
	capacity.BackpressureActive = backpressure.Active
	if !backpressure.Active {
		return capacity
	}
	if predictiveRunning < 0 {
		predictiveRunning = 0
	}
	if predictiveRunning < backpressure.MinimumRunning {
		predictiveRunning = backpressure.MinimumRunning
	}
	capacity.PredictiveRunning = predictiveRunning
	if predictiveRunning > capacity.EffectiveRunning {
		capacity.EffectiveRunning = predictiveRunning
	}
	if capacity.EffectiveRunning <= 0 {
		return capacity
	}
	capacity.BackpressureApplied = true
	capacity.EffectiveGlobalLimit = projectPredictiveRouterLimit(
		capacity.EffectiveRunning,
		backpressure.InspectCapacity,
	)
	if capacity.EffectiveGlobalLimit > capacity.EffectiveRunning {
		capacity.InspectCapacity = capacity.EffectiveGlobalLimit - capacity.EffectiveRunning
	}
	return capacity
}

func projectPredictiveRouterLimit(active, inspectCapacity int) int {
	if active <= 0 {
		return 0
	}
	if inspectCapacity < 0 {
		inspectCapacity = 0
	}
	maximumInspect := int(^uint(0)>>1) - active
	if inspectCapacity > maximumInspect {
		inspectCapacity = maximumInspect
	}
	projected := active + inspectCapacity
	return projected
}

func applyPredictiveRouterCapacity(input *metrics.PredictiveAdmissionInput, capacity predictiveRouterCapacityProjection) {
	if input == nil {
		return
	}
	input.RouterBackpressure.Applied = capacity.BackpressureApplied
	input.RouterBackpressure.PredictiveRunning = capacity.PredictiveRunning
	input.RouterBackpressure.RawRunning = capacity.RawRunning
	input.RouterBackpressure.EffectiveRunning = capacity.EffectiveRunning
	input.RouterBackpressure.RawGlobalLimit = capacity.RawGlobalLimit
	input.RouterBackpressure.EffectiveGlobalLimit = capacity.EffectiveGlobalLimit
	input.RouterBackpressure.InspectCapacity = capacity.InspectCapacity
}

func predictiveRouterCapacityMetrics(capacity predictiveRouterCapacityProjection) metrics.DynamicRouterCapacity {
	return metrics.DynamicRouterCapacity{
		Present:              true,
		BackpressureActive:   capacity.BackpressureActive,
		BackpressureApplied:  capacity.BackpressureApplied,
		RawRunning:           capacity.RawRunning,
		EffectiveRunning:     capacity.EffectiveRunning,
		RawWaiting:           capacity.RawWaiting,
		EffectiveWaiting:     capacity.EffectiveWaiting,
		RawGlobalLimit:       capacity.RawGlobalLimit,
		EffectiveGlobalLimit: capacity.EffectiveGlobalLimit,
	}
}

func (s *predictiveRouterCapacityLogState) Claim(
	input metrics.PredictiveAdmissionInput,
	capacity predictiveRouterCapacityProjection,
) *predictiveRouterCapacityEvent {
	if s == nil || !capacity.BackpressureApplied || capacity.Activation == 0 {
		return nil
	}
	activation := capacity.Activation
	for {
		logged := s.loggedActivation.Load()
		if logged >= activation {
			return nil
		}
		if !s.loggedActivation.CompareAndSwap(logged, activation) {
			continue
		}
		return &predictiveRouterCapacityEvent{
			Activation:           activation,
			Scope:                input.RouterBackpressure.Scope,
			Reason:               input.RouterBackpressure.Reason,
			Source:               input.RouterBackpressure.Source,
			PredictiveRunning:    capacity.PredictiveRunning,
			RawRunning:           capacity.RawRunning,
			EffectiveRunning:     capacity.EffectiveRunning,
			RawWaiting:           capacity.RawWaiting,
			EffectiveWaiting:     capacity.EffectiveWaiting,
			RawGlobalLimit:       capacity.RawGlobalLimit,
			EffectiveGlobalLimit: capacity.EffectiveGlobalLimit,
			ActivatedAt:          input.RouterBackpressure.ActivatedAt,
		}
	}
}

func predictiveRouterCapacityLogLine(event predictiveRouterCapacityEvent) string {
	reason := event.Reason
	if reason == "" {
		reason = "unknown"
	}
	source := event.Source
	if source == "" {
		source = "unknown"
	}
	return fmt.Sprintf(
		"predictive_router_backpressure event=router_capacity_applied mode=enforce activation=%d scope=%s reason=%s source=%s predictive_running=%d raw_running=%d effective_running=%d raw_waiting=%d effective_waiting=%d raw_global_limit=%d effective_global_limit=%d activated_at=%s",
		event.Activation,
		event.Scope,
		reason,
		source,
		event.PredictiveRunning,
		event.RawRunning,
		event.EffectiveRunning,
		event.RawWaiting,
		event.EffectiveWaiting,
		event.RawGlobalLimit,
		event.EffectiveGlobalLimit,
		event.ActivatedAt.UTC().Format(time.RFC3339Nano),
	)
}

func logPredictiveRouterCapacity(event predictiveRouterCapacityEvent) {
	log.Print(predictiveRouterCapacityLogLine(event))
}
