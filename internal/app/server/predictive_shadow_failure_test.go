package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type failureInjectingPredictiveShadow struct {
	phase        string
	retainedBody []byte
}

type failureInjectingPredictiveReservation struct {
	phase          string
	terminateCalls int
	terminalCause  runtimepredictive.TerminalCause
}

type fixedDecisionPredictiveShadow struct {
	decision predictiveAdmissionDecision
}

func (s *failureInjectingPredictiveShadow) Close() error {
	return nil
}

func (s *fixedDecisionPredictiveShadow) Close() error { return nil }

func (s *fixedDecisionPredictiveShadow) Decide(context.Context, string, predictiveShadowInput) predictiveAdmissionDecision {
	return s.decision
}

func (s *failureInjectingPredictiveShadow) Decide(_ context.Context, _ string, input predictiveShadowInput) predictiveAdmissionDecision {
	s.retainedBody = input.Body
	if s.phase == "decide" {
		panic("injected predictive decide panic")
	}
	return predictiveAdmissionDecision{
		Outcome:     predictiveAdmissionOutcomeForward,
		Reservation: &failureInjectingPredictiveReservation{phase: s.phase},
	}
}

func (r *failureInjectingPredictiveReservation) MarkPrefillComplete() bool {
	if r.phase == "semantic" {
		panic("injected predictive semantic panic")
	}
	return true
}

func (r *failureInjectingPredictiveReservation) MarkForwarded() bool {
	if r.phase == "forward" {
		panic("injected predictive forward panic")
	}
	if r.phase == "forward_false" {
		return false
	}
	return true
}

func (r *failureInjectingPredictiveReservation) ObserveCompletion(predictiveCompletionObservation) bool {
	if r.phase == "completion" {
		panic("injected predictive completion panic")
	}
	return true
}

func (r *failureInjectingPredictiveReservation) ReleaseResources() bool {
	if r.phase == "resource_release" {
		panic("injected predictive resource release panic")
	}
	return true
}

func (r *failureInjectingPredictiveReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	r.terminateCalls++
	r.terminalCause = cause
	if r.phase == "terminal" {
		panic("injected predictive terminal panic")
	}
	return true
}

func TestGuardedPredictiveCompletionObservationRequiresOwnershipAndIsIdempotent(t *testing.T) {
	underlying := &recordingPredictiveReservation{}
	guarded := &guardedPredictiveReservation{reservation: underlying}
	observation := predictiveCompletionObservation{CompletionTokens: 5, ElapsedSinceRequest: 100 * time.Millisecond, BackendMeanITL: 20 * time.Millisecond}
	if guarded.ObserveCompletion(observation) {
		t.Fatal("completion observation was accepted before forward ownership")
	}
	if !guarded.MarkForwarded() || !guarded.ObserveCompletion(observation) {
		t.Fatal("first owned completion observation was not accepted")
	}
	if guarded.ObserveCompletion(observation) {
		t.Fatal("duplicate completion observation was accepted")
	}
	if !guarded.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("owned reservation did not terminate")
	}
	if guarded.ObserveCompletion(observation) {
		t.Fatal("completion observation was accepted after termination")
	}
	underlying.mu.Lock()
	defer underlying.mu.Unlock()
	if len(underlying.completions) != 1 {
		t.Fatalf("underlying completion observations = %v, want exactly one", underlying.completions)
	}
}

func TestGuardedPredictiveResourceReleaseRequiresOwnershipAndIsIdempotent(t *testing.T) {
	underlying := &failureInjectingPredictiveReservation{}
	guarded := &guardedPredictiveReservation{reservation: underlying}
	if guarded.ReleaseResources() {
		t.Fatal("resource release was accepted before forward ownership")
	}
	if !guarded.MarkForwarded() || !guarded.ReleaseResources() {
		t.Fatal("first owned resource release was not accepted")
	}
	if guarded.ReleaseResources() {
		t.Fatal("duplicate resource release was accepted")
	}
	if !guarded.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("resource-released guarded reservation did not terminate")
	}
	if guarded.ReleaseResources() {
		t.Fatal("resource release was accepted after terminal outcome")
	}
}

