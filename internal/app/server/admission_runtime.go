package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type admissionReservation interface {
	MarkForwarded() bool
	MarkFirstByte() bool
	Terminate(coreadmission.TerminalCause) bool
}

type admissionDecision struct {
	Record      coreadmission.DecisionRecord
	Reservation admissionReservation
}

func (d admissionDecision) valid() bool {
	if d.Record.Admitted() {
		return d.Reservation != nil
	}
	return d.Record.Action == coreadmission.ActionProtect && d.Reservation == nil
}

type admissionService interface {
	Decide(context.Context, domainpredictive.RequestEstimate) admissionDecision
	Snapshot(time.Time) admissionTelemetrySnapshot
	Close() error
}

type admissionPolicyService interface {
	UpdateTPSPolicy(coreadmission.TPSPolicyUpdate) (coreadmission.TPSPolicyUpdateResult, error)
}

type admissionObserver interface {
	Close() error
}

type serverDependencies struct {
	NewAdmission func(config) (admissionService, error)
}

type admissionTelemetrySnapshot struct {
	BackendKind        string
	CapabilityProfile  runtimepredictive.BackendCapabilityProfile
	CapabilityReason   string
	Capacity           coreadmission.CapacitySnapshot
	Report             admissionReportSnapshot
	PredictionDuration *durationHistogram
}

type admissionRuntime struct {
	controller       *coreadmission.AdmissionController
	reporter         *admissionReporter
	observer         admissionObserver
	backendKind      string
	profile          runtimepredictive.BackendCapabilityProfile
	capabilityReason string
	mode             string
	now              func() time.Time
	prediction       durationHistogram
	closeOnce        sync.Once
	closeErr         error
}

func newAdmissionRuntime(
	controller *coreadmission.AdmissionController,
	reporter *admissionReporter,
	profile runtimepredictive.BackendCapabilityProfile,
	capabilityReason string,
	backendKind string,
	mode string,
	now func() time.Time,
) (*admissionRuntime, error) {
	if controller == nil || reporter == nil || profile.Validate() != nil ||
		(capabilityReason != "metadata" && capabilityReason != "explicit_override" && capabilityReason != "test") ||
		(backendKind != "vllm" && backendKind != "sglang") ||
		(mode != "enforce" && mode != "shadow") {
		return nil, fmt.Errorf("admission runtime configuration is invalid")
	}
	if now == nil {
		now = time.Now
	}
	return &admissionRuntime{
		controller:       controller,
		reporter:         reporter,
		backendKind:      backendKind,
		profile:          profile,
		capabilityReason: capabilityReason,
		mode:             mode,
		now:              now,
		prediction:       newPredictiveDurationHistogram(),
	}, nil
}

func (r *admissionRuntime) Decide(
	ctx context.Context,
	estimate domainpredictive.RequestEstimate,
) admissionDecision {
	started := time.Now()
	defer func() {
		if r != nil {
			r.prediction.Observe(time.Since(started))
		}
	}()
	if r == nil || r.controller == nil || r.reporter == nil {
		return admissionDecision{Record: coreadmission.DecisionRecord{
			Action:   coreadmission.ActionProtect,
			Reason:   coreadmission.ReasonControllerUnavailable,
			Scope:    coreadmission.ProtectionAvailability,
			Estimate: estimate,
		}}
	}
	now := r.now()
	if ctx == nil || ctx.Err() != nil {
		estimate = domainpredictive.RequestEstimate{}
	}
	result := r.controller.Admit(now, estimate)
	r.reporter.Record(now, r.mode, result.Decision)
	decision := admissionDecision{Record: result.Decision}
	if result.Decision.Admitted() {
		decision.Reservation = result.Handle
	}
	return decision
}

func (r *admissionRuntime) Snapshot(now time.Time) admissionTelemetrySnapshot {
	if r == nil || r.controller == nil || r.reporter == nil {
		return admissionTelemetrySnapshot{}
	}
	return admissionTelemetrySnapshot{
		BackendKind:        r.backendKind,
		CapabilityProfile:  r.profile,
		CapabilityReason:   r.capabilityReason,
		Capacity:           r.controller.Snapshot(now),
		Report:             r.reporter.Snapshot(),
		PredictionDuration: &r.prediction,
	}
}

func (r *admissionRuntime) UpdateTPSPolicy(
	update coreadmission.TPSPolicyUpdate,
) (coreadmission.TPSPolicyUpdateResult, error) {
	if r == nil || r.controller == nil {
		return coreadmission.TPSPolicyUpdateResult{}, coreadmission.ErrTPSPolicyUnavailable
	}
	return r.controller.UpdateTPSPolicy(update)
}

func (r *admissionRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		if r.observer != nil {
			r.closeErr = closeAdmissionObserver(r.observer)
		}
		if r.controller != nil {
			r.controller.Close()
		}
	})
	return r.closeErr
}

func closeAdmissionObserver(observer admissionObserver) (err error) {
	if observer == nil {
		return nil
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("admission observer close panicked")
		}
	}()
	return observer.Close()
}
