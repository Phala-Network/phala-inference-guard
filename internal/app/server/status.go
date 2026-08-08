package server

import (
	"fmt"
	"log"
	"time"
)

func (s *proxyServer) statusLogLine() string {
	input, snapshot := s.predictiveAdmissionMetricsInput()
	compatibility := predictiveRouterCompatibility(s.cfg.PredictiveAdmissionMode, snapshot)
	return fmt.Sprintf(
		"%s predictive={mode=%s attempts=%d fit=%d risk=%d unknown=%d reject=%d reservations=%d action=%s reason=%s pressure=%s prefill=%s/%d kv=%d/%d/%d tps=%.3f router=%t/%s/%d observer=%t/%t/%d/%d compatibility=%d/%d/%d/%d}",
		version, input.Mode, input.Attempts, input.Fits, input.Risks, input.Unknown,
		input.EnforcedRejects, input.Reservations, input.RequestAwareAction,
		input.RequestAwareReason, input.RequestAwarePressureSource,
		input.RequestAwarePrefillClass, input.RequestAwareEstimatedPrefillTokens,
		input.RequestAwareEffectiveKV, input.RequestAwarePostAdmitKV, input.RequestAwareRemainingKV,
		input.RequestAwareAggregateTPSProxy,
		snapshot.RouterBackpressure.Active, snapshot.RouterBackpressure.Scope,
		snapshot.RouterBackpressure.InspectCapacity, snapshot.Observer.MetricsFresh,
		snapshot.Observer.IdentityValid, snapshot.Observer.Running, snapshot.Observer.Waiting,
		compatibility.ObservedRunning, compatibility.ObservedWaiting,
		compatibility.GlobalLimitRaw, compatibility.GlobalLimit,
	)
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
