package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

type inspectingAdmissionObserver struct {
	controller *coreadmission.AdmissionController
	now        time.Time
	closed     bool
	panicClose bool
	returnErr  error
}

type trackingAdmissionService struct {
	delegate         admissionService
	terminal         chan coreadmission.TerminalCause
	forwarded        atomic.Int64
	firstByte        atomic.Int64
	terminalAttempts atomic.Int64
	successful       atomic.Int64
}

func (s *trackingAdmissionService) Decide(
	ctx context.Context,
	demand coreadmission.TPSRequestDemand,
) admissionDecision {
	decision := s.delegate.Decide(ctx, demand)
	if decision.Reservation != nil {
		decision.Reservation = &trackingAdmissionReservation{
			owner:    s,
			delegate: decision.Reservation,
		}
	}
	return decision
}

func (s *trackingAdmissionService) Snapshot(now time.Time) admissionTelemetrySnapshot {
	return s.delegate.Snapshot(now)
}

func (s *trackingAdmissionService) Close() error { return s.delegate.Close() }

type trackingAdmissionReservation struct {
	owner    *trackingAdmissionService
	delegate admissionReservation
}

func (r *trackingAdmissionReservation) MarkForwarded() bool {
	if !r.delegate.MarkForwarded() {
		return false
	}
	r.owner.forwarded.Add(1)
	return true
}

func (r *trackingAdmissionReservation) MarkFirstByte() bool {
	if !r.delegate.MarkFirstByte() {
		return false
	}
	r.owner.firstByte.Add(1)
	return true
}

func (r *trackingAdmissionReservation) Terminate(cause coreadmission.TerminalCause) bool {
	r.owner.terminalAttempts.Add(1)
	if !r.delegate.Terminate(cause) {
		return false
	}
	r.owner.successful.Add(1)
	select {
	case r.owner.terminal <- cause:
	default:
	}
	return true
}

func requireTrackedTerminal(
	t *testing.T,
	tracked *trackingAdmissionService,
	want coreadmission.TerminalCause,
) {
	t.Helper()
	select {
	case cause := <-tracked.terminal:
		if cause != want {
			t.Fatalf("admission terminal cause=%s want=%s", cause, want)
		}
	default:
		t.Fatalf("admission request had no successful %s terminal transition", want)
	}
}

func (o *inspectingAdmissionObserver) Close() error {
	if o.controller == nil || !o.controller.Snapshot(o.now).IntakeOpen {
		return errors.New("Controller closed before Observer")
	}
	o.closed = true
	if o.panicClose {
		panic("observer close")
	}
	return o.returnErr
}

func TestAdmissionRuntimeCloseStopsObserverBeforeControllerEvenOnObserverPanic(t *testing.T) {
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	observer := &inspectingAdmissionObserver{
		controller: controller,
		now:        clock.Now(),
		panicClose: true,
	}
	runtime.observer = observer

	err := runtime.Close()
	if err == nil || err.Error() != "admission observer close panicked" || !observer.closed {
		t.Fatalf("runtime close error=%v observer=%+v", err, observer)
	}
	after := controller.Snapshot(clock.Now())
	if after.IntakeOpen || after.MinimumDecision.Reason != coreadmission.ReasonClosed {
		t.Fatalf("Controller remained open after observer panic: %+v", after)
	}
	if second := runtime.Close(); second == nil || second.Error() != err.Error() {
		t.Fatalf("runtime close is not idempotent: first=%v second=%v", err, second)
	}
}

func TestAdmissionReporterCallbackFailureCannotChangeDecision(t *testing.T) {
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	runtime.reporter = newAdmissionReporter(time.Nanosecond, func(admissionDecisionLogEvent) {
		panic("reporter callback")
	})
	decision := runtime.Decide(context.Background(), coreadmission.NewTPSRequestDemand(1))
	if !decision.Record.Admitted() ||
		decision.Reservation == nil ||
		controller.Snapshot(clock.Now()).State.LiveReservations != 1 {
		t.Fatalf(
			"reporter callback changed admission: decision=%+v state=%+v",
			decision,
			controller.Snapshot(clock.Now()).State,
		)
	}
	if !decision.Reservation.Terminate(coreadmission.TerminalCancel) {
		t.Fatal("reporter callback reservation could not be released")
	}
}

