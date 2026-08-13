package server

import (
	"fmt"
	"log"
	"time"
)

func (s *proxyServer) statusLogLine() string {
	now := time.Now()
	input, snapshot := s.predictiveAdmissionMetricsInput(now)
	projection := projectAdmissionCapacity(s.cfg.PredictiveAdmissionMode, snapshot.Capacity, snapshot.Report)
	compatibility := projectRouterCompatibility(s.cfg.PredictiveAdmissionMode, snapshot.Capacity, projection)
	return fmt.Sprintf(
		"%s predictive={mode=%s attempts=%d fit=%d risk=%d unknown=%d reject=%d reservations=%d action=%s reason=%s pressure=%s prefill=%s/%d kv=%d/%d/%d tps=%.3f router=%t/%s/%d observation=%t/%t/%d/%d compatibility=%d/%d/%d/%d}",
		version, input.Mode, input.Attempts, input.Fits, input.Risks, input.Unknown,
		input.EnforcedRejects, input.Reservations, input.AdmissionAction,
		input.AdmissionReason, input.AdmissionPressureSource,
		input.AdmissionPrefillClass, input.AdmissionEstimatedPrefillTokens,
		input.AdmissionEffectiveKV, input.AdmissionPostAdmitKV, input.AdmissionRemainingKV,
		input.AdmissionAggregateTPS,
		projection.Active, projection.Scope,
		projection.InspectCapacity, capacityObservationFresh(snapshot.Capacity, now),
		snapshot.Capacity.IntakeOpen, snapshot.Capacity.State.RawRunning, snapshot.Capacity.State.RawWaiting,
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
