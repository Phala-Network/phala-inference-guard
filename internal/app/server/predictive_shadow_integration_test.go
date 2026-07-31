package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type recordingPredictiveShadow struct {
	mu         sync.Mutex
	inputs     []predictiveShadowInput
	requests   []*recordingPredictiveReservation
	mutateBody bool
}

type recordingPredictiveReservation struct {
	mu       sync.Mutex
	semantic int
	causes   []runtimepredictive.TerminalCause
}

func (s *recordingPredictiveShadow) DecideAndReserve(_ context.Context, _ string, input predictiveShadowInput) predictiveShadowReservation {
	request := &recordingPredictiveReservation{}
	s.mu.Lock()
	owned := input
	owned.Body = append([]byte(nil), input.Body...)
	s.inputs = append(s.inputs, owned)
	s.requests = append(s.requests, request)
	mutateBody := s.mutateBody
	s.mu.Unlock()
	if mutateBody && len(input.Body) > 0 {
		input.Body[0] ^= 0xff
	}
	return request
}

func (r *recordingPredictiveReservation) MarkPrefillComplete() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.semantic++
	return r.semantic == 1
}

func (r *recordingPredictiveReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.causes = append(r.causes, cause)
	return len(r.causes) == 1
}

func (s *recordingPredictiveShadow) snapshot(t *testing.T) (predictiveShadowInput, int, []runtimepredictive.TerminalCause) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.inputs) != 1 || len(s.requests) != 1 {
		t.Fatalf("predictive inputs/requests = %d/%d, want 1/1", len(s.inputs), len(s.requests))
	}
	request := s.requests[0]
	request.mu.Lock()
	defer request.mu.Unlock()
	return s.inputs[0], request.semantic, append([]runtimepredictive.TerminalCause(nil), request.causes...)
}

func TestPredictiveAdmissionOffConstructsAndRunsNoShadow(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer backend.Close()
	constructed := 0
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "off"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) {
			constructed++
			return nil, fmt.Errorf("off mode factory must not run")
		},
	})
	if err != nil {
		t.Fatalf("new off server: %v", err)
	}
	if constructed != 0 || srv.predictiveShadow != nil {
		t.Fatalf("off mode constructed predictive work: constructed=%d shadow=%T", constructed, srv.predictiveShadow)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	classification := srv.classifyRequest(request)
	if classification.PredictiveBody != nil {
		t.Fatalf("off mode retained %d predictive body bytes", len(classification.PredictiveBody))
	}
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || constructed != 0 {
		t.Fatalf("off response/construction = %d/%d, want 200/0", recorder.Code, constructed)
	}
}

func TestPredictiveShadowPreservesUpstreamAndClientBytes(t *testing.T) {
	originalBody := `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`
	type observation struct {
		upstreamBody string
		response     *httptest.ResponseRecorder
		shadow       *recordingPredictiveShadow
	}
	run := func(t *testing.T, mode string) observation {
		t.Helper()
		seenBody := ""
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			seenBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Upstream-Proof", "same")
			_, _ = w.Write([]byte(`{"id":"ok","choices":[{"message":{"content":"answer"}}]}`))
		}))
		defer backend.Close()
		cfg := testProxyConfig(backend.URL)
		cfg.PredictiveAdmissionMode = mode
		shadow := &recordingPredictiveShadow{mutateBody: true}
		dependencies := serverDependencies{}
		if mode == "shadow" {
			dependencies.NewPredictiveShadow = func(config) (predictiveAdmissionShadow, error) { return shadow, nil }
		}
		srv, err := newProxyServerWithDependencies(cfg, dependencies)
		if err != nil {
			t.Fatalf("new %s server: %v", mode, err)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(originalBody))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		srv.ServeHTTP(recorder, request)
		return observation{upstreamBody: seenBody, response: recorder, shadow: shadow}
	}

	off := run(t, "off")
	shadow := run(t, "shadow")
	if off.upstreamBody != originalBody || shadow.upstreamBody != originalBody {
		t.Fatalf("upstream body changed: off=%q shadow=%q want=%q", off.upstreamBody, shadow.upstreamBody, originalBody)
	}
	if off.response.Code != shadow.response.Code || off.response.Body.String() != shadow.response.Body.String() {
		t.Fatalf("client response changed: off=%d/%q shadow=%d/%q", off.response.Code, off.response.Body.String(), shadow.response.Code, shadow.response.Body.String())
	}
	if off.response.Header().Get("Content-Type") != shadow.response.Header().Get("Content-Type") || off.response.Header().Get("X-Upstream-Proof") != shadow.response.Header().Get("X-Upstream-Proof") {
		t.Fatalf("client headers changed: off=%v shadow=%v", off.response.Header(), shadow.response.Header())
	}
	input, semantic, causes := shadow.shadow.snapshot(t)
	if string(input.Body) != originalBody || input.Path != "/v1/chat/completions" || input.OutputTokens != 64 || !input.HasOutputTokens {
		t.Fatalf("predictive input = %+v", input)
	}
	if semantic != 0 || len(causes) != 1 || causes[0] != runtimepredictive.TerminalCompleted {
		t.Fatalf("non-stream lifecycle semantic/causes = %d/%v", semantic, causes)
	}
}

func TestPredictiveShadowReleasesLocalQoSRejectBeforeUpstream(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.GlobalLimit = 0
	cfg.PredictiveAdmissionMode = "shadow"
	shadow := &recordingPredictiveShadow{}
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return shadow, nil },
	})
	if err != nil {
		t.Fatalf("new shadow server: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("response/backend calls = %d/%d, want 429/0", recorder.Code, backendCalls)
	}
	_, semantic, causes := shadow.snapshot(t)
	if semantic != 0 || len(causes) != 1 || causes[0] != runtimepredictive.TerminalLocalQoSReject {
		t.Fatalf("local reject lifecycle semantic/causes = %d/%v", semantic, causes)
	}
}

func TestPredictiveShadowMarksSemanticStreamingOutput(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"))
	}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "shadow"
	shadow := &recordingPredictiveShadow{}
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return shadow, nil },
	})
	if err != nil {
		t.Fatalf("new shadow server: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("stream response = %d/%q", recorder.Code, recorder.Body.String())
	}
	_, semantic, causes := shadow.snapshot(t)
	if semantic != 1 || len(causes) != 1 || causes[0] != runtimepredictive.TerminalCompleted {
		t.Fatalf("stream lifecycle semantic/causes = %d/%v", semantic, causes)
	}
}

func TestPredictiveShadowModeFailsClosedWithoutAdapter(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "shadow"
	if _, err := newProxyServer(cfg); err == nil || !strings.Contains(err.Error(), "predictive shadow adapter") {
		t.Fatalf("newProxyServer error = %v, want missing-adapter startup failure", err)
	}
}
