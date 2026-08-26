package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
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
	Decide(context.Context, coreadmission.TPSRequestDemand) admissionDecision
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
	Capacity           coreadmission.CapacitySnapshot
	Report             admissionReportSnapshot
	PredictionDuration *durationHistogram
}

type admissionRuntime struct {
	controller  *coreadmission.AdmissionController
	reporter    *admissionReporter
	observer    admissionObserver
	backendKind string
	mode        string
	now         func() time.Time
	prediction  durationHistogram
	closeOnce   sync.Once
	closeErr    error
}

func newAdmissionRuntime(
	controller *coreadmission.AdmissionController,
	reporter *admissionReporter,
	backendKind string,
	mode string,
	now func() time.Time,
) (*admissionRuntime, error) {
	if controller == nil || reporter == nil ||
		(backendKind != "vllm" && backendKind != "sglang") ||
		(mode != "enforce" && mode != "shadow") {
		return nil, fmt.Errorf("admission runtime configuration is invalid")
	}
	if now == nil {
		now = time.Now
	}
	return &admissionRuntime{
		controller:  controller,
		reporter:    reporter,
		backendKind: backendKind,
		mode:        mode,
		now:         now,
		prediction:  newPredictiveDurationHistogram(),
	}, nil
}

func (r *admissionRuntime) Decide(
	ctx context.Context,
	demand coreadmission.TPSRequestDemand,
) admissionDecision {
	started := time.Now()
	defer func() {
		if r != nil {
			r.prediction.Observe(time.Since(started))
		}
	}()
	if r == nil || r.controller == nil || r.reporter == nil {
		return admissionDecision{Record: coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonControllerUnavailable,
			Scope:  coreadmission.ProtectionAvailability,
			Demand: demand,
		}}
	}
	now := r.now()
	if ctx == nil || ctx.Err() != nil {
		demand = coreadmission.TPSRequestDemand{}
	}
	result := r.controller.Admit(now, demand)
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
	capacity := r.controller.Snapshot(now)
	return admissionTelemetrySnapshot{
		BackendKind:        r.backendKind,
		Capacity:           capacity,
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
