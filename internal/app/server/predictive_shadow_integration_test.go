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
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type recordingPredictiveShadow struct {
	mu         sync.Mutex
	inputs     []predictiveShadowInput
	requests   []*recordingPredictiveReservation
	mutateBody bool
	closeCalls int
}

type recordingPredictiveReservation struct {
	mu                      sync.Mutex
	forwarded               int
	semantic                int
	semanticTTFT            []time.Duration
	completions             []predictiveCompletionObservation
	completionAfterSemantic []bool
	causes                  []runtimepredictive.TerminalCause
}

type rejectingPredictiveAdmission struct {
	mu     sync.Mutex
	calls  int
	inputs []predictiveShadowInput
}

func (a *rejectingPredictiveAdmission) DecideAndReserve(_ context.Context, _ string, input predictiveShadowInput) predictiveShadowReservation {
	a.mu.Lock()
	a.calls++
	owned := input
	owned.Body = append([]byte(nil), input.Body...)
	a.inputs = append(a.inputs, owned)
	a.mu.Unlock()
	return nil
}

func (a *rejectingPredictiveAdmission) Close() error { return nil }

func (a *rejectingPredictiveAdmission) Snapshot() (int, predictiveShadowInput) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(a.inputs) == 0 {
		return a.calls, predictiveShadowInput{}
	}
	return a.calls, a.inputs[len(a.inputs)-1]
}

func (r *recordingPredictiveReservation) MarkForwarded() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.forwarded++
	return r.forwarded == 1
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

func (s *recordingPredictiveShadow) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	return nil
}

func (s *recordingPredictiveShadow) closeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCalls
}

func (r *recordingPredictiveReservation) MarkPrefillComplete() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.semantic++
	return r.semantic == 1
}

func (r *recordingPredictiveReservation) ObserveSemanticTTFT(ttft time.Duration) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.semanticTTFT = append(r.semanticTTFT, ttft)
	return len(r.semanticTTFT) == 1
}

func (r *recordingPredictiveReservation) ObserveCompletion(observation predictiveCompletionObservation) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.completions = append(r.completions, observation)
	r.completionAfterSemantic = append(r.completionAfterSemantic, r.semantic > 0)
	return len(r.completions) == 1
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

func (s *recordingPredictiveShadow) semanticTTFTSnapshot(t *testing.T) []time.Duration {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 1 {
		t.Fatalf("predictive requests = %d, want 1", len(s.requests))
	}
	request := s.requests[0]
	request.mu.Lock()
	defer request.mu.Unlock()
	return append([]time.Duration(nil), request.semanticTTFT...)
}

func (s *recordingPredictiveShadow) completionSnapshot(t *testing.T) []predictiveCompletionObservation {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 1 {
		t.Fatalf("predictive requests = %d, want 1", len(s.requests))
	}
	request := s.requests[0]
	request.mu.Lock()
	defer request.mu.Unlock()
	return append([]predictiveCompletionObservation(nil), request.completions...)
}

func (s *recordingPredictiveShadow) completionOrderingSnapshot(t *testing.T) []bool {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 1 {
		t.Fatalf("predictive requests = %d, want 1", len(s.requests))
	}
	request := s.requests[0]
	request.mu.Lock()
	defer request.mu.Unlock()
	return append([]bool(nil), request.completionAfterSemantic...)
}

func (s *recordingPredictiveShadow) forwardedSnapshot(t *testing.T) int {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) != 1 {
		t.Fatalf("predictive requests = %d, want 1", len(s.requests))
	}
	request := s.requests[0]
	request.mu.Lock()
	defer request.mu.Unlock()
	return request.forwarded
}