func TestGuardedPredictiveSemanticObservationRequiresForwardOwnership(t *testing.T) {
	underlying := &recordingPredictiveReservation{}
	guarded := &guardedPredictiveReservation{reservation: underlying}
	if guarded.ObserveSemanticTTFT(10 * time.Millisecond) {
		t.Fatal("semantic observation was accepted before forward ownership")
	}
	if !guarded.MarkForwarded() || !guarded.ObserveSemanticTTFT(10*time.Millisecond) {
		t.Fatal("first owned semantic observation was not accepted")
	}
	if guarded.ObserveSemanticTTFT(20 * time.Millisecond) {
		t.Fatal("duplicate semantic observation was accepted")
	}
}

func TestGuardedPredictiveFailedForwardNeverGrantsObservationOwnership(t *testing.T) {
	underlying := &failureInjectingPredictiveReservation{phase: "forward_false"}
	guarded := &guardedPredictiveReservation{reservation: underlying}
	if guarded.MarkForwarded() {
		t.Fatal("injected failed forward unexpectedly succeeded")
	}
	if guarded.ObserveSemanticTTFT(10 * time.Millisecond) {
		t.Fatal("failed forward granted semantic observation ownership")
	}
	if guarded.ObserveCompletion(predictiveCompletionObservation{
		CompletionTokens:    5,
		ElapsedSinceRequest: 100 * time.Millisecond,
		BackendMeanITL:      20 * time.Millisecond,
	}) {
		t.Fatal("failed forward granted completion observation ownership")
	}
}

