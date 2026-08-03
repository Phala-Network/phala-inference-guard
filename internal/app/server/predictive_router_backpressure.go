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
	maximumPredictiveRouterBackpressureHold = 30 * time.Second
)

type predictiveRouterBackpressureEventKind string

const (
	predictiveRouterBackpressureActivated predictiveRouterBackpressureEventKind = "activated"
	predictiveRouterBackpressureRenewed   predictiveRouterBackpressureEventKind = "renewed"
)

type predictiveRouterBackpressureEvent struct {
	Kind                 predictiveRouterBackpressureEventKind
	Activation           uint64
	Scope                predictiveProtectionScope
	Reason               domainpredictive.Reason
	Source               runtimepredictive.PredictionSource
	Samples              int
	Exploratory          bool
	AggregateTPS         float64
	PreviousAggregateTPS float64
	VirtualDecode        int
	VirtualActiveKV      int64
	Hold                 time.Duration
	ActivatedAt          time.Time
	RejectedAt           time.Time
	Until                time.Time
	Extensions           uint64
	Suppressed           uint64
}

type predictiveRouterBackpressurePolicy struct {
	PhysicalKVHard int64
	ActiveKVHard   int64
}

type predictiveRequestRejectEvent struct {
	Phase                string
	Reason               domainpredictive.Reason
	Source               runtimepredictive.PredictionSource
	Samples              int
	Exploratory          bool
	AggregateTPS         float64
	PreviousAggregateTPS float64
	Scope                predictiveProtectionScope
	RejectedAt           time.Time
	Suppressed           uint64
}

type predictiveRequestRejectLogState struct {
	buckets [5]predictiveRequestRejectLogBucket
}

type predictiveRequestRejectLogBucket struct {
	lastLoggedAt time.Time
	lastReason   domainpredictive.Reason
	suppressed   uint64
}

type predictiveRouterBackpressureSnapshot struct {
	Active               bool
	Activation           uint64
	Scope                predictiveProtectionScope
	Reason               domainpredictive.Reason
	Source               runtimepredictive.PredictionSource
	Samples              int
	Exploratory          bool
	AggregateTPS         float64
	PreviousAggregateTPS float64
	ActivatedAt          time.Time
	Until                time.Time
	Hold                 time.Duration
	MinimumRunning       int
	Activations          uint64
	Extensions           uint64
	LatestRejectAt       time.Time
	RenewalLogs          uint64
	RenewalsSuppressed   uint64
}

