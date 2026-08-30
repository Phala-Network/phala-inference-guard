package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

func TestAdmissionHTTPForwardsRequestAndResponseBytesUnchanged(t *testing.T) {
	requestBody := []byte(`{"model":"model-agnostic","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`)
	responseBody := []byte(`{"id":"completion","choices":[{"message":{"content":"ok"}}]}`)
	var received []byte
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(responseBody)
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	if response.Code != http.StatusOK ||
		!bytes.Equal(received, requestBody) ||
		!bytes.Equal(response.Body.Bytes(), responseBody) {
		t.Fatalf(
			"proxy bytes changed: status=%d request=%q response=%q",
			response.Code,
			received,
			response.Body.Bytes(),
		)
	}
}

func TestAdmissionHTTPRequestSizeDoesNotChangeTPSDecision(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)

	small := serveAdmissionRequest(t, srv, "small")
	smallDecision := runtime.Snapshot(clock.Now()).Report.LastDecision
	large := serveAdmissionRequest(t, srv, strings.Repeat("a", 64*1024))
	largeDecision := runtime.Snapshot(clock.Now()).Report.LastDecision

	if small.Code != http.StatusOK ||
		large.Code != http.StatusOK ||
		backendCalls.Load() != 2 ||
		!smallDecision.Admitted() ||
		!largeDecision.Admitted() {
		t.Fatalf(
			"request size changed TPS admission: small=%d/%+v large=%d/%+v calls=%d",
			small.Code,
			smallDecision,
			large.Code,
			largeDecision,
			backendCalls.Load(),
		)
	}
	if smallDecision.Demand.DecodeSequences != largeDecision.Demand.DecodeSequences {
		t.Fatalf("equal fanout produced different TPS demand: small=%+v large=%+v", smallDecision, largeDecision)
	}
}

func TestAdmissionHTTPChargesCompleteFanoutBeforeForward(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", WindowConcurrency: 4,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	body := `{"model":"model-agnostic","messages":[{"role":"user","content":"hello"}],"n":8,"max_tokens":256}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	decision := runtime.Snapshot(clock.Now()).Report.LastDecision
	if response.Code != http.StatusTooManyRequests ||
		backendCalls.Load() != 0 ||
		decision.Reason != coreadmission.ReasonWindowConcurrency ||
		decision.Demand.DecodeSequences != 8 ||
		decision.ProjectedRunning != 8 ||
		decision.ProjectedWindowSequences != 8 ||
		decision.WindowConcurrency != 4 ||
		decision.ReservationID != 0 {
		t.Fatalf(
			"fanout was not charged atomically before forward: status=%d calls=%d decision=%+v",
			response.Code,
			backendCalls.Load(),
			decision,
		)
	}
}

func TestAdmissionHTTPEnforceProtectionIsOpenAICompatibleAndObservable(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", TPSReference: 50, Waiting: 1, WindowConcurrency: 1,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	body := `{"model":"model-agnostic","messages":[{"role":"user","content":"hello"}],"n":8,"max_tokens":8}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	var payload map[string]any
	if response.Code != http.StatusTooManyRequests ||
		backendCalls.Load() != 0 ||
		json.Unmarshal(response.Body.Bytes(), &payload) != nil ||
		payload["error"] == nil {
		t.Fatalf(
			"enforced TPS protection response=%d calls=%d body=%q",
			response.Code,
			backendCalls.Load(),
			response.Body.String(),
		)
	}
	snapshot := runtime.Snapshot(clock.Now())
	if snapshot.Report.Attempts != 1 ||
		!snapshot.Report.HasLastReject ||
		snapshot.Report.LastReject.Reason != coreadmission.ReasonTPSReference ||
		srv.predictiveEnforcedRejects.Load() != 1 {
		t.Fatalf("enforced protection telemetry=%+v rejects=%d", snapshot, srv.predictiveEnforcedRejects.Load())
	}
}

