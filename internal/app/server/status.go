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
		"%s predictive={mode=%s policy=%d/%s attempts=%d fit=%d risk=%d unknown=%d reject=%d reservations=%d last=%s/%s pressure=%s capacity=%s/%s prefill=%s/%d kv=%d/%d/%d tps_proxy=%.3f sustained_tps=%.3f/%.3f reference=%.3f ready=%t sequences=%d/%d/%d unobserved=%d qos_budget=%t/%d router=%t/%s/%d observation=%t/%t/%d/%d compatibility=%d/%d/%d/%d}",
		version, input.Mode, snapshot.Capacity.Policy.Revision, predictivePolicySource(snapshot.Capacity.Policy),
		input.Attempts, input.Fits, input.Risks, input.Unknown,
		input.EnforcedRejects, input.Reservations, input.AdmissionAction,
		input.AdmissionReason, input.AdmissionPressureSource,
		snapshot.Capacity.MinimumDecision.Action, snapshot.Capacity.MinimumDecision.Reason,
		input.AdmissionPrefillClass, input.AdmissionEstimatedPrefillTokens,
		input.AdmissionEffectiveKV, input.AdmissionPostAdmitKV, input.AdmissionRemainingKV,
		input.AdmissionAggregateTPS,
		input.TPSWindowAggregate, input.TPSWindowMeanActive, input.TPSReference,
		input.TPSWindowReady,
		input.TPSCurrentSequences, input.TPSPostAdmitSequences, input.TPSSequenceLimit,
		input.TPSUnobservedSequences,
		input.TPSLastDecisionQoSBudgeted, input.TPSQoSBudgetLeases,
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
