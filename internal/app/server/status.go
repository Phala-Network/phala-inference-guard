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
		"level=info component=controller event=status version=%s mode=%s policy=%d/%s counts=%d/%d/%d/%d/%d rejects=%d reservations=%d residual_debts=%d last=%s/%s capacity=%s/%s tps=%.3f/%.3f ready=%t latest=%t/%.3f tps_result=%s tps_subreason=%s running=%d/%d/%s waiting=%d projected_running=%d window=%d/%d unobserved=%d liabilities=%d router=%t/%s/%d observer=%t/%t",
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
		snapshot.Capacity.State.ResidualDebts,
		input.AdmissionAction,
		input.AdmissionReason,
		decision.Action,
		decision.Reason,
		snapshot.Capacity.State.TPS.MeanActiveTPS,
		snapshot.Capacity.State.TPS.Reference,
		snapshot.Capacity.State.TPS.Ready,
		snapshot.Capacity.State.TPS.Latest.Qualified,
		snapshot.Capacity.State.TPS.Latest.MeanActiveTPS,
		decision.TPSDecisionResult,
		decision.TPSDecisionSubreason,
		snapshot.Capacity.State.RawRunning,
		decision.RunningLimit,
		decision.RunningLimitSource,
		snapshot.Capacity.State.RawWaiting,
		decision.ProjectedRunning,
		decision.ProjectedWindowSequences,
		decision.WindowConcurrency,
		snapshot.Capacity.State.UnobservedSequences,
		snapshot.Capacity.State.SequenceLiabilities,
		projection.Active,
		projection.Scope,
		projection.InspectCapacity,
		capacityObservationFresh(snapshot.Capacity, now),
		snapshot.Capacity.IntakeOpen,
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
