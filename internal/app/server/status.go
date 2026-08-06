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
	requestAwareAction := input.RequestAwareAction
	if requestAwareAction == "" {
		requestAwareAction = "unknown"
	}
	requestAwareReason := input.RequestAwareReason
	if requestAwareReason == "" {
		requestAwareReason = "unknown"
	}
	requestAwarePressureSource := input.RequestAwarePressureSource
	if requestAwarePressureSource == "" {
		requestAwarePressureSource = "none"
	}
	requestAwarePrefillClass := input.RequestAwarePrefillClass
	if requestAwarePrefillClass == "" {
		requestAwarePrefillClass = "unknown"
	}
	return fmt.Sprintf(
		" predictive={mode=%s attempts=%d fit=%d risk=%d unknown=%d reject=%d last=%s/%s last_reject=%s/%s/%s reservations=%d virtual_decode=%d pending_prefill=%d/%d/%d retired=%d/%d request_aware=%s/%s/%s pressure=%.3f size=%d/%d/%d prefill_last=%s/%d/%d/%d/%d/%d/%d prefill_current=%d/%d/%d/%d kv=%d/%d/%d load=%d/%d/%d tps=%.3f/%.3f/%.3f/%d router_bp=%d/%d/%s/%s inspect=%d activation=%d effective=%d/%d raw=%d/%d}",
		input.Mode,
		input.Attempts,
		input.Fits,
		input.Risks,
		input.Unknown,
		input.EnforcedRejects,
		reason,
		source,
		lastRejectReason,
		lastRejectSource,
		lastRejectScope,
		input.Reservations,
		input.VirtualDecodeSequences,
		input.ForwardedPendingPrefills,
		input.ForwardedPendingPrefillTokens,
		boolInt(input.ForwardedPendingPrefillAttributionValid),
		input.RetiredReservations,
		input.RetiredEvictions,
		requestAwareAction,
		requestAwareReason,
		requestAwarePressureSource,
		input.RequestAwarePressure,
		input.RequestAwareSelectionInputTokens,
		input.RequestAwareReservedTokens,
		input.RequestAwareAllowanceTokens,
		requestAwarePrefillClass,
		input.RequestAwareEstimatedPrefillTokens,
		input.RequestAwareLastDecisionPendingPrefillSequences,
		input.RequestAwareLastDecisionPendingPrefillTokens,
		input.RequestAwareLastDecisionPostAdmitPendingPrefillTokens,
		input.RequestAwareLastDecisionPendingLongPrefillSequences,
		input.RequestAwareLastDecisionPendingQuiescentPrefillSequences,
		input.RequestAwarePendingPrefillSequences,
		input.RequestAwarePendingPrefillTokens,
		input.RequestAwarePendingLongPrefillSequences,
		input.RequestAwarePendingQuiescentPrefillSequences,
		input.RequestAwareEffectiveKV,
		input.RequestAwarePostAdmitKV,
		input.RequestAwareRemainingKV,
		input.RequestAwareRunning,
		input.RequestAwareWaiting,
		input.RequestAwareEffectiveSequences,
		input.RequestAwareAggregateTPSProxy,
		input.RequestAwareMeanActiveTPSProxy,
		input.RequestAwareProjectedTPSProxy,
		boolInt(input.RequestAwareTPSForecastValid),
		boolInt(backpressure.Active),
		boolInt(backpressure.Applied),
		backpressureScope,
		backpressureReason,
		backpressure.InspectCapacity,
		backpressure.Activation,
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
