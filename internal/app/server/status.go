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
	decision := snapshot.Capacity.MinimumDecision
	report := snapshot.Report
	return fmt.Sprintf(
		"level=info component=controller event=status version=%s mode=%s policy=%d/%s counts=%d/%d/%d/%d/%d rejects=%d reservations=%d last=%s/%s capacity=%s/%s prefill=%s/%d kv=%d/%d/%d cache=%t/%.3f/%.3f tps=%.3f/%.3f ready=%t sequences=%d/%d/%d unobserved=%d router=%t/%s/%d observer=%t/%t backend=%d/%d",
		version,
		input.Mode,
		snapshot.Capacity.Policy.Revision,
		predictivePolicySource(snapshot.Capacity.Policy),
		report.Attempts,
		report.Admitted,
		report.RequestProtected,
		report.LoadProtected,
		report.AvailabilityProtected,
		input.EnforcedRejects,
		input.Reservations,
		input.AdmissionAction,
		input.AdmissionReason,
		decision.Action,
		decision.Reason,
		decision.PrefillClass,
		decision.Estimate.SelectionInputTokens,
		snapshot.Capacity.State.EffectiveKVTokens,
		decision.PostAdmitKVTokens,
		decision.RemainingKVTokens,
		snapshot.Capacity.State.CacheObservationValid,
		snapshot.Capacity.State.CacheHitFraction,
		snapshot.Capacity.State.CacheCreditFraction,
		snapshot.Capacity.State.TPS.MeanActiveTPS,
		snapshot.Capacity.State.TPS.Reference,
		snapshot.Capacity.State.TPS.Ready,
		decision.TPSCurrentSequences,
		decision.TPSPostAdmitSequences,
		decision.TPSSequenceLimit,
		snapshot.Capacity.State.UnobservedSequences,
		projection.Active,
		projection.Scope,
		projection.InspectCapacity,
		capacityObservationFresh(snapshot.Capacity, now),
		snapshot.Capacity.IntakeOpen,
		snapshot.Capacity.State.RawRunning,
		snapshot.Capacity.State.RawWaiting,
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