func TestPredictiveAdmissionOffConstructsAndRunsNoShadow(t *testing.T) {
	for _, mode := range []string{"", "off"} {
		t.Run("mode="+mode, func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"id":"ok"}`))
			}))
			defer backend.Close()
			constructed := 0
			cfg := testProxyConfig(backend.URL)
			cfg.PredictiveAdmissionMode = mode
			srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
				NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) {
					constructed++
					return nil, fmt.Errorf("disabled mode factory must not run")
				},
			})
			if err != nil {
				t.Fatalf("new disabled server: %v", err)
			}
			if constructed != 0 || srv.predictiveShadow != nil {
				t.Fatalf("disabled mode constructed predictive work: constructed=%d shadow=%T", constructed, srv.predictiveShadow)
			}
			body := `{"model":"m","messages":[{"role":"user","content":"hello"}]}`
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer secret")
			request.Header.Set("Content-Type", "application/json")
			classification := srv.classifyRequest(request)
			if classification.PredictiveBody != nil {
				t.Fatalf("disabled mode retained %d predictive body bytes", len(classification.PredictiveBody))
			}
			recorder := httptest.NewRecorder()
			srv.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK || constructed != 0 {
				t.Fatalf("disabled response/construction = %d/%d, want 200/0", recorder.Code, constructed)
			}
		})
	}
}

func TestPredictiveShadowPreservesUpstreamAndClientBytes(t *testing.T) {
	originalBody := `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`
	type observation struct {
		upstreamBody         string
		upstreamOutputTokens string
		response             *httptest.ResponseRecorder
		shadow               *recordingPredictiveShadow
	}
	run := func(t *testing.T, mode string) observation {
		t.Helper()
		seenBody := ""
		seenOutputTokens := ""
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			seenBody = string(body)
			seenOutputTokens = r.Header.Get("X-PIG-Output-Tokens")
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
		return observation{upstreamBody: seenBody, upstreamOutputTokens: seenOutputTokens, response: recorder, shadow: shadow}
	}

	off := run(t, "off")
	shadow := run(t, "shadow")
	if off.upstreamBody != originalBody || shadow.upstreamBody != originalBody {
		t.Fatalf("upstream body changed: off=%q shadow=%q want=%q", off.upstreamBody, shadow.upstreamBody, originalBody)
	}
	if off.upstreamOutputTokens != shadow.upstreamOutputTokens {
		t.Fatalf("upstream predictive header changed: off=%q shadow=%q", off.upstreamOutputTokens, shadow.upstreamOutputTokens)
	}
	if off.response.Code != shadow.response.Code || off.response.Body.String() != shadow.response.Body.String() {
		t.Fatalf("client response changed: off=%d/%q shadow=%d/%q", off.response.Code, off.response.Body.String(), shadow.response.Code, shadow.response.Body.String())
	}
	if off.response.Header().Get("Content-Type") != shadow.response.Header().Get("Content-Type") || off.response.Header().Get("X-Upstream-Proof") != shadow.response.Header().Get("X-Upstream-Proof") {
		t.Fatalf("client headers changed: off=%v shadow=%v", off.response.Header(), shadow.response.Header())
	}
	input, semantic, causes := shadow.shadow.snapshot(t)
	if input.Body != nil || !input.Cost.Supported || input.Cost.EstimatedInputHigh <= 0 || input.Path != "/v1/chat/completions" || input.OutputTokens != 64 || !input.HasOutputTokens {
		t.Fatalf("predictive input = %+v", input)
	}
	if semantic != 0 || len(causes) != 1 || causes[0] != runtimepredictive.TerminalCompleted {
		t.Fatalf("non-stream lifecycle semantic/causes = %d/%v", semantic, causes)
	}
	if forwarded := shadow.shadow.forwardedSnapshot(t); forwarded != 1 {
		t.Fatalf("non-stream forward lifecycle = %d, want 1", forwarded)
	}
}

func TestPredictiveEnforceRejectsRiskBeforeAnyUpstreamAction(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		_, _ = w.Write([]byte(`{"id":"must-not-run"}`))
	}))
	defer backend.Close()
	admission := &rejectingPredictiveAdmission{}
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return admission, nil },
	})
	if err != nil {
		t.Fatalf("new enforcing server: %v", err)
	}
	defer srv.Close()
	body := `{"model":"m","messages":[{"role":"user","content":"protect before upstream"}],"max_tokens":64}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("enforcing risk status = %d body=%q, want 429", recorder.Code, recorder.Body.String())
	}
	if backendCalls != 0 {
		t.Fatalf("enforcing risk reached upstream %d times", backendCalls)
	}
	if got := srv.predictiveEnforcedRejects.Load(); got != 1 {
		t.Fatalf("predictive enforced rejects = %d, want 1", got)
	}
	if got := srv.total429.Load(); got != 1 {
		t.Fatalf("total 429 = %d, want 1", got)
	}
	calls, input := admission.Snapshot()
	if calls != 1 || input.Body != nil || !input.Cost.Supported || input.Cost.EstimatedInputHigh <= 0 || input.Path != "/v1/chat/completions" || input.OutputTokens != 64 || !input.HasOutputTokens {
		t.Fatalf("enforcing prediction calls/input = %d/%+v", calls, input)
	}
}

