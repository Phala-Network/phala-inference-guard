package server

import (
	"sync"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

const (
	defaultAdmissionDecisionLogInterval = 5 * time.Second
	maximumAdmissionLogSignatures       = 256
)

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

type admissionDecisionLogSignature struct {
	action   coreadmission.Action
	reason   coreadmission.Reason
	scope    coreadmission.ProtectionScope
	enforced bool
}

type admissionDecisionLogBucket struct {
	lastLoggedAt time.Time
	suppressed   uint64
}

type admissionDecisionLogState struct {
	buckets map[admissionDecisionLogSignature]admissionDecisionLogBucket
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
	signature := admissionDecisionLogSignature{
		action: event.Decision.Action, reason: event.Decision.Reason,
		scope: event.Decision.Scope, enforced: event.Enforced,
	}
	if s.buckets == nil {
		s.buckets = make(map[admissionDecisionLogSignature]admissionDecisionLogBucket)
	}
	bucket, exists := s.buckets[signature]
	elapsed := now.Sub(bucket.lastLoggedAt)
	if exists && !bucket.lastLoggedAt.IsZero() && elapsed >= 0 && elapsed < interval {
		if bucket.suppressed < ^uint64(0) {
			bucket.suppressed++
		}
		s.buckets[signature] = bucket
		return nil
	}
	if !exists && len(s.buckets) >= maximumAdmissionLogSignatures {
		// Action, reason, scope and enforce state are all bounded enums. Keep a
		// defensive bound anyway so a future invalid enum cannot grow logging
		// state without limit.
		s.buckets = make(map[admissionDecisionLogSignature]admissionDecisionLogBucket)
		bucket = admissionDecisionLogBucket{}
	}
	event.Suppressed = bucket.suppressed
	event.ObservedAt = now
	s.buckets[signature] = admissionDecisionLogBucket{lastLoggedAt: now}
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
