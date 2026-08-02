package server

import (
	"fmt"
	"log"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	minimumPredictiveRouterBackpressureHold = 2 * time.Second
	maximumPredictiveRouterBackpressureHold = 5 * time.Second
)

type predictiveRouterBackpressureEvent struct {
	Reason          domainpredictive.Reason
	Source          runtimepredictive.PredictionSource
	Samples         int
	VirtualDecode   int
	VirtualActiveKV int64
	Hold            time.Duration
	ActivatedAt     time.Time
	Until           time.Time
}

type predictiveRouterBackpressurePolicy struct {
	PhysicalKVHard int64
	ActiveKVHard   int64
}

type predictiveRouterBackpressureSnapshot struct {
	Active      bool
	Reason      domainpredictive.Reason
	Source      runtimepredictive.PredictionSource
	Samples     int
	ActivatedAt time.Time
	Until       time.Time
	Hold        time.Duration
	Activations uint64
	Extensions  uint64
}

type predictiveRouterBackpressureState struct {
	reason      domainpredictive.Reason
	source      runtimepredictive.PredictionSource
	samples     int
	activatedAt time.Time
	until       time.Time
	hold        time.Duration
	activations uint64
	extensions  uint64
}

func predictiveRouterBackpressureHold(pollInterval time.Duration) time.Duration {
	return normalizePredictiveRouterBackpressureHold(2 * pollInterval)
}

func normalizePredictiveRouterBackpressureHold(hold time.Duration) time.Duration {
	if hold < minimumPredictiveRouterBackpressureHold {
		hold = minimumPredictiveRouterBackpressureHold
	}
	if hold > maximumPredictiveRouterBackpressureHold {
		hold = maximumPredictiveRouterBackpressureHold
	}
	return hold
}

func (s *predictiveRouterBackpressureState) Observe(
	now time.Time,
	hold time.Duration,
	result runtimepredictive.CountAdmissionResult,
	policy predictiveRouterBackpressurePolicy,
) *predictiveRouterBackpressureEvent {
	if s == nil || result.Reserved || !predictiveReasonCreatesRouterBackpressure(result, policy) {
		return nil
	}
	hold = normalizePredictiveRouterBackpressureHold(hold)
	active := now.Before(s.until)
	if active {
		s.extensions++
		s.reason = result.Decision.Reason
		s.source = result.Prediction.Source
		s.samples = result.Prediction.Samples
		return nil
	}
	s.activations++
	s.activatedAt = now
	s.reason = result.Decision.Reason
	s.source = result.Prediction.Source
	s.samples = result.Prediction.Samples
	s.until = now.Add(hold)
	s.hold = hold
	return &predictiveRouterBackpressureEvent{
		Reason:          s.reason,
		Source:          s.source,
		Samples:         s.samples,
		VirtualDecode:   result.Prediction.Features.ExistingDecodeSequences,
		VirtualActiveKV: result.Prediction.Features.ExistingActiveKVUpper,
		Hold:            hold,
		ActivatedAt:     now,
		Until:           s.until,
	}
}

func (s predictiveRouterBackpressureState) Snapshot(now time.Time) predictiveRouterBackpressureSnapshot {
	active := !s.until.IsZero() && now.Before(s.until)
	snapshot := predictiveRouterBackpressureSnapshot{
		Active:      active,
		ActivatedAt: s.activatedAt,
		Until:       s.until,
		Hold:        s.hold,
		Activations: s.activations,
		Extensions:  s.extensions,
	}
	if active {
		snapshot.Reason = s.reason
		snapshot.Source = s.source
		snapshot.Samples = s.samples
	}
	return snapshot
}

func predictiveReasonCreatesRouterBackpressure(result runtimepredictive.CountAdmissionResult, policy predictiveRouterBackpressurePolicy) bool {
	// vLLM KV usage may remain non-zero for prefix-cache or scrape-reconciliation
	// reasons after active work reaches zero. The scheduler feature is captured
	// atomically from observed running/waiting plus live reservations during the
	// rejected decision, so it is the authoritative existing-load signal.
	if result.Prediction.Features.ExistingDecodeSequences <= 0 {
		return false
	}
	switch result.Decision.Reason {
	case domainpredictive.ReasonKVOverBudget:
		return predictiveRequestFitsKVLimit(result, result.Cost.PhysicalKVUpper, policy.PhysicalKVHard)
	case domainpredictive.ReasonActiveKVOverBudget:
		return predictiveRequestFitsKVLimit(result, result.Cost.ActiveKVUpper, policy.ActiveKVHard)
	case domainpredictive.ReasonExistingTPSAtRisk,
		domainpredictive.ReasonNewTPSAtRisk,
		domainpredictive.ReasonTPOTAtRisk,
		domainpredictive.ReasonWorkspaceAtRisk,
		domainpredictive.ReasonPreemptionAtRisk:
		return true
	default:
		return false
	}
}

func predictiveRequestFitsKVLimit(result runtimepredictive.CountAdmissionResult, requestKV, hardLimit int64) bool {
	return result.Cost.ManifestID != "" && result.Cost.BackendEpoch != "" &&
		requestKV >= 0 && hardLimit > 0 && requestKV <= hardLimit
}

func predictiveRouterBackpressureLogLine(event predictiveRouterBackpressureEvent) string {
	return fmt.Sprintf(
		"predictive_router_backpressure event=activated mode=enforce reason=%s source=%s samples=%d virtual_decode=%d virtual_active_kv=%d hold=%s activated_at=%s until=%s",
		event.Reason,
		event.Source,
		event.Samples,
		event.VirtualDecode,
		event.VirtualActiveKV,
		event.Hold,
		event.ActivatedAt.UTC().Format(time.RFC3339Nano),
		event.Until.UTC().Format(time.RFC3339Nano),
	)
}

func logPredictiveRouterBackpressure(event predictiveRouterBackpressureEvent) {
	log.Print(predictiveRouterBackpressureLogLine(event))
}
