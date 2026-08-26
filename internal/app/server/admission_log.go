package server

import (
	"fmt"
	"log"
	"time"
)

func admissionDecisionLogLine(event admissionDecisionLogEvent) string {
	decision := event.Decision
	level := "info"
	if event.Enforced {
		level = "warn"
	}
	return fmt.Sprintf(
		"level=%s component=admission event=protection mode=%s enforced=%t action=%s reason=%s scope=%s tps_result=%s tps_subreason=%s sequences=%d/%d/%d backend=%d/%d tps=%.3f/%.3f ready=%t leases=%d policy_revision=%d suppressed=%d",
		level,
		event.Mode,
		event.Enforced,
		decision.Action,
		decision.Reason,
		decision.Scope,
		decision.TPSDecisionResult,
		decision.TPSDecisionSubreason,
		decision.TPSCurrentSequences,
		decision.TPSPostAdmitSequences,
		decision.TPSSequenceLimit,
		decision.State.RawRunning,
		decision.State.RawWaiting,
		decision.State.TPS.MeanActiveTPS,
		decision.State.TPS.Reference,
		decision.State.TPS.Ready,
		decision.State.QoSBudgetLeases,
		decision.PolicyRevision,
		event.Suppressed,
	)
}

func admissionDecisionDetailLogLine(event admissionDecisionLogEvent) string {
	decision := event.Decision
	return fmt.Sprintf(
		"level=debug component=admission event=protection_detail mode=%s enforced=%t action=%s reason=%s scope=%s demand_source=%s decode_sequences=%d running=%d waiting=%d previous_running=%d generation_delta=%d preemption_delta=%d observation_interval=%s tps_reference=%.6f tps_window_ready=%t tps_window_qualified_samples=%d tps_window_qualified_sequence_samples=%d tps_window_qualified_sequence_seconds=%.6f tps_window_aggregate=%.6f tps_window_mean_active=%.6f tps_result=%s tps_subreason=%s tps_sequence_limit=%d tps_current_sequences=%d tps_post_admit_sequences=%d tps_qos_budgeted=%t unobserved_sequences=%d sequence_liabilities=%d live_reservations=%d residual_debts=%d qos_budget_leases=%d observation_sequence=%d controller_sequence=%d runtime_epoch=%d policy_revision=%d reservation_id=%d suppressed=%d observed_at=%s",
		event.Mode,
		event.Enforced,
		decision.Action,
		decision.Reason,
		decision.Scope,
		decision.Demand.Source,
		decision.Demand.DecodeSequences,
		decision.State.RawRunning,
		decision.State.RawWaiting,
		decision.State.PreviousRawRunning,
		decision.State.GenerationDelta,
		decision.State.PreemptionDelta,
		decision.State.ObservationInterval,
		decision.State.TPS.Reference,
		decision.State.TPS.Ready,
		decision.State.TPS.QualifiedSamples,
		decision.State.TPS.QualifiedSequenceSamples,
		decision.State.TPS.QualifiedSequenceSeconds,
		decision.State.TPS.AggregateTPS,
		decision.State.TPS.MeanActiveTPS,
		decision.TPSDecisionResult,
		decision.TPSDecisionSubreason,
		decision.TPSSequenceLimit,
		decision.TPSCurrentSequences,
		decision.TPSPostAdmitSequences,
		decision.TPSQoSBudgeted,
		decision.State.UnobservedSequences,
		decision.State.SequenceLiabilities,
		decision.State.LiveReservations,
		decision.State.ResidualDebts,
		decision.State.QoSBudgetLeases,
		decision.ObservationSequence,
		decision.ControllerSequence,
		decision.RuntimeEpoch,
		decision.PolicyRevision,
		decision.ReservationID,
		event.Suppressed,
		event.ObservedAt.UTC().Format(time.RFC3339Nano),
	)
}

func newAdmissionDecisionLogger(logLevel string) func(admissionDecisionLogEvent) {
	debug := logLevel == "debug"
	return func(event admissionDecisionLogEvent) {
		log.Print(admissionDecisionLogLine(event))
		if debug {
			log.Print(admissionDecisionDetailLogLine(event))
		}
	}
}