func TestAdmissionHTTPShadowProtectionForwardsWithoutReservation(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "shadow", TPSReference: 50, Waiting: 1, WindowConcurrency: 1,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "shadow", runtime)
	body := `{"model":"model-agnostic","messages":[{"role":"user","content":"hello"}],"n":8,"max_tokens":8}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	snapshot := runtime.Snapshot(clock.Now())
	if response.Code != http.StatusOK ||
		backendCalls.Load() != 1 ||
		snapshot.Report.ShadowProtectedForwards != 1 ||
		snapshot.Report.LastDecision.Admitted() ||
		snapshot.Capacity.State.LiveReservations != 0 ||
		snapshot.Capacity.State.ResidualDebts != 0 ||
		srv.predictiveEnforcedRejects.Load() != 0 {
		t.Fatalf(
			"shadow lifecycle status=%d calls=%d snapshot=%+v enforced=%d",
			response.Code,
			backendCalls.Load(),
			snapshot,
			srv.predictiveEnforcedRejects.Load(),
		)
	}
}

func TestAdmissionHTTPSuccessReconcilesFirstByteAndTerminalExactlyOnce(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	tracked := &trackingAdmissionService{
		delegate: runtime,
		terminal: make(chan coreadmission.TerminalCause, 2),
	}
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", tracked)

	response := serveAdmissionRequest(t, srv, "success")

	if response.Code != http.StatusOK || response.Body.String() != "ok" {
		t.Fatalf("successful response status=%d body=%q", response.Code, response.Body.String())
	}
	requireTrackedTerminal(t, tracked, coreadmission.TerminalSuccess)
	if tracked.forwarded.Load() != 1 ||
		tracked.firstByte.Load() != 1 ||
		tracked.terminalAttempts.Load() != 2 ||
		tracked.successful.Load() != 1 {
		t.Fatalf(
			"successful lifecycle forwarded=%d first_byte=%d terminal_attempts=%d successful=%d",
			tracked.forwarded.Load(),
			tracked.firstByte.Load(),
			tracked.terminalAttempts.Load(),
			tracked.successful.Load(),
		)
	}
	state := controller.Snapshot(clock.Now()).State
	if state.LiveReservations != 0 || state.ResidualDebts != 0 || state.SequenceLiabilities != 0 {
		t.Fatalf("successful lifecycle leaked state=%+v", state)
	}
}

func TestAdmissionHTTPProtocolErrorDoesNotReachControllerOrUpstream(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"model-agnostic","messages":[`),
	)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest ||
		backendCalls.Load() != 0 ||
		runtime.Snapshot(clock.Now()).Report.Attempts != 0 {
		t.Fatalf(
			"invalid JSON status=%d calls=%d report=%+v",
			response.Code,
			backendCalls.Load(),
			runtime.Snapshot(clock.Now()).Report,
		)
	}
}

func TestAdmissionHTTPClientCancellationTerminatesAsDisconnect(t *testing.T) {
	started := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		select {
		case <-request.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer backend.Close()
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	tracked := &trackingAdmissionService{
		delegate: runtime,
		terminal: make(chan coreadmission.TerminalCause, 2),
	}
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", tracked)
	requestContext, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"model-agnostic","messages":[{"role":"user","content":"cancel"}],"max_tokens":8}`),
	).WithContext(requestContext)
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
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled request did not return")
	}

	requireTrackedTerminal(t, tracked, coreadmission.TerminalDisconnect)
	if tracked.forwarded.Load() != 1 || tracked.firstByte.Load() != 0 {
		t.Fatalf("cancel lifecycle forwarded=%d first_byte=%d", tracked.forwarded.Load(), tracked.firstByte.Load())
	}
	state := controller.Snapshot(clock.Now()).State
	if state.LiveReservations != 0 ||
		state.ResidualDebts != 1 ||
		state.SequenceLiabilities != 1 {
		t.Fatalf("cancelled lifecycle state=%+v", state)
	}
}

func TestAdmissionDecisionLogContainsOnlyBoundedTPSDiagnostics(t *testing.T) {
	line := admissionDecisionLogLine(admissionDecisionLogEvent{
		Mode:     "enforce",
		Enforced: true,
		Decision: coreadmission.DecisionRecord{
			Action: coreadmission.ActionProtect,
			Reason: coreadmission.ReasonTPSReference,
			Scope:  coreadmission.ProtectionLoad,
			State: coreadmission.ProjectedState{
				RawRunning: 8,
				RawWaiting: 1,
				TPS: coreadmission.TPSSnapshot{
					Enabled:       true,
					Ready:         true,
					Reference:     25,
					MeanActiveTPS: 27.5,
				},
			},
			TPSDecisionResult:        coreadmission.TPSDecisionResultProtect,
			TPSDecisionSubreason:     coreadmission.TPSDecisionSubreasonWaiting,
			ProjectedRunning:         10,
			RunningLimit:             192,
			RunningLimitSource:       coreadmission.RunningLimitSourceAdmin,
			ProjectedWindowSequences: 2,
			WindowConcurrency:        48,
		},
	})
	for _, secret := range []string{"request-123", "user prompt", "Bearer secret", "api-key", "public.example"} {
		if strings.Contains(line, secret) {
			t.Fatalf("admission log exposed %q: %s", secret, line)
		}
	}
	for _, required := range []string{
		"level=warn",
		"component=admission",
		"event=protection",
		"reason=tps_reference",
		"scope=load",
		"tps_result=protect",
		"tps_subreason=waiting",
		"backend=8/1",
		"tps=27.500/25.000",
		"ready=true",
		"projected_running=10",
		"running_limit=192",
		"running_limit_source=admin",
		"projected_window=2",
		"window_concurrency=48",
	} {
		if !strings.Contains(line, required) {
			t.Fatalf("admission log missing %q: %s", required, line)
		}
	}
	for _, retired := range []string{"prefill", "kv_tokens", "cache_", "input_tokens", "sequences=", "leases="} {
		if strings.Contains(line, retired) {
			t.Fatalf("admission log retained retired field %q: %s", retired, line)
		}
	}
	if len(line) > 640 {
		t.Fatalf("default admission log is not compact: bytes=%d line=%s", len(line), line)
	}
}
