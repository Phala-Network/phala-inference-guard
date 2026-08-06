package server

import (
	"fmt"
	"log"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const defaultRequestAwareDecisionLogInterval = time.Second

type requestAwareDecisionLogEvent struct {
	Mode                 string
	Enforced             bool
	Action               runtimepredictive.RequestAwareAction
	Reason               runtimepredictive.RequestAwareReason
	HTTPReason           domainpredictive.Reason
	Scope                predictiveProtectionScope
	PressureSource       runtimepredictive.RequestAwarePressureSource
	Pressure             float64
	SelectionInputTokens int64
	ReservedTokens       int64
	AllowanceTokens      int64
	EffectiveKV          int64
	PostAdmitKV          int64
	RemainingKV          int64
	Running              int
	Waiting              int
	EffectiveSequences   int
	AggregateTPSProxy    float64
	MeanActiveTPSProxy   float64
	ProjectedTPSProxy    float64
	TPSForecastValid     bool
	Suppressed           uint64
	ObservedAt           time.Time
}

type requestAwareDecisionLogState struct {
	lastLoggedAt time.Time
	lastAction   runtimepredictive.RequestAwareAction
	lastReason   runtimepredictive.RequestAwareReason
	lastSource   runtimepredictive.RequestAwarePressureSource
	lastEnforced bool
	suppressed   uint64
}

func (s *requestAwareDecisionLogState) Claim(
	now time.Time,
	interval time.Duration,
	event requestAwareDecisionLogEvent,
) *requestAwareDecisionLogEvent {
	if s == nil || event.Action == runtimepredictive.RequestAwareAdmit {
		return nil
	}
	if interval <= 0 {
		interval = defaultRequestAwareDecisionLogInterval
	}
	signatureChanged := event.Action != s.lastAction || event.Reason != s.lastReason ||
		event.PressureSource != s.lastSource || event.Enforced != s.lastEnforced
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
	s.lastAction = event.Action
	s.lastReason = event.Reason
	s.lastSource = event.PressureSource
	s.lastEnforced = event.Enforced
	s.suppressed = 0
	return &event
}

func requestAwareDecisionLogLine(event requestAwareDecisionLogEvent) string {
	pressureSource := event.PressureSource
	if pressureSource == "" {
		pressureSource = runtimepredictive.RequestAwarePressureNone
	}
	scope := event.Scope
	if scope == "" {
		scope = predictiveProtectionScopeRequest
	}
	return fmt.Sprintf(
		"predictive_request_aware event=admission_decision mode=%s enforced=%t action=%s reason=%s http_reason=%s scope=%s pressure_source=%s pressure=%.6f selection_input_tokens=%d reserved_tokens=%d allowance_tokens=%d effective_kv=%d post_admit_kv=%d remaining_kv=%d running=%d waiting=%d effective_sequences=%d aggregate_tps_proxy=%.6f mean_active_tps_proxy=%.6f projected_tps_proxy=%.6f tps_forecast_valid=%t suppressed=%d observed_at=%s",
		event.Mode,
		event.Enforced,
		event.Action,
		event.Reason,
		event.HTTPReason,
		scope,
		pressureSource,
		event.Pressure,
		event.SelectionInputTokens,
		event.ReservedTokens,
		event.AllowanceTokens,
		event.EffectiveKV,
		event.PostAdmitKV,
		event.RemainingKV,
		event.Running,
		event.Waiting,
		event.EffectiveSequences,
		event.AggregateTPSProxy,
		event.MeanActiveTPSProxy,
		event.ProjectedTPSProxy,
		event.TPSForecastValid,
		event.Suppressed,
		event.ObservedAt.UTC().Format(time.RFC3339Nano),
	)
}

func logRequestAwareDecision(event requestAwareDecisionLogEvent) {
	log.Print(requestAwareDecisionLogLine(event))
}

func emitRequestAwareDecision(
	reporter func(requestAwareDecisionLogEvent),
	event *requestAwareDecisionLogEvent,
) {
	if reporter == nil || event == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	reporter(*event)
}
