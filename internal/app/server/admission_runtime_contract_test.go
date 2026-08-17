package server

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
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

func (s *trackingAdmissionService) Decide(ctx context.Context, estimate domainpredictive.RequestEstimate) admissionDecision {
	decision := s.delegate.Decide(ctx, estimate)
	if decision.Reservation != nil {
		decision.Reservation = &trackingAdmissionReservation{owner: s, delegate: decision.Reservation}
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
	observer := &inspectingAdmissionObserver{controller: controller, now: clock.Now(), panicClose: true}
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
	decision := runtime.Decide(context.Background(), domainpredictive.RequestEstimate{
		SelectionInputTokens: 1_024, MaximumSequenceInputTokens: 1_024,
		KVReservationInputTokens: 1_024, DecodeHorizonTokens: 256,
		MaximumSequenceKVReservationInputTokens: 1_024,
		BasePromptCount:                         1, DecodeSequences: 1,
	})
	if !decision.Record.Admitted() || decision.Reservation == nil || controller.Snapshot(clock.Now()).State.LiveReservations != 1 {
		t.Fatalf("reporter callback changed admission: decision=%+v state=%+v", decision, controller.Snapshot(clock.Now()).State)
	}
	if !decision.Reservation.Terminate(coreadmission.TerminalCancel) {
		t.Fatal("reporter callback admission reservation could not be released")
	}
}

func TestAdmissionResponseEOFAndOuterDeferMutateTerminalOnce(t *testing.T) {
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	decision := runtime.Decide(context.Background(), domainpredictive.RequestEstimate{
		SelectionInputTokens: 1_024, MaximumSequenceInputTokens: 1_024,
		KVReservationInputTokens: 1_024, DecodeHorizonTokens: 256,
		MaximumSequenceKVReservationInputTokens: 1_024,
		BasePromptCount:                         1, DecodeSequences: 1,
	})
	if decision.Reservation == nil || !decision.Reservation.MarkForwarded() || !decision.Reservation.MarkFirstByte() {
		t.Fatalf("prepare response lifecycle: %+v", decision)
	}
	var successful int
	body := observeAdmissionResponseBody(io.NopCloser(bytes.NewReader([]byte("ok"))), nil, func() {
		if decision.Reservation.Terminate(coreadmission.TerminalSuccess) {
			successful++
		}
	})
	if _, err := io.ReadAll(body); err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if decision.Reservation.Terminate(coreadmission.TerminalSuccess) {
		successful++
	}
	state := controller.Snapshot(clock.Now()).State
	if successful != 1 || state.LiveReservations != 0 || state.ResidualDebts != 1 {
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

func TestAdmissionHTTPClientCancellationTerminatesExactlyOnce(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer backend.Close()
	defer close(release)
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	tracked := &trackingAdmissionService{delegate: runtime, terminal: make(chan coreadmission.TerminalCause, 2)}
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", tracked)
	clientContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"model-agnostic","messages":[{"role":"user","content":"cancel"}],"max_tokens":8}`),
	).WithContext(clientContext)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() {
		srv.ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("cancel test request did not reach upstream")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled admission request did not return")
	}
	requireTrackedTerminal(t, tracked, coreadmission.TerminalDisconnect)
	if tracked.forwarded.Load() != 1 || tracked.firstByte.Load() != 0 ||
		tracked.terminalAttempts.Load() != 1 || tracked.successful.Load() != 1 {
		t.Fatalf(
			"cancelled admission lifecycle forwarded=%d first_byte=%d terminal_attempts=%d successful=%d",
			tracked.forwarded.Load(), tracked.firstByte.Load(), tracked.terminalAttempts.Load(), tracked.successful.Load(),
		)
	}
	state := controller.Snapshot(clock.Now()).State
	if state.LiveReservations != 0 || state.ResidualDebts != 1 {
		t.Fatalf("cancelled admission lifecycle state=%+v", state)
	}
	clock.Advance(time.Millisecond)
	publishAdmissionObservationForTest(t, controller, runtime.profile, coreadmission.BackendObservation{
		CapabilityFingerprint: runtime.profile.ModelIdentitySHA256,
		MaxModelLenTokens:     runtime.profile.MaxModelLenTokens, KVCapacityTokens: runtime.profile.KVCapacityTokens,
		KVBlockSize: runtime.profile.KVBlockSize, ObservedAt: clock.Now(), MaximumAge: time.Hour,
	})
	if covered := controller.Snapshot(clock.Now()).State; covered.LiveReservations != 0 || covered.ResidualDebts != 0 {
		t.Fatalf("covering observation did not clear cancelled request debt: %+v", covered)
	}
}

func TestAdmissionHTTPSuccessRunsFirstByteEOFAndOuterTerminalExactlyOnce(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
		_, _ = response.Write([]byte("ok"))
	}))
	defer backend.Close()
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	tracked := &trackingAdmissionService{delegate: runtime, terminal: make(chan coreadmission.TerminalCause, 2)}
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", tracked)

	response := serveAdmissionRequest(t, srv, "success")

	if response.Code != http.StatusOK || response.Body.String() != "ok" {
		t.Fatalf("successful admission response status=%d body=%q", response.Code, response.Body.String())
	}
	requireTrackedTerminal(t, tracked, coreadmission.TerminalSuccess)
	if tracked.forwarded.Load() != 1 || tracked.firstByte.Load() != 1 ||
		tracked.terminalAttempts.Load() != 2 || tracked.successful.Load() != 1 {
		t.Fatalf(
			"successful admission lifecycle forwarded=%d first_byte=%d terminal_attempts=%d successful=%d",
			tracked.forwarded.Load(), tracked.firstByte.Load(), tracked.terminalAttempts.Load(), tracked.successful.Load(),
		)
	}
	if srv.admissionFailures.firstByte.Load() != 0 || srv.admissionFailures.terminal.Load() != 0 {
		t.Fatalf(
			"successful admission lifecycle failures first_byte=%d terminal=%d",
			srv.admissionFailures.firstByte.Load(), srv.admissionFailures.terminal.Load(),
		)
	}
	state := controller.Snapshot(clock.Now()).State
	if state.LiveReservations != 0 || state.ResidualDebts != 1 {
		t.Fatalf("successful admission lifecycle state=%+v", state)
	}
}

func TestAdmissionHTTPUpstream5xxDoesNotCompleteAsSuccess(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusInternalServerError)
		_, _ = response.Write([]byte(`{"error":"upstream"}`))
	}))
	defer backend.Close()
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	tracked := &trackingAdmissionService{delegate: runtime, terminal: make(chan coreadmission.TerminalCause, 2)}
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", tracked)

	response := serveAdmissionRequest(t, srv, "upstream-5xx")

	if response.Code != http.StatusInternalServerError {
		t.Fatalf("upstream 5xx admission response status=%d body=%q", response.Code, response.Body.String())
	}
	requireTrackedTerminal(t, tracked, coreadmission.TerminalError)
	if tracked.forwarded.Load() != 1 || tracked.firstByte.Load() != 1 ||
		tracked.terminalAttempts.Load() != 1 || tracked.successful.Load() != 1 {
		t.Fatalf(
			"upstream 5xx lifecycle forwarded=%d first_byte=%d terminal_attempts=%d successful=%d",
			tracked.forwarded.Load(), tracked.firstByte.Load(), tracked.terminalAttempts.Load(), tracked.successful.Load(),
		)
	}
	state := controller.Snapshot(clock.Now()).State
	if state.LiveReservations != 0 || state.ResidualDebts != 1 {
		t.Fatalf("upstream 5xx admission lifecycle state=%+v", state)
	}
}

func TestAdmissionHTTPTransportFailureTerminatesAsErrorOnce(t *testing.T) {
	closedBackend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	upstream := closedBackend.URL
	closedBackend.Close()
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	tracked := &trackingAdmissionService{delegate: runtime, terminal: make(chan coreadmission.TerminalCause, 2)}
	srv := newProxyServerWithAdmissionForTest(t, upstream, "enforce", tracked)

	response := serveAdmissionRequest(t, srv, "transport-failure")

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("transport failure admission response status=%d body=%q", response.Code, response.Body.String())
	}
	requireTrackedTerminal(t, tracked, coreadmission.TerminalError)
	if tracked.forwarded.Load() != 1 || tracked.firstByte.Load() != 0 ||
		tracked.terminalAttempts.Load() != 1 || tracked.successful.Load() != 1 {
		t.Fatalf(
			"transport failure lifecycle forwarded=%d first_byte=%d terminal_attempts=%d successful=%d",
			tracked.forwarded.Load(), tracked.firstByte.Load(), tracked.terminalAttempts.Load(), tracked.successful.Load(),
		)
	}
	state := controller.Snapshot(clock.Now()).State
	if state.LiveReservations != 0 || state.ResidualDebts != 1 {
		t.Fatalf("transport failure admission lifecycle state=%+v", state)
	}
}

func TestAdmissionHTTPTimeoutTerminatesAsTimeoutOnce(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer backend.Close()
	defer close(release)
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	tracked := &trackingAdmissionService{delegate: runtime, terminal: make(chan coreadmission.TerminalCause, 2)}
	cfg := testProxyConfig(backend.URL)
	cfg.ProxyTimeout = 20 * time.Millisecond
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewAdmission: func(config) (admissionService, error) { return tracked, nil },
	})
	if err != nil {
		t.Fatalf("construct timeout admission proxy: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	response := serveAdmissionRequest(t, srv, "timeout")

	select {
	case <-started:
	default:
		t.Fatal("timeout admission request did not reach upstream")
	}
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("timeout admission response status=%d body=%q", response.Code, response.Body.String())
	}
	requireTrackedTerminal(t, tracked, coreadmission.TerminalTimeout)
	if tracked.forwarded.Load() != 1 || tracked.firstByte.Load() != 0 ||
		tracked.terminalAttempts.Load() != 1 || tracked.successful.Load() != 1 {
		t.Fatalf(
			"timeout admission lifecycle forwarded=%d first_byte=%d terminal_attempts=%d successful=%d",
			tracked.forwarded.Load(), tracked.firstByte.Load(), tracked.terminalAttempts.Load(), tracked.successful.Load(),
		)
	}
	state := controller.Snapshot(clock.Now()).State
	if state.LiveReservations != 0 || state.ResidualDebts != 1 {
		t.Fatalf("timeout admission lifecycle state=%+v", state)
	}
}

func TestAdmissionDecisionLogContainsNoRequestOrCredentialData(t *testing.T) {
	line := admissionDecisionLogLine(admissionDecisionLogEvent{
		Mode: "enforce", Enforced: true, ObservedAt: time.Unix(1, 0),
		Decision: coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect, Reason: coreadmission.ReasonKVCapacity,
			Scope: coreadmission.ProtectionLoad,
			Estimate: domainpredictive.RequestEstimate{
				SelectionInputTokens: 1_500, MaximumSequenceInputTokens: 900,
				KVReservationInputTokens: 1_600, MaximumSequenceKVReservationInputTokens: 1_000,
				BasePromptCount: 2, DecodeSequences: 4,
			},
			Work: domainpredictive.RequestWork{
				PrefillInputTokens: 1_500, PrefillComputeTokens: 1_200,
				FirstBytePendingPrefillInputTokens:   600,
				FirstBytePendingPrefillComputeTokens: 500,
				FirstBytePendingPrefillSequences:     3,
				InputKVTokens:                        1_600, FirstByteCoverableInputKVTokens: 400,
				FirstBytePendingInputKVTokens: 1_200, FutureKVTokens: 800,
			},
			State: coreadmission.ProjectedState{
				UnobservedSequences: 2,
				TPS: coreadmission.TPSSnapshot{
					Enabled: true, Ready: true, Reference: 20,
					QualifiedSequenceSamples: 4, AggregateTPS: 180, MeanActiveTPS: 22.5,
				},
			},
			TPSSequenceLimit: 9, TPSCurrentSequences: 9, TPSPostAdmitSequences: 10,
		},
	})
	for _, secret := range []string{"request-123", "user prompt", "Bearer secret", "api-key", "public.example"} {
		if strings.Contains(line, secret) {
			t.Fatalf("admission log exposed %q: %s", secret, line)
		}
	}
	for _, required := range []string{
		"action=protect", "reason=kv_capacity", "scope=load", "enforced=true",
		"maximum_sequence_input_tokens=900",
		"base_prompt_count=2", "prefill_input_tokens=1500", "prefill_compute_tokens=1200",
		"first_byte_pending_prefill_input_tokens=600",
		"first_byte_pending_prefill_compute_tokens=500", "first_byte_pending_prefill_sequences=3",
		"maximum_sequence_kv_reservation_input_tokens=1000",
		"input_kv_tokens=1600", "first_byte_coverable_input_kv_tokens=400",
		"first_byte_pending_input_kv_tokens=1200", "future_kv_tokens=800",
		"tps_reference=20.000000", "tps_window_ready=true",
		"tps_window_qualified_sequence_samples=4", "tps_sequence_limit=9",
		"tps_current_sequences=9", "tps_post_admit_sequences=10", "tps_unobserved_sequences=2",
		"decode_sequences=4",
	} {
		if !strings.Contains(line, required) {
			t.Fatalf("admission log missing %q: %s", required, line)
		}
	}
}

func TestV01215AdmissionGenerationTPSDoesNotInventOneSequence(t *testing.T) {
	aggregate, mean, meanValid := admissionGenerationTPS(coreadmission.ProjectedState{
		GenerationDelta:     25,
		ObservationInterval: 500 * time.Millisecond,
	})
	if aggregate != 50 || mean != 0 || meanValid {
		t.Fatalf("completion-between-polls TPS aggregate=%v mean=%v valid=%t", aggregate, mean, meanValid)
	}
}