func TestAdmissionResponseEOFAndOuterDeferMutateTerminalOnce(t *testing.T) {
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	decision := runtime.Decide(context.Background(), coreadmission.NewTPSRequestDemand(1))
	if decision.Reservation == nil ||
		!decision.Reservation.MarkForwarded() ||
		!decision.Reservation.MarkFirstByte() {
		t.Fatalf("prepare response lifecycle: %+v", decision)
	}
	var successful int
	body := observeAdmissionResponseBody(
		io.NopCloser(bytes.NewReader([]byte("ok"))),
		nil,
		func() {
			if decision.Reservation.Terminate(coreadmission.TerminalSuccess) {
				successful++
			}
		},
	)
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if decision.Reservation.Terminate(coreadmission.TerminalSuccess) {
		successful++
	}
	state := controller.Snapshot(clock.Now()).State
	if successful != 1 ||
		state.LiveReservations != 0 ||
		state.ResidualDebts != 0 ||
		state.SequenceLiabilities != 0 {
		t.Fatalf("terminal successful_mutations=%d state=%+v", successful, state)
	}
}

func TestAdmissionTerminalCauseCoversHTTPFailurePaths(t *testing.T) {
	tests := []struct {
		name   string
		result proxyResult
		want   coreadmission.TerminalCause
	}{
		{name: "success", result: proxyResult{status: http.StatusOK}, want: coreadmission.TerminalSuccess},
		{name: "upstream error", result: proxyResult{status: http.StatusInternalServerError}, want: coreadmission.TerminalError},
		{name: "proxy failure", result: proxyResult{proxyFailed: true}, want: coreadmission.TerminalError},
		{name: "disconnect", result: proxyResult{status: clientClosedRequestStatus}, want: coreadmission.TerminalDisconnect},
		{name: "timeout", result: proxyResult{timedOut: true}, want: coreadmission.TerminalTimeout},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := admissionTerminalCause(test.result); got != test.want {
				t.Fatalf("terminal cause=%s want=%s result=%+v", got, test.want, test.result)
			}
		})
	}
}

func TestAdmissionDecisionLogRateLimitsEachSignatureIndependently(t *testing.T) {
	var state admissionDecisionLogState
	interval := 5 * time.Second
	started := time.Unix(100, 0)
	event := admissionDecisionLogEvent{
		Mode:     "enforce",
		Enforced: true,
		Decision: coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonTPSReference,
			Scope:  coreadmission.ProtectionLoad,
		},
	}
	if got := state.Claim(started, interval, event); got == nil {
		t.Fatal("first TPS protection was suppressed")
	}
	other := event
	other.Decision.Reason = coreadmission.ReasonObservationStale
	if got := state.Claim(started.Add(time.Second), interval, other); got == nil {
		t.Fatal("first availability protection was suppressed")
	}
	if got := state.Claim(started.Add(2*time.Second), interval, event); got != nil {
		t.Fatalf("alternating reason bypassed per-signature rate limit: %+v", got)
	}
	got := state.Claim(started.Add(6*time.Second), interval, event)
	if got == nil || got.Suppressed != 1 {
		t.Fatalf("TPS summary after interval=%+v, want one suppressed event", got)
	}
}

func TestAdmissionDecisionDebugLogRetainsBoundedTPSDiagnostics(t *testing.T) {
	line := admissionDecisionDetailLogLine(admissionDecisionLogEvent{
		Mode:       "shadow",
		ObservedAt: time.Unix(1, 0),
		Decision: coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonTPSReference,
			Scope:  coreadmission.ProtectionLoad,
			Demand: coreadmission.TPSRequestDemand{
				DecodeSequences: 2,
				Source:          coreadmission.TPSDemandSourceRequest,
			},
			State: coreadmission.ProjectedState{
				RawRunning:          3,
				SequenceLiabilities: 2,
			},
			TPSDecisionResult:    coreadmission.TPSDecisionResultProtect,
			TPSDecisionSubreason: coreadmission.TPSDecisionSubreasonWaiting,
			ObservationSequence:  7,
			ControllerSequence:   8,
			RuntimeEpoch:         9,
		},
	})
	for _, required := range []string{
		"level=debug",
		"event=protection_detail",
		"demand_source=request",
		"decode_sequences=2",
		"sequence_liabilities=2",
		"tps_result=protect",
		"tps_subreason=waiting",
		"observation_sequence=7",
		"controller_sequence=8",
		"runtime_epoch=9",
		"observed_at=",
	} {
		if !strings.Contains(line, required) {
			t.Fatalf("debug admission log missing %q: %s", required, line)
		}
	}
}

func TestAdmissionGenerationTPSDoesNotInventOneSequence(t *testing.T) {
	aggregate, mean, meanValid := admissionGenerationTPS(coreadmission.ProjectedState{
		GenerationDelta:     25,
		ObservationInterval: 500 * time.Millisecond,
	})
	if aggregate != 50 || mean != 0 || meanValid {
		t.Fatalf("completion-between-polls TPS aggregate=%v mean=%v valid=%t", aggregate, mean, meanValid)
	}
}