func TestPredictiveEnforceRejectsUnscannableBodyBeforeAnyUpstreamAction(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		_, _ = w.Write([]byte(`{"id":"must-not-run"}`))
	}))
	defer backend.Close()
	admission := &rejectingPredictiveAdmission{}
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	cfg.JSONClassifyBodyBytes = 8
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return admission, nil },
	})
	if err != nil {
		t.Fatalf("new enforcing server: %v", err)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"larger than the predictive classifier limit"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("unscannable enforce response/backend = %d/%d, want 429/0", recorder.Code, backendCalls)
	}
	if calls, _ := admission.Snapshot(); calls != 0 {
		t.Fatalf("unscannable body invoked predictor %d times, want 0", calls)
	}
	if got := srv.predictiveEnforcedRejects.Load(); got != 1 {
		t.Fatalf("predictive enforced rejects = %d, want 1", got)
	}
}

func TestPredictiveEnforceFitForwardsAndTerminatesExactlyOnce(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer backend.Close()
	admission := &recordingPredictiveShadow{}
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return admission, nil },
	})
	if err != nil {
		t.Fatalf("new enforcing server: %v", err)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"fit"}],"max_tokens":16}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || backendCalls != 1 {
		t.Fatalf("fit enforce response/backend = %d/%d, want 200/1", recorder.Code, backendCalls)
	}
	_, semantic, causes := admission.snapshot(t)
	if semantic != 0 || len(causes) != 1 || causes[0] != runtimepredictive.TerminalCompleted {
		t.Fatalf("fit enforce semantic/causes = %d/%v", semantic, causes)
	}
	if forwarded := admission.forwardedSnapshot(t); forwarded != 1 {
		t.Fatalf("fit enforce forwarded = %d, want 1", forwarded)
	}
	if got := srv.predictiveEnforcedRejects.Load(); got != 0 {
		t.Fatalf("fit enforce rejects = %d, want 0", got)
	}
}