type predictiveRouterBackpressureState struct {
	reason                 domainpredictive.Reason
	source                 runtimepredictive.PredictionSource
	samples                int
	exploratory            bool
	aggregateTPS           float64
	previousAggregateTPS   float64
	virtualDecode          int
	virtualActiveKV        int64
	activatedAt            time.Time
	until                  time.Time
	latestRejectAt         time.Time
	hold                   time.Duration
	activation             uint64
	availabilityActive     bool
	availabilityReason     domainpredictive.Reason
	availabilityActivated  time.Time
	availabilityActivation uint64
	activations            uint64
	extensions             uint64
	lastRenewalLoggedAt    time.Time
	renewalsPendingLog     uint64
	renewalLogs            uint64
	renewalsSuppressed     uint64
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
		if now.After(s.latestRejectAt) {
			s.latestRejectAt = now
		}
		candidateUntil := s.latestRejectAt.Add(hold)
		if candidateUntil.After(s.until) {
			s.until = candidateUntil
		}
		// Keep the activation identity and its original reason immutable for the
		// episode, but expose the latest typed reject in the bounded renewal log.
		// The adapter's durable last-reject metrics carry the same latest reason.
		if !s.lastRenewalLoggedAt.IsZero() && now.Before(s.lastRenewalLoggedAt.Add(hold)) {
			s.renewalsPendingLog++
			s.renewalsSuppressed++
			return nil
		}
		event := &predictiveRouterBackpressureEvent{
			Kind:                 predictiveRouterBackpressureRenewed,
			Activation:           s.activation,
			Scope:                predictiveProtectionScopeLoad,
			Reason:               result.Decision.Reason,
			Source:               result.Prediction.Source,
			Samples:              result.Prediction.Samples,
			Exploratory:          result.Prediction.Exploratory,
			AggregateTPS:         result.Prediction.Estimate.AggregateCompletionTPSEstimate,
			PreviousAggregateTPS: result.Prediction.Estimate.PreviousAggregateCompletionTPSEstimate,
			VirtualDecode:        result.Prediction.Features.ExistingDecodeSequences,
			VirtualActiveKV:      result.Prediction.Features.ExistingActiveKVUpper,
			Hold:                 hold,
			ActivatedAt:          s.activatedAt,
			RejectedAt:           s.latestRejectAt,
			Until:                s.until,
			Extensions:           s.extensions,
			Suppressed:           s.renewalsPendingLog,
		}
		s.lastRenewalLoggedAt = now
		s.renewalsPendingLog = 0
		s.renewalLogs++
		return event
	}
	s.activations++
	s.activation = s.activations
	s.activatedAt = now
	s.reason = result.Decision.Reason
	s.source = result.Prediction.Source
	s.samples = result.Prediction.Samples
	s.exploratory = result.Prediction.Exploratory
	s.aggregateTPS = result.Prediction.Estimate.AggregateCompletionTPSEstimate
	s.previousAggregateTPS = result.Prediction.Estimate.PreviousAggregateCompletionTPSEstimate
	s.virtualDecode = result.Prediction.Features.ExistingDecodeSequences
	s.virtualActiveKV = result.Prediction.Features.ExistingActiveKVUpper
	s.until = now.Add(hold)
	s.latestRejectAt = now
	s.hold = hold
	s.lastRenewalLoggedAt = time.Time{}
	s.renewalsPendingLog = 0
	return &predictiveRouterBackpressureEvent{
		Kind:                 predictiveRouterBackpressureActivated,
		Activation:           s.activation,
		Scope:                predictiveProtectionScopeLoad,
		Reason:               s.reason,
		Source:               s.source,
		Samples:              s.samples,
		Exploratory:          s.exploratory,
		AggregateTPS:         s.aggregateTPS,
		PreviousAggregateTPS: s.previousAggregateTPS,
		VirtualDecode:        s.virtualDecode,
		VirtualActiveKV:      s.virtualActiveKV,
		Hold:                 hold,
		ActivatedAt:          now,
		RejectedAt:           now,
		Until:                s.until,
	}
}

func (s *predictiveRouterBackpressureState) SetAvailability(now time.Time, unavailable bool) *predictiveRouterBackpressureEvent {
	if s == nil {
		return nil
	}
	if !unavailable {
		wasActive := s.availabilityActive
		s.availabilityActive = false
		s.availabilityReason = ""
		s.availabilityActivated = time.Time{}
		s.availabilityActivation = 0
		if wasActive && now.Before(s.until) {
			s.activations++
			s.activation = s.activations
			s.activatedAt = now
			s.lastRenewalLoggedAt = time.Time{}
			s.renewalsPendingLog = 0
			return &predictiveRouterBackpressureEvent{
				Kind:                 predictiveRouterBackpressureActivated,
				Activation:           s.activation,
				Scope:                predictiveProtectionScopeLoad,
				Reason:               s.reason,
				Source:               s.source,
				Samples:              s.samples,
				Exploratory:          s.exploratory,
				AggregateTPS:         s.aggregateTPS,
				PreviousAggregateTPS: s.previousAggregateTPS,
				VirtualDecode:        s.virtualDecode,
				VirtualActiveKV:      s.virtualActiveKV,
				Hold:                 s.hold,
				ActivatedAt:          now,
				Until:                s.until,
			}
		}
		return nil
	}
	if s.availabilityActive {
		return nil
	}
	s.activations++
	s.availabilityActive = true
	s.availabilityReason = domainpredictive.ReasonPredictorProfileUnknown
	s.availabilityActivated = now
	s.availabilityActivation = s.activations
	return &predictiveRouterBackpressureEvent{
		Kind:          predictiveRouterBackpressureActivated,
		Activation:    s.availabilityActivation,
		Scope:         predictiveProtectionScopeAvailability,
		Reason:        s.availabilityReason,
		Source:        runtimepredictive.PredictionSourceUnavailable,
		VirtualDecode: 1,
		ActivatedAt:   now,
	}
}

