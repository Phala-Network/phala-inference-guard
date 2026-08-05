package server

import (
	"fmt"
	"log"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/observability/metrics"
	statusview "github.com/Phala-Network/phala-inference-guard/internal/observability/status"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
)

func (s *proxyServer) statusLogLine() string {
	snapshot := dynamic.Snapshot{}
	if s.dynamicController == nil {
		snapshot = dynamic.Snapshot{}
	} else {
		snapshot = s.dynamicController.Snapshot()
	}
	tierSnapshot := s.qosGate.TierSnapshot(snapshot.GlobalLimit)
	predictive := s.predictiveAdmissionMetricsInput()
	routerCapacity := predictiveRouterCapacity(
		predictive.Mode,
		predictiveRouterBackpressureFromMetrics(predictive.RouterBackpressure),
		predictive.VirtualDecodeSequences,
		snapshot,
	)
	applyPredictiveRouterCapacity(&predictive, routerCapacity)
	line := statusview.Format(statusview.Input{
		Version:            version,
		Snapshot:           snapshot,
		QueueCurrent:       s.qosGate.QueueCurrent(),
		DynamicRejected:    s.qosGate.DynamicRejected(),
		BackendUnavailable: s.backendUnavailable.Load(),
		Tier: statusview.TierSnapshot{
			BasicInflight:   tierSnapshot.BasicInflight,
			BasicWaiting:    tierSnapshot.BasicWaiting,
			BasicLimit:      tierSnapshot.BasicLimit,
			PremiumInflight: tierSnapshot.PremiumInflight,
			PremiumWaiting:  tierSnapshot.PremiumWaiting,
			PremiumReserved: tierSnapshot.PremiumReserved,
		},
		Backends: s.statusBackendSnapshots(),
	})
	line += formatPredictiveStatus(predictive)
	if s.kvShadow != nil {
		shadow := s.kvShadow.Snapshot()
		last := shadow.LastDecision
		line += fmt.Sprintf(" kv_shadow={last=%s backend=%s projected=%d/%d ratio=%.3f reservations=%d tokens=%d}", last.Reason, last.Backend, last.ProjectedHighTokens, last.HardBudgetTokens, last.ProjectedRatio, shadow.Reservations, shadow.UnabsorbedTokens)
	}
	return line
}

func formatPredictiveStatus(input metrics.PredictiveAdmissionInput) string {
	backpressure := input.RouterBackpressure
	reason := input.LastReason
	if reason == "" {
		reason = "unknown"
	}
	source := input.LastSource
	if source == "" {
		source = "unknown"
	}
	backpressureReason := backpressure.Reason
	if backpressureReason == "" {
		backpressureReason = "none"
	}
	backpressureScope := backpressure.Scope
	if backpressureScope == "" {
		backpressureScope = "none"
	}
	lastRejectReason := input.LastRejectReason
	if lastRejectReason == "" {
		lastRejectReason = "none"
	}
	lastRejectSource := input.LastRejectSource
	if lastRejectSource == "" {
		lastRejectSource = "unknown"
	}
	lastRejectScope := input.LastRejectScope
	if lastRejectScope == "" {
		lastRejectScope = "none"
	}
	shadowPrefillState := input.ShadowPendingPrefillAttributionState
	switch shadowPrefillState {
	case "empty", "single", "aggregate", "incompatible":
	default:
		if input.ShadowPendingPrefills <= 0 {
			shadowPrefillState = "empty"
		} else {
			shadowPrefillState = "incompatible"
		}
	}
	return fmt.Sprintf(
		" predictive={mode=%s attempts=%d fit=%d risk=%d unknown=%d reject=%d last=%s/%s/%d last_reject=%s/%s/%s/%d reservations=%d virtual_decode=%d pending_prefill=%d/%d/%d shadow_prefill=%d/%d/%d/%s deferred=%d prefill_learning=%d/%d/%d/%.3f/%d/%d prefill_deduplicated=%d hard_origin=%d/%d/%d/%d/%d/%d completion_observer=%d/%d/%d/%d router_bp=%d/%d/%s/%s throughput=%.2f/%.2f router_lease=%d/%d/%d/%d/%s/%s effective=%d/%d raw=%d/%d}",
		input.Mode,
		input.Attempts,
		input.Fits,
		input.Risks,
		input.Unknown,
		input.EnforcedRejects,
		reason,
		source,
		input.LastSamples,
		lastRejectReason,
		lastRejectSource,
		lastRejectScope,
		input.LastRejectSamples,
		input.Reservations,
		input.VirtualDecodeSequences,
		input.ForwardedPendingPrefills,
		input.ForwardedPendingPrefillTokens,
		boolInt(input.ForwardedPendingPrefillAttributionValid),
		input.ShadowPendingPrefills,
		input.ShadowPendingPrefillTokens,
		boolInt(input.ShadowPendingPrefillAttributionValid),
		shadowPrefillState,
		input.DeferredOutcomes.Active,
		input.ExistingPrefill.Accepted,
		input.ExistingPrefill.Rejected,
		input.ExistingPrefill.Censored,
		input.ExistingPrefill.LastExistingUserTPS,
		boolInt(input.ExistingPrefill.LastExistingUserTPSValid),
		boolInt(input.ExistingPrefill.LastExploratory),
		input.ExistingPrefill.Deduplicated,
		input.LearningHardExistingTPSExploratory,
		input.LearningHardExistingTPSNonExploratory,
		input.LearningHardNewTPSExploratory,
		input.LearningHardNewTPSNonExploratory,
		input.LearningHardTPOTExploratory,
		input.LearningHardTPOTNonExploratory,
		input.CompletionObserverAttached,
		input.CompletionObserverClaimed,
		input.CompletionObserverUsage,
		input.CompletionObserverTerminal,
		boolInt(backpressure.Active),
		boolInt(backpressure.Applied),
		backpressureScope,
		backpressureReason,
		backpressure.AggregateCompletionTPSEstimate,
		backpressure.PreviousAggregateCompletionTPSEstimate,
		backpressure.Activation,
		backpressure.Extensions,
		backpressure.RenewalLogs,
		backpressure.RenewalsSuppressed,
		predictiveRouterBackpressureTime(backpressure.LatestRejectAt),
		predictiveRouterBackpressureTime(backpressure.Until),
		backpressure.EffectiveRunning,
		backpressure.EffectiveGlobalLimit,
		backpressure.RawRunning,
		backpressure.RawGlobalLimit,
	)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *proxyServer) statusBackendSnapshots() []statusview.BackendSnapshot {
	backends := make([]statusview.BackendSnapshot, 0, len(s.backends))
	for index, backend := range s.backends {
		backendStatus := s.backendRuntimeStatus(index, backend)
		backends = append(backends, statusview.BackendSnapshot{
			Name:      backend.Name(),
			Running:   backendStatus.Running,
			Waiting:   backendStatus.Waiting,
			Inflight:  backend.Inflight(),
			TTFTValid: backendStatus.TTFTValid,
			TTFTP95:   backendStatus.TTFTP95,
			TTFTP99:   backendStatus.TTFTP99,
			Failed:    backendStatus.Failed,
		})
	}
	return backends
}

func (s *proxyServer) statusLogLoop() {
	if s.cfg.StatusLogInterval <= 0 {
		return
	}
	ticker := time.NewTicker(s.cfg.StatusLogInterval)
	defer ticker.Stop()
	for range ticker.C {
		log.Print(s.statusLogLine())
	}
}