func TestPredictiveEnforceFitIsNotLateRejectedByLegacyDynamicFeedback(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer backend.Close()
	legacyMetrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "legacy feedback unavailable", http.StatusServiceUnavailable)
	}))
	defer legacyMetrics.Close()
	admission := &recordingPredictiveShadow{}
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	cfg.DynamicEnabled = true
	cfg.DynamicEnforce = true
	cfg.DynamicMetricsURL = legacyMetrics.URL
	cfg.DynamicMetricsURLs = []string{legacyMetrics.URL}
	cfg.Backends[0].MetricsURL = legacyMetrics.URL
	cfg.GlobalLimit = 16
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return admission, nil },
	})
	if err != nil {
		t.Fatalf("new enforcing server: %v", err)
	}
	defer srv.Close()
	body := `{"model":"m","messages":[{"role":"user","content":"predictive fit despite stale legacy feedback"}],"max_tokens":16}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || backendCalls != 1 {
		t.Fatalf("predictive fit under legacy dynamic feedback = %d/%d, want 200/1", recorder.Code, backendCalls)
	}
	if got := srv.qosGate.DynamicRejected(); got != 0 {
		t.Fatalf("predictive enforce fit hit legacy dynamic rejection %d times", got)
	}
	if forwarded := admission.forwardedSnapshot(t); forwarded != 1 {
		t.Fatalf("predictive enforce fit forwarded = %d, want 1", forwarded)
	}
}

func TestPredictiveEnforceStillRespectsStaticAbsoluteConcurrencyLimit(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		_, _ = w.Write([]byte(`{"id":"must-not-run"}`))
	}))
	defer backend.Close()
	admission := &recordingPredictiveShadow{}
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	cfg.GlobalLimit = 0
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return admission, nil },
	})
	if err != nil {
		t.Fatalf("new enforcing server: %v", err)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"static absolute cap"}],"max_tokens":16}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("predictive fit above static cap response/backend = %d/%d, want 429/0", recorder.Code, backendCalls)
	}
	_, _, causes := admission.snapshot(t)
	if len(causes) != 1 || causes[0] != runtimepredictive.TerminalLocalQoSReject {
		t.Fatalf("static cap terminal causes = %v, want local QoS reject", causes)
	}
	if got := srv.predictiveEnforcedRejects.Load(); got != 0 {
		t.Fatalf("static cap counted as predictive enforced reject %d times", got)
	}
}

func TestPredictiveShadowRiskPreservesUpstreamBehavior(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"shadow"}`))
	}))
	defer backend.Close()
	admission := &rejectingPredictiveAdmission{}
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "shadow"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return admission, nil },
	})
	if err != nil {
		t.Fatalf("new shadow server: %v", err)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"observe only"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated || backendCalls != 1 || recorder.Body.String() != `{"id":"shadow"}` {
		t.Fatalf("shadow risk response/backend = %d/%d/%q, want 201/1/shadow body", recorder.Code, backendCalls, recorder.Body.String())
	}
	if calls, _ := admission.Snapshot(); calls != 1 {
		t.Fatalf("shadow risk prediction calls = %d, want 1", calls)
	}
	if got := srv.predictiveEnforcedRejects.Load(); got != 0 {
		t.Fatalf("shadow risk enforced rejects = %d, want 0", got)
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
	if forwarded := shadow.forwardedSnapshot(t); forwarded != 0 {
		t.Fatalf("local reject forward lifecycle = %d, want 0", forwarded)
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
	if forwarded := shadow.forwardedSnapshot(t); forwarded != 1 {
		t.Fatalf("stream forward lifecycle = %d, want 1", forwarded)
	}
	ttft := shadow.semanticTTFTSnapshot(t)
	if len(ttft) != 1 || ttft[0] <= 0 {
		t.Fatalf("stream attributed semantic TTFT = %v, want one positive observation", ttft)
	}
}

func TestPredictiveShadowObservesFinalStreamingUsageForTPS(t *testing.T) {
	response := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":8,\"completion_tokens\":5,\"total_tokens\":13},\"metrics\":{\"generation_time_ms\":80,\"mean_itl_ms\":20,\"tokens_per_second\":25}}\n\n" +
		"data: [DONE]\n\n"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(response))
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
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true,"stream_options":{"include_usage":true},"max_tokens":8}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != response {
		t.Fatalf("stream response changed: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	observations := shadow.completionSnapshot(t)
	if len(observations) != 1 {
		t.Fatalf("completion observations = %v, want one final usage observation", observations)
	}
	observation := observations[0]
	if observation.CompletionTokens != 5 || observation.BackendMeanITL != 20*time.Millisecond || observation.BackendGenerationTime != 80*time.Millisecond || observation.ElapsedSinceRequest <= 0 {
		t.Fatalf("completion observation = %+v", observation)
	}
}

func TestPredictiveShadowObservesNonStreamingUsageForTPS(t *testing.T) {
	response := `{"id":"x","object":"chat.completion","choices":[{"message":{"content":"hello"}}],"usage":{"prompt_tokens":8,"completion_tokens":5,"total_tokens":13},"metrics":{"generation_time_ms":80,"mean_itl_ms":20}}`
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(response))
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
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"max_tokens":8}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != response {
		t.Fatalf("non-stream response changed: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	observations := shadow.completionSnapshot(t)
	if len(observations) != 1 || observations[0].CompletionTokens != 5 || observations[0].BackendMeanITL != 20*time.Millisecond || observations[0].BackendGenerationTime != 80*time.Millisecond {
		t.Fatalf("non-stream completion observations = %+v", observations)
	}
}

func TestPredictiveStreamingLocalTPSObservationFollowsSemanticOutputInSameRead(t *testing.T) {
	response := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"completion_tokens\":5}}\n\n" +
		"data: [DONE]\n\n"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(response))
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
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true,"stream_options":{"include_usage":true},"max_tokens":8}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != response {
		t.Fatalf("stream response changed: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	observations := shadow.completionSnapshot(t)
	ordering := shadow.completionOrderingSnapshot(t)
	if len(observations) != 1 || observations[0].CompletionTokens != 5 || observations[0].BackendMeanITL != 0 || len(ordering) != 1 || !ordering[0] {
		t.Fatalf("local observation/order = %+v/%v, want one usage after semantic output", observations, ordering)
	}
}

func TestPredictiveStreamingCopyFailureCannotCompleteTPSOutcome(t *testing.T) {
	response := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"completion_tokens\":5},\"metrics\":{\"generation_time_ms\":80,\"mean_itl_ms\":20}}\n\n"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", len(response)+1))
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, response)
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
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true,"stream_options":{"include_usage":true},"max_tokens":8}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Body.String() != response {
		t.Fatalf("truncated upstream response changed: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if observations := shadow.completionSnapshot(t); len(observations) != 0 {
		t.Fatalf("completion observations = %+v, want no committed usage from a truncated stream", observations)
	}
	_, _, causes := shadow.snapshot(t)
	if len(causes) != 1 || causes[0] != runtimepredictive.TerminalUpstreamFailure {
		t.Fatalf("truncated upstream terminal causes = %v, want upstream failure", causes)
	}
}

func TestPredictiveShadowModeFailsClosedWithoutMetrics(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "shadow"
	if _, err := newProxyServer(cfg); err == nil || !strings.Contains(err.Error(), "one vLLM metrics URL") {
		t.Fatalf("newProxyServer error = %v, want missing-metrics startup failure", err)
	}
}

func TestPredictiveShadowCloseIsIdempotent(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
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
	if err := srv.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := srv.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if got := shadow.closeCount(); got != 1 {
		t.Fatalf("adapter close calls = %d, want 1", got)
	}
}