func TestPredictiveShadowDecidePanicDoesNotChangeResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Upstream-Proof", "same")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer backend.Close()

	srv := newFailureInjectingShadowServer(t, backend.URL, &failureInjectingPredictiveShadow{phase: "decide"})
	recorder := servePredictiveFailureRequest(srv, false)
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"id":"ok"}` || recorder.Header().Get("X-Upstream-Proof") != "same" {
		t.Fatalf("decide panic changed response: status=%d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
}

func TestPredictiveEnforceDecidePanicFailsClosedBeforeUpstream(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		_, _ = w.Write([]byte(`{"id":"must-not-run"}`))
	}))
	defer backend.Close()

	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) {
			return &failureInjectingPredictiveShadow{phase: "decide"}, nil
		},
	})
	if err != nil {
		t.Fatalf("new enforcing server: %v", err)
	}
	recorder := servePredictiveFailureRequest(srv, false)
	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("enforce panic response/backend = %d/%d, want 429/0", recorder.Code, backendCalls)
	}
	if got := srv.predictiveShadowFailures.decide.Load(); got != 1 {
		t.Fatalf("predictive decide failures = %d, want 1", got)
	}
	if got := srv.predictiveEnforcedRejects.Load(); got != 1 {
		t.Fatalf("predictive enforced rejects = %d, want 1", got)
	}
}

func TestPredictiveEnforceInvalidTypedResultsFailClosedAndReleaseReservations(t *testing.T) {
	for _, test := range []struct {
		name        string
		outcome     predictiveAdmissionOutcome
		reservation bool
	}{
		{name: "forward_without_reservation", outcome: predictiveAdmissionOutcomeForward},
		{name: "request_reject_with_reservation", outcome: predictiveAdmissionOutcomeRequestReject, reservation: true},
		{name: "load_protection_with_reservation", outcome: predictiveAdmissionOutcomeLoadProtection, reservation: true},
		{name: "availability_protection_with_reservation", outcome: predictiveAdmissionOutcomeAvailabilityProtection, reservation: true},
		{name: "unknown_outcome_with_reservation", outcome: predictiveAdmissionOutcome("unknown"), reservation: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backendCalls := 0
			backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				backendCalls++
			}))
			defer backend.Close()

			var reservation *failureInjectingPredictiveReservation
			decision := predictiveAdmissionDecision{Outcome: test.outcome}
			if test.reservation {
				reservation = &failureInjectingPredictiveReservation{}
				decision.Reservation = reservation
			}
			cfg := testProxyConfig(backend.URL)
			cfg.PredictiveAdmissionMode = "enforce"
			srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
				NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) {
					return &fixedDecisionPredictiveShadow{decision: decision}, nil
				},
			})
			if err != nil {
				t.Fatalf("new enforcing server: %v", err)
			}
			defer srv.Close()

			recorder := servePredictiveFailureRequest(srv, false)
			if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
				t.Fatalf("invalid result response/backend = %d/%d, want 429/0", recorder.Code, backendCalls)
			}
			if got := srv.predictiveShadowFailures.decide.Load(); got != 1 {
				t.Fatalf("invalid result decision failures = %d, want 1", got)
			}
			if got := srv.predictiveEnforcedRejects.Load(); got != 1 {
				t.Fatalf("invalid result enforced rejects = %d, want 1", got)
			}
			if reservation != nil && (reservation.terminateCalls != 1 || reservation.terminalCause != runtimepredictive.TerminalLocalQoSReject) {
				t.Fatalf("invalid result reservation cleanup = calls=%d cause=%s", reservation.terminateCalls, reservation.terminalCause)
			}
			var output strings.Builder
			srv.writeLocalMetrics(&output)
			if !strings.Contains(output.String(), `pig_predictive_admission_failures_total{phase="decide"} 1`) ||
				!strings.Contains(output.String(), "pig_predictive_router_backpressure_active 0") {
				t.Fatalf("invalid request-scoped result metrics are incomplete:\n%s", output.String())
			}
		})
	}
}

func TestPredictiveEnforceForwardCommitPanicFailsClosedBeforeUpstream(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		_, _ = w.Write([]byte(`{"id":"must-not-run"}`))
	}))
	defer backend.Close()

	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) {
			return &failureInjectingPredictiveShadow{phase: "forward"}, nil
		},
	})
	if err != nil {
		t.Fatalf("new enforcing server: %v", err)
	}
	recorder := servePredictiveFailureRequest(srv, false)
	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("enforce forward panic response/backend = %d/%d, want 429/0", recorder.Code, backendCalls)
	}
	if got := srv.predictiveShadowFailures.semantic.Load(); got != 0 {
		t.Fatalf("predictive semantic failures = %d, want 0 for a forward-commit panic", got)
	}
	var metrics strings.Builder
	srv.writeLocalMetrics(&metrics)
	if !strings.Contains(metrics.String(), `pig_predictive_admission_failures_total{phase="forward"} 1`) {
		t.Fatalf("predictive metrics do not classify the commit panic as forward: %s", metrics.String())
	}
	if !strings.Contains(metrics.String(), `pig_predictive_admission_failures_total{phase="forward_rejected"} 1`) {
		t.Fatalf("predictive metrics do not publish the resulting pre-forward rejection: %s", metrics.String())
	}
	if got := srv.predictiveEnforcedRejects.Load(); got != 1 {
		t.Fatalf("predictive enforced rejects = %d, want 1", got)
	}
}

func TestPredictiveEnforceForwardCommitFalseFailsClosedBeforeUpstream(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		_, _ = w.Write([]byte(`{"id":"must-not-run"}`))
	}))
	defer backend.Close()

	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) {
			return &failureInjectingPredictiveShadow{phase: "forward_false"}, nil
		},
	})
	if err != nil {
		t.Fatalf("new enforcing server: %v", err)
	}
	recorder := servePredictiveFailureRequest(srv, false)
	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("enforce false forward response/backend = %d/%d, want 429/0", recorder.Code, backendCalls)
	}
	if got := srv.predictiveShadowFailures.semantic.Load(); got != 0 {
		t.Fatalf("predictive semantic failures = %d, want 0", got)
	}
	if got := srv.predictiveShadowFailures.forward.Load(); got != 0 {
		t.Fatalf("predictive forward panic failures = %d, want 0 for a false return", got)
	}
	if got := srv.predictiveShadowFailures.forwardRejected.Load(); got != 1 {
		t.Fatalf("predictive forward false rejections = %d, want 1", got)
	}
	var metrics strings.Builder
	srv.writeLocalMetrics(&metrics)
	if !strings.Contains(metrics.String(), `pig_predictive_admission_failures_total{phase="forward_rejected"} 1`) {
		t.Fatalf("predictive metrics hide a false forward commit: %s", metrics.String())
	}
	if got := srv.predictiveEnforcedRejects.Load(); got != 1 {
		t.Fatalf("predictive enforced rejects = %d, want 1", got)
	}
}

func TestPredictiveShadowForwardCommitFailureDoesNotChangeResponse(t *testing.T) {
	for _, phase := range []string{"forward", "forward_false"} {
		t.Run(phase, func(t *testing.T) {
			backendCalls := 0
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backendCalls++
				_, _ = w.Write([]byte(`{"id":"ok"}`))
			}))
			defer backend.Close()

			srv := newFailureInjectingShadowServer(t, backend.URL, &failureInjectingPredictiveShadow{phase: phase})
			recorder := servePredictiveFailureRequest(srv, false)
			if recorder.Code != http.StatusOK || recorder.Body.String() != `{"id":"ok"}` || backendCalls != 1 {
				t.Fatalf("forward failure changed response/backend: status=%d body=%q backend=%d", recorder.Code, recorder.Body.String(), backendCalls)
			}
			wantFailures := uint64(0)
			if phase == "forward" {
				wantFailures = 1
			}
			if got := srv.predictiveShadowFailures.forward.Load(); got != wantFailures {
				t.Fatalf("predictive forward panic failures = %d, want %d", got, wantFailures)
			}
			if got := srv.predictiveShadowFailures.semantic.Load(); got != 0 {
				t.Fatalf("predictive semantic failures = %d, want 0", got)
			}
		})
	}
}

func TestPredictiveShadowSemanticPanicDoesNotChangeStreamingResponse(t *testing.T) {
	chunk := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(chunk))
	}))
	defer backend.Close()

	srv := newFailureInjectingShadowServer(t, backend.URL, &failureInjectingPredictiveShadow{phase: "semantic"})
	recorder := servePredictiveFailureRequest(srv, true)
	if recorder.Code != http.StatusOK || recorder.Body.String() != chunk {
		t.Fatalf("semantic panic changed response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestPredictiveShadowCompletionPanicDoesNotChangeStreamingResponse(t *testing.T) {
	response := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"completion_tokens\":5},\"metrics\":{\"mean_itl_ms\":20}}\n\n" +
		"data: [DONE]\n\n"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(response))
	}))
	defer backend.Close()

	srv := newFailureInjectingShadowServer(t, backend.URL, &failureInjectingPredictiveShadow{phase: "completion"})
	recorder := servePredictiveFailureRequest(srv, true)
	if recorder.Code != http.StatusOK || recorder.Body.String() != response {
		t.Fatalf("completion panic changed response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := srv.predictiveShadowFailures.completion.Load(); got != 1 {
		t.Fatalf("predictive completion failures = %d, want 1", got)
	}
	var output strings.Builder
	srv.writeLocalMetrics(&output)
	if !strings.Contains(output.String(), `pig_predictive_admission_failures_total{phase="completion"} 1`) {
		t.Fatalf("completion panic metric missing: %s", output.String())
	}
}

func TestPredictiveShadowResourceReleasePanicDoesNotChangeStreamingResponse(t *testing.T) {
	response := "data: {\"choices\":[],\"usage\":{\"completion_tokens\":5},\"metrics\":{\"mean_itl_ms\":20}}\n\n" +
		"data: [DONE]\n\n"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(response))
	}))
	defer backend.Close()

	srv := newFailureInjectingShadowServer(t, backend.URL, &failureInjectingPredictiveShadow{phase: "resource_release"})
	recorder := servePredictiveFailureRequest(srv, true)
	if recorder.Code != http.StatusOK || recorder.Body.String() != response {
		t.Fatalf("resource release panic changed response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if got := srv.predictiveShadowFailures.resourceRelease.Load(); got != 1 {
		t.Fatalf("predictive resource release failures = %d, want 1", got)
	}
	var output strings.Builder
	srv.writeLocalMetrics(&output)
	if !strings.Contains(output.String(), `pig_predictive_admission_failures_total{phase="resource_release"} 1`) {
		t.Fatalf("resource release panic metric missing: %s", output.String())
	}
}

func TestPredictiveShadowTerminalPanicDoesNotChangeResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer backend.Close()

	srv := newFailureInjectingShadowServer(t, backend.URL, &failureInjectingPredictiveShadow{phase: "terminal"})
	recorder := servePredictiveFailureRequest(srv, false)
	if recorder.Code != http.StatusOK || recorder.Body.String() != `{"id":"ok"}` {
		t.Fatalf("terminal panic changed response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestPredictiveShadowDoesNotExposeOrRetainRequestBody(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer backend.Close()

	shadow := &failureInjectingPredictiveShadow{}
	srv := newFailureInjectingShadowServer(t, backend.URL, shadow)
	recorder := servePredictiveFailureRequest(srv, false)
	if recorder.Code != http.StatusOK {
		t.Fatalf("response status = %d", recorder.Code)
	}
	if shadow.retainedBody != nil {
		t.Fatalf("predictive adapter received a raw request body copy of %d bytes", len(shadow.retainedBody))
	}
}

func TestPredictiveShadowClassifiesProxyDeadlineAsTimeout(t *testing.T) {
	releaseBackend := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-releaseBackend
	}))
	defer backend.Close()
	defer close(releaseBackend)

	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "shadow"
	cfg.ProxyTimeout = 20 * time.Millisecond
	shadow := &recordingPredictiveShadow{}
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return shadow, nil },
	})
	if err != nil {
		t.Fatalf("new shadow server: %v", err)
	}
	recorder := servePredictiveFailureRequest(srv, false)
	if recorder.Code == http.StatusOK {
		t.Fatalf("timed out request unexpectedly returned 200: %q", recorder.Body.String())
	}
	_, _, causes := shadow.snapshot(t)
	if len(causes) != 1 || causes[0] != runtimepredictive.TerminalTimeout {
		t.Fatalf("timeout terminal causes = %v, want [%s]", causes, runtimepredictive.TerminalTimeout)
	}
}

func TestPredictiveShadowFactoryRunsAfterFallibleServerConstruction(t *testing.T) {
	cfg := testProxyConfig("http://127.0.0.1")
	cfg.PredictiveAdmissionMode = "shadow"
	cfg.Backends[0].Upstream = "://invalid"
	constructed := 0
	_, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) {
			constructed++
			return &recordingPredictiveShadow{}, nil
		},
	})
	if err == nil {
		t.Fatal("invalid backend unexpectedly constructed a server")
	}
	if constructed != 0 {
		t.Fatalf("predictive adapter constructed before fallible dependencies: %d", constructed)
	}
}

func newFailureInjectingShadowServer(t *testing.T, upstream string, shadow predictiveAdmissionShadow) *proxyServer {
	t.Helper()
	cfg := testProxyConfig(upstream)
	cfg.PredictiveAdmissionMode = "shadow"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return shadow, nil },
	})
	if err != nil {
		t.Fatalf("new shadow server: %v", err)
	}
	return srv
}

func servePredictiveFailureRequest(srv *proxyServer, streaming bool) *httptest.ResponseRecorder {
	body := `{"model":"m","messages":[]}`
	if streaming {
		body = `{"model":"m","messages":[],"stream":true}`
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	if streaming {
		request.Header.Set("Accept", "text/event-stream")
	}
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	return recorder
}