func (s predictiveRouterBackpressureState) Snapshot(now time.Time) predictiveRouterBackpressureSnapshot {
	if s.availabilityActive {
		return predictiveRouterBackpressureSnapshot{
			Active:             true,
			Activation:         s.availabilityActivation,
			Scope:              predictiveProtectionScopeAvailability,
			Reason:             s.availabilityReason,
			Source:             runtimepredictive.PredictionSourceUnavailable,
			ActivatedAt:        s.availabilityActivated,
			MinimumRunning:     1,
			Activations:        s.activations,
			Extensions:         s.extensions,
			LatestRejectAt:     s.latestRejectAt,
			RenewalLogs:        s.renewalLogs,
			RenewalsSuppressed: s.renewalsSuppressed,
		}
	}
	active := !s.until.IsZero() && now.Before(s.until)
	snapshot := predictiveRouterBackpressureSnapshot{
		Active:             active,
		Activation:         s.activation,
		Scope:              predictiveProtectionScopeLoad,
		ActivatedAt:        s.activatedAt,
		Until:              s.until,
		Hold:               s.hold,
		Activations:        s.activations,
		Extensions:         s.extensions,
		LatestRejectAt:     s.latestRejectAt,
		RenewalLogs:        s.renewalLogs,
		RenewalsSuppressed: s.renewalsSuppressed,
	}
	if active {
		snapshot.Reason = s.reason
		snapshot.Source = s.source
		snapshot.Samples = s.samples
		snapshot.Exploratory = s.exploratory
		snapshot.AggregateTPS = s.aggregateTPS
		snapshot.PreviousAggregateTPS = s.previousAggregateTPS
	}
	if !active {
		snapshot.Activation = 0
		snapshot.Scope = ""
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
		domainpredictive.ReasonThroughputFrontier,
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

func predictiveProtectionScopeForResult(result runtimepredictive.CountAdmissionResult, policy predictiveRouterBackpressurePolicy) predictiveProtectionScope {
	if result.AvailabilityUnavailable {
		return predictiveProtectionScopeAvailability
	}
	if predictiveReasonCreatesRouterBackpressure(result, policy) {
		return predictiveProtectionScopeLoad
	}
	return predictiveProtectionScopeRequest
}

func (s *predictiveRequestRejectLogState) Observe(now time.Time, hold time.Duration, result runtimepredictive.CountAdmissionResult) *predictiveRequestRejectEvent {
	return s.ObservePhase(now, hold, "decision", result)
}

func (s *predictiveRequestRejectLogState) ObservePhase(now time.Time, hold time.Duration, phase string, result runtimepredictive.CountAdmissionResult) *predictiveRequestRejectEvent {
	if s == nil {
		return nil
	}
	phase, phaseIndex := predictiveRequestRejectPhase(phase)
	bucket := &s.buckets[phaseIndex]
	hold = normalizePredictiveRouterBackpressureHold(hold)
	if bucket.lastReason == result.Decision.Reason && !bucket.lastLoggedAt.IsZero() && now.Before(bucket.lastLoggedAt.Add(hold)) {
		bucket.suppressed++
		return nil
	}
	event := &predictiveRequestRejectEvent{
		Phase:                phase,
		Reason:               result.Decision.Reason,
		Source:               result.Prediction.Source,
		Samples:              result.Prediction.Samples,
		Exploratory:          result.Prediction.Exploratory,
		AggregateTPS:         result.Prediction.Estimate.AggregateCompletionTPSEstimate,
		PreviousAggregateTPS: result.Prediction.Estimate.PreviousAggregateCompletionTPSEstimate,
		Scope:                predictiveProtectionScopeRequest,
		RejectedAt:           now,
		Suppressed:           bucket.suppressed,
	}
	bucket.lastLoggedAt = now
	bucket.lastReason = result.Decision.Reason
	bucket.suppressed = 0
	return event
}

func predictiveRequestRejectPhase(phase string) (string, int) {
	switch phase {
	case "decision":
		return phase, 0
	case "decision_panic":
		return phase, 1
	case "invalid_decision_result":
		return phase, 2
	case "forward_commit":
		return phase, 3
	default:
		return "unknown", 4
	}
}

func predictiveRequestRejectLogLine(event predictiveRequestRejectEvent) string {
	phase := event.Phase
	if phase == "" {
		phase = "decision"
	}
	source := event.Source
	if source == "" {
		source = runtimepredictive.PredictionSourceStatic
	}
	return fmt.Sprintf(
		"predictive_admission event=request_rejected mode=enforce phase=%s scope=%s reason=%s source=%s samples=%d exploratory=%t aggregate_completion_tps_estimate=%.6f previous_aggregate_completion_tps_estimate=%.6f suppressed=%d rejected_at=%s",
		phase,
		event.Scope,
		event.Reason,
		source,
		event.Samples,
		event.Exploratory,
		event.AggregateTPS,
		event.PreviousAggregateTPS,
		event.Suppressed,
		event.RejectedAt.UTC().Format(time.RFC3339Nano),
	)
}

func logPredictiveRequestReject(event predictiveRequestRejectEvent) {
	log.Print(predictiveRequestRejectLogLine(event))
}

func (s *proxyServer) logPredictiveFailureReject(phase string) {
	if s == nil {
		return
	}
	now := time.Now()
	s.predictiveFailureLogMu.Lock()
	event := s.predictiveFailureRejectLogs.ObservePhase(now, s.cfg.PredictiveRouterBackpressureHold, phase, runtimepredictive.CountAdmissionResult{
		Decision: domainpredictive.Decision{Reason: domainpredictive.ReasonPredictorProfileUnknown},
		Prediction: runtimepredictive.SchedulerPrediction{
			Source: runtimepredictive.PredictionSourceUnavailable,
		},
	})
	s.predictiveFailureLogMu.Unlock()
	if event != nil {
		logPredictiveRequestReject(*event)
	}
}

func predictiveRouterBackpressureLogLine(event predictiveRouterBackpressureEvent) string {
	if event.Kind == predictiveRouterBackpressureRenewed {
		return fmt.Sprintf(
			"predictive_router_backpressure event=renewed mode=enforce activation=%d scope=%s reason=%s source=%s samples=%d exploratory=%t aggregate_completion_tps_estimate=%.6f previous_aggregate_completion_tps_estimate=%.6f virtual_decode=%d virtual_active_kv=%d extensions=%d suppressed=%d latest_reject_at=%s until=%s",
			event.Activation,
			event.Scope,
			event.Reason,
			event.Source,
			event.Samples,
			event.Exploratory,
			event.AggregateTPS,
			event.PreviousAggregateTPS,
			event.VirtualDecode,
			event.VirtualActiveKV,
			event.Extensions,
			event.Suppressed,
			event.RejectedAt.UTC().Format(time.RFC3339Nano),
			predictiveRouterBackpressureTime(event.Until),
		)
	}
	return fmt.Sprintf(
		"predictive_router_backpressure event=activated mode=enforce activation=%d scope=%s reason=%s source=%s samples=%d exploratory=%t aggregate_completion_tps_estimate=%.6f previous_aggregate_completion_tps_estimate=%.6f virtual_decode=%d virtual_active_kv=%d hold=%s activated_at=%s until=%s",
		event.Activation,
		event.Scope,
		event.Reason,
		event.Source,
		event.Samples,
		event.Exploratory,
		event.AggregateTPS,
		event.PreviousAggregateTPS,
		event.VirtualDecode,
		event.VirtualActiveKV,
		event.Hold,
		event.ActivatedAt.UTC().Format(time.RFC3339Nano),
		predictiveRouterBackpressureTime(event.Until),
	)
}

func predictiveRouterBackpressureTime(value time.Time) string {
	if value.IsZero() {
		return "none"
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func logPredictiveRouterBackpressure(event predictiveRouterBackpressureEvent) {
	log.Print(predictiveRouterBackpressureLogLine(event))
}
