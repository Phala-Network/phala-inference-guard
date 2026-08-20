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
	cacheCreditTokens := decision.Work.PrefillInputTokens - decision.Work.PrefillComputeTokens
	if cacheCreditTokens < 0 {
		cacheCreditTokens = 0
	}
	return fmt.Sprintf(
		"level=%s component=admission event=protection mode=%s enforced=%t action=%s reason=%s scope=%s prefill_class=%s input_estimate_confidence=%s input_tokens=%d prefill_compute_tokens=%d cache_credit_tokens=%d kv_tokens=%d/%d/%d backend=%d/%d sequences=%d/%d/%d tps=%.3f/%.3f tps_ready=%t policy_revision=%d suppressed=%d",
		level,
		event.Mode,
		event.Enforced,
		decision.Action,
		decision.Reason,
		decision.Scope,
		decision.PrefillClass,
		decision.Estimate.InputEstimateConfidence.String(),
		decision.Estimate.SelectionInputTokens,
		decision.Work.PrefillComputeTokens,
		cacheCreditTokens,
		decision.State.EffectiveKVTokens,
		decision.PostAdmitKVTokens,
		decision.RemainingKVTokens,
		decision.State.RawRunning,
		decision.State.RawWaiting,
		decision.TPSCurrentSequences,
		decision.TPSPostAdmitSequences,
		decision.TPSSequenceLimit,
		decision.State.TPS.MeanActiveTPS,
		decision.State.TPS.Reference,
		decision.State.TPS.Ready,
		decision.PolicyRevision,
		event.Suppressed,
	)
}

func admissionDecisionDetailLogLine(event admissionDecisionLogEvent) string {
	decision := event.Decision
	return fmt.Sprintf(
		"level=debug component=admission event=protection_detail mode=%s enforced=%t action=%s reason=%s scope=%s prefill_class=%s input_estimate_confidence=%s selection_input_tokens=%d maximum_sequence_input_tokens=%d base_prompt_count=%d prefill_input_tokens=%d prefill_compute_tokens=%d first_byte_pending_prefill_input_tokens=%d first_byte_pending_prefill_compute_tokens=%d first_byte_pending_prefill_sequences=%d request_cache_credit_tokens=%d cache_observation_valid=%t cache_hit_fraction=%.6f cache_credit_fraction=%.6f cache_evidence_tokens=%d cache_credit_budget_tokens=%d cache_credit_spent_tokens_before=%d kv_reservation_input_tokens=%d maximum_sequence_kv_reservation_input_tokens=%d input_kv_tokens=%d first_byte_coverable_input_kv_tokens=%d first_byte_pending_input_kv_tokens=%d future_kv_tokens=%d decode_horizon_tokens=%d output_limit_tokens=%d output_limit_known=%t decode_sequences=%d effective_kv_tokens=%d post_admit_kv_tokens=%d remaining_kv_tokens=%d pending_prefill_input_tokens_before=%d pending_prefill_tokens_before=%d pending_cache_credit_tokens_before=%d pending_prefill_tokens_after=%d running=%d waiting=%d tps_unobserved_sequences=%d tps_qos_budget_leases=%d tps_reference=%.6f tps_window_ready=%t tps_window_qualified_sequence_samples=%d tps_window_aggregate=%.6f tps_window_mean_active=%.6f tps_sequence_limit=%d tps_current_sequences=%d tps_post_admit_sequences=%d tps_qos_budgeted=%t observation_sequence=%d controller_sequence=%d runtime_epoch=%d policy_revision=%d suppressed=%d observed_at=%s",
		event.Mode,
		event.Enforced,
		decision.Action,
		decision.Reason,
		decision.Scope,
		decision.PrefillClass,
		decision.Estimate.InputEstimateConfidence.String(),
		decision.Estimate.SelectionInputTokens,
		decision.Estimate.MaximumSequenceInputTokens,
		decision.Estimate.BasePromptCount,
		decision.Work.PrefillInputTokens,
		decision.Work.PrefillComputeTokens,
		decision.Work.FirstBytePendingPrefillInputTokens,
		decision.Work.FirstBytePendingPrefillComputeTokens,
		decision.Work.FirstBytePendingPrefillSequences,
		decision.Work.PrefillInputTokens-decision.Work.PrefillComputeTokens,
		decision.State.CacheObservationValid,
		decision.State.CacheHitFraction,
		decision.State.CacheCreditFraction,
		decision.State.CacheEvidenceTokens,
		decision.State.CacheCreditBudgetTokens,
		decision.State.CacheCreditSpentTokens,
		decision.Estimate.KVReservationInputTokens,
		decision.Estimate.MaximumSequenceKVReservationInputTokens,
		decision.Work.InputKVTokens,
		decision.Work.FirstByteCoverableInputKVTokens,
		decision.Work.FirstBytePendingInputKVTokens,
		decision.Work.FutureKVTokens,
		decision.Estimate.DecodeHorizonTokens,
		decision.Estimate.OutputLimitTokens,
		decision.Estimate.OutputLimitKnown,
		decision.Estimate.DecodeSequences,
		decision.State.EffectiveKVTokens,
		decision.PostAdmitKVTokens,
		decision.RemainingKVTokens,
		decision.State.PendingPrefillInputTokens,
		decision.PendingPrefillTokensBefore,
		decision.State.PendingCacheCreditTokens,
		decision.PendingPrefillTokensAfter,
		decision.State.RawRunning,
		decision.State.RawWaiting,
		decision.State.UnobservedSequences,
		decision.State.QoSBudgetLeases,
		decision.State.TPS.Reference,
		decision.State.TPS.Ready,
		decision.State.TPS.QualifiedSequenceSamples,
		decision.State.TPS.AggregateTPS,
		decision.State.TPS.MeanActiveTPS,
		decision.TPSSequenceLimit,
		decision.TPSCurrentSequences,
		decision.TPSPostAdmitSequences,
		decision.TPSQoSBudgeted,
		decision.ObservationSequence,
		decision.ControllerSequence,
		decision.RuntimeEpoch,
		decision.PolicyRevision,
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
