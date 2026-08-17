package server

import (
	"fmt"
	"log"
	"sync"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

const defaultAdmissionDecisionLogInterval = time.Second

type admissionReportSnapshot struct {
	Attempts                uint64
	Admitted                uint64
	RequestProtected        uint64
	LoadProtected           uint64
	AvailabilityProtected   uint64
	ShadowProtectedForwards uint64
	HasLastDecision         bool
	LastDecision            coreadmission.DecisionRecord
	HasLastReject           bool
	LastReject              coreadmission.DecisionRecord
	LastRejectAt            time.Time
}

type admissionDecisionLogEvent struct {
	Mode       string
	Enforced   bool
	Decision   coreadmission.DecisionRecord
	Suppressed uint64
	ObservedAt time.Time
}

type admissionDecisionLogState struct {
	lastLoggedAt time.Time
	lastAction   coreadmission.Action
	lastReason   coreadmission.Reason
	lastScope    coreadmission.ProtectionScope
	lastEnforced bool
	suppressed   uint64
}

func (s *admissionDecisionLogState) Claim(
	now time.Time,
	interval time.Duration,
	event admissionDecisionLogEvent,
) *admissionDecisionLogEvent {
	if s == nil || event.Decision.Admitted() {
		return nil
	}
	if interval <= 0 {
		interval = defaultAdmissionDecisionLogInterval
	}
	signatureChanged := event.Decision.Action != s.lastAction ||
		event.Decision.Reason != s.lastReason || event.Decision.Scope != s.lastScope ||
		event.Enforced != s.lastEnforced
	elapsed := now.Sub(s.lastLoggedAt)
	if !s.lastLoggedAt.IsZero() && !signatureChanged && elapsed >= 0 && elapsed < interval {
		if s.suppressed < ^uint64(0) {
			s.suppressed++
		}
		return nil
	}
	event.Suppressed = s.suppressed
	event.ObservedAt = now
	s.lastLoggedAt = now
	s.lastAction = event.Decision.Action
	s.lastReason = event.Decision.Reason
	s.lastScope = event.Decision.Scope
	s.lastEnforced = event.Enforced
	s.suppressed = 0
	return &event
}

type admissionReporter struct {
	mu          sync.Mutex
	snapshot    admissionReportSnapshot
	logState    admissionDecisionLogState
	logInterval time.Duration
	onDecision  func(admissionDecisionLogEvent)
}

func newAdmissionReporter(
	logInterval time.Duration,
	onDecision func(admissionDecisionLogEvent),
) *admissionReporter {
	if logInterval <= 0 {
		logInterval = defaultAdmissionDecisionLogInterval
	}
	return &admissionReporter{logInterval: logInterval, onDecision: onDecision}
}

func (r *admissionReporter) Record(now time.Time, mode string, decision coreadmission.DecisionRecord) {
	if r == nil {
		return
	}
	enforced := mode == "enforce" && !decision.Admitted()
	r.mu.Lock()
	r.snapshot.Attempts++
	r.snapshot.HasLastDecision = true
	r.snapshot.LastDecision = decision
	if decision.Admitted() {
		r.snapshot.Admitted++
	} else {
		switch decision.Scope {
		case coreadmission.ProtectionRequest:
			r.snapshot.RequestProtected++
		case coreadmission.ProtectionLoad:
			r.snapshot.LoadProtected++
		default:
			r.snapshot.AvailabilityProtected++
		}
		if mode == "shadow" {
			r.snapshot.ShadowProtectedForwards++
		}
		if enforced {
			r.snapshot.HasLastReject = true
			r.snapshot.LastReject = decision
			r.snapshot.LastRejectAt = now
		}
	}
	event := r.logState.Claim(now, r.logInterval, admissionDecisionLogEvent{
		Mode: mode, Enforced: enforced, Decision: decision,
	})
	reporter := r.onDecision
	r.mu.Unlock()
	emitAdmissionDecision(reporter, event)
}

func (r *admissionReporter) Snapshot() admissionReportSnapshot {
	if r == nil {
		return admissionReportSnapshot{}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.snapshot
}

func admissionDecisionLogLine(event admissionDecisionLogEvent) string {
	decision := event.Decision
	return fmt.Sprintf(
		"predictive_admission event=admission_decision mode=%s enforced=%t action=%s reason=%s scope=%s prefill_class=%s selection_input_tokens=%d prefill_compute_tokens=%d request_cache_credit_tokens=%d cache_observation_valid=%t cache_hit_fraction=%.6f cache_credit_fraction=%.6f cache_evidence_tokens=%d cache_credit_budget_tokens=%d kv_reservation_input_tokens=%d decode_horizon_tokens=%d effective_kv_tokens=%d post_admit_kv_tokens=%d remaining_kv_tokens=%d pending_prefill_input_tokens_before=%d pending_prefill_tokens_before=%d pending_cache_credit_tokens_before=%d pending_prefill_tokens_after=%d running=%d waiting=%d tps_unobserved_sequences=%d tps_reference=%.6f tps_window_ready=%t tps_window_aggregate=%.6f tps_window_mean_active=%.6f tps_sequence_limit=%d tps_current_sequences=%d tps_post_admit_sequences=%d observation_sequence=%d controller_sequence=%d runtime_epoch=%d suppressed=%d observed_at=%s",
		event.Mode,
		event.Enforced,
		decision.Action,
		decision.Reason,
		decision.Scope,
		decision.PrefillClass,
		decision.Estimate.SelectionInputTokens,
		decision.Work.PrefillComputeTokens,
		decision.Estimate.SelectionInputTokens-decision.Work.PrefillComputeTokens,
		decision.State.CacheObservationValid,
		decision.State.CacheHitFraction,
		decision.State.CacheCreditFraction,
		decision.State.CacheEvidenceTokens,
		decision.State.CacheCreditBudgetTokens,
		decision.Estimate.KVReservationInputTokens,
		decision.Estimate.DecodeHorizonTokens,
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
		decision.State.TPS.Reference,
		decision.State.TPS.Ready,
		decision.State.TPS.AggregateTPS,
		decision.State.TPS.MeanActiveTPS,
		decision.TPSSequenceLimit,
		decision.TPSCurrentSequences,
		decision.TPSPostAdmitSequences,
		decision.ObservationSequence,
		decision.ControllerSequence,
		decision.RuntimeEpoch,
		event.Suppressed,
		event.ObservedAt.UTC().Format(time.RFC3339Nano),
	)
}

func logAdmissionDecision(event admissionDecisionLogEvent) {
	log.Print(admissionDecisionLogLine(event))
}

func emitAdmissionDecision(
	reporter func(admissionDecisionLogEvent),
	event *admissionDecisionLogEvent,
) {
	if reporter == nil || event == nil {
		return
	}
	defer func() { _ = recover() }()
	reporter(*event)
}
