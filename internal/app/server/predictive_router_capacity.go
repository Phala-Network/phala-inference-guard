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
	RawGlobalLimit       int
	EffectiveGlobalLimit int
}

type predictiveRouterCapacityEvent struct {
	Activation                             uint64
	Scope                                  string
	Reason                                 string
	Source                                 string
	Samples                                int
	AggregateCompletionTPSEstimate         float64
	PreviousAggregateCompletionTPSEstimate float64
	PredictiveRunning                      int
	RawRunning                             int
	EffectiveRunning                       int
	RawGlobalLimit                         int
	EffectiveGlobalLimit                   int
	ActivatedAt                            time.Time
	Until                                  time.Time
}

type predictiveRouterCapacityLogState struct {
	loggedActivation atomic.Uint64
}

func predictiveRouterBackpressureFromMetrics(input metrics.PredictiveRouterBackpressureInput) predictiveRouterBackpressureSnapshot {
	return predictiveRouterBackpressureSnapshot{
		Active:         input.Active,
		Activation:     input.Activation,
		Scope:          predictiveProtectionScope(input.Scope),
		MinimumRunning: input.MinimumRunning,
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
		RawGlobalLimit:       snapshot.GlobalLimit,
		EffectiveGlobalLimit: snapshot.GlobalLimit,
	}
	if mode != "enforce" {
		return capacity
	}
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
			Activation:                             activation,
			Scope:                                  input.RouterBackpressure.Scope,
			Reason:                                 input.RouterBackpressure.Reason,
			Source:                                 input.RouterBackpressure.Source,
			Samples:                                input.RouterBackpressure.Samples,
			AggregateCompletionTPSEstimate:         input.RouterBackpressure.AggregateCompletionTPSEstimate,
			PreviousAggregateCompletionTPSEstimate: input.RouterBackpressure.PreviousAggregateCompletionTPSEstimate,
			PredictiveRunning:                      capacity.PredictiveRunning,
			RawRunning:                             capacity.RawRunning,
			EffectiveRunning:                       capacity.EffectiveRunning,
			RawGlobalLimit:                         capacity.RawGlobalLimit,
			EffectiveGlobalLimit:                   capacity.EffectiveGlobalLimit,
			ActivatedAt:                            input.RouterBackpressure.ActivatedAt,
			Until:                                  input.RouterBackpressure.Until,
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
		"predictive_router_backpressure event=router_capacity_applied mode=enforce activation=%d scope=%s reason=%s source=%s samples=%d aggregate_completion_tps_estimate=%.6f previous_aggregate_completion_tps_estimate=%.6f predictive_running=%d raw_running=%d effective_running=%d raw_global_limit=%d effective_global_limit=%d activated_at=%s until=%s",
		event.Activation,
		event.Scope,
		reason,
		source,
		event.Samples,
		event.AggregateCompletionTPSEstimate,
		event.PreviousAggregateCompletionTPSEstimate,
		event.PredictiveRunning,
		event.RawRunning,
		event.EffectiveRunning,
		event.RawGlobalLimit,
		event.EffectiveGlobalLimit,
		event.ActivatedAt.UTC().Format(time.RFC3339Nano),
		predictiveRouterBackpressureTime(event.Until),
	)
}

func logPredictiveRouterCapacity(event predictiveRouterCapacityEvent) {
	log.Print(predictiveRouterCapacityLogLine(event))
}
