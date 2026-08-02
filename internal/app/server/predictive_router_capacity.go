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
	BackpressureActive   bool
	BackpressureApplied  bool
	PredictiveRunning    int
	RawRunning           int
	EffectiveRunning     int
	RawGlobalLimit       int
	EffectiveGlobalLimit int
}

type predictiveRouterCapacityEvent struct {
	Activation           uint64
	Reason               string
	Source               string
	Samples              int
	PredictiveRunning    int
	RawRunning           int
	EffectiveRunning     int
	RawGlobalLimit       int
	EffectiveGlobalLimit int
	ActivatedAt          time.Time
	Until                time.Time
}

type predictiveRouterCapacityLogState struct {
	loggedActivation atomic.Uint64
}

func predictiveRouterCapacity(
	mode string,
	backpressureActive bool,
	predictiveRunning int,
	snapshot runtimedynamic.Snapshot,
) predictiveRouterCapacityProjection {
	capacity := predictiveRouterCapacityProjection{
		RawRunning:           snapshot.Running,
		EffectiveRunning:     snapshot.Running,
		RawGlobalLimit:       snapshot.GlobalLimit,
		EffectiveGlobalLimit: snapshot.GlobalLimit,
	}
	if mode != "enforce" {
		return capacity
	}
	capacity.BackpressureActive = backpressureActive
	if !backpressureActive {
		return capacity
	}
	if predictiveRunning < 0 {
		predictiveRunning = 0
	}
	capacity.PredictiveRunning = predictiveRunning
	if predictiveRunning > capacity.EffectiveRunning {
		capacity.EffectiveRunning = predictiveRunning
	}
	if capacity.EffectiveRunning <= 0 {
		return capacity
	}
	capacity.BackpressureApplied = true
	capacity.EffectiveGlobalLimit = clampPredictiveRouterLimit(snapshot.GlobalLimit, capacity.EffectiveRunning)
	return capacity
}

func clampPredictiveRouterLimit(base, active int) int {
	if active <= 0 {
		return base
	}
	// Router treats a non-positive global limit as missing capacity and reports
	// zero fullness. During bounded predictive backpressure, use the active load
	// as an effective sentinel so Router sees exactly 100% fullness. The raw
	// limit and PIG's local admission limit remain unchanged.
	if base <= 0 || active < base {
		return active
	}
	return base
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
}

func predictiveRouterCapacityMetrics(capacity predictiveRouterCapacityProjection) metrics.DynamicRouterCapacity {
	return metrics.DynamicRouterCapacity{
		Present:              true,
		BackpressureActive:   capacity.BackpressureActive,
		BackpressureApplied:  capacity.BackpressureApplied,
		RawRunning:           capacity.RawRunning,
		EffectiveRunning:     capacity.EffectiveRunning,
		RawGlobalLimit:       capacity.RawGlobalLimit,
		EffectiveGlobalLimit: capacity.EffectiveGlobalLimit,
	}
}

func (s *predictiveRouterCapacityLogState) Claim(
	input metrics.PredictiveAdmissionInput,
	capacity predictiveRouterCapacityProjection,
) *predictiveRouterCapacityEvent {
	if s == nil || !capacity.BackpressureApplied || input.RouterBackpressure.Activations == 0 {
		return nil
	}
	activation := input.RouterBackpressure.Activations
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
			Reason:               input.RouterBackpressure.Reason,
			Source:               input.RouterBackpressure.Source,
			Samples:              input.RouterBackpressure.Samples,
			PredictiveRunning:    capacity.PredictiveRunning,
			RawRunning:           capacity.RawRunning,
			EffectiveRunning:     capacity.EffectiveRunning,
			RawGlobalLimit:       capacity.RawGlobalLimit,
			EffectiveGlobalLimit: capacity.EffectiveGlobalLimit,
			ActivatedAt:          input.RouterBackpressure.ActivatedAt,
			Until:                input.RouterBackpressure.Until,
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
		"predictive_router_backpressure event=router_capacity_applied mode=enforce activation=%d reason=%s source=%s samples=%d predictive_running=%d raw_running=%d effective_running=%d raw_global_limit=%d effective_global_limit=%d activated_at=%s until=%s",
		event.Activation,
		reason,
		source,
		event.Samples,
		event.PredictiveRunning,
		event.RawRunning,
		event.EffectiveRunning,
		event.RawGlobalLimit,
		event.EffectiveGlobalLimit,
		event.ActivatedAt.UTC().Format(time.RFC3339Nano),
		event.Until.UTC().Format(time.RFC3339Nano),
	)
}

func logPredictiveRouterCapacity(event predictiveRouterCapacityEvent) {
	log.Print(predictiveRouterCapacityLogLine(event))
}
