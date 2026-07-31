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
	phase string
}

func (s *failureInjectingPredictiveShadow) Close() error {
	return nil
}

func (s *failureInjectingPredictiveShadow) DecideAndReserve(_ context.Context, _ string, input predictiveShadowInput) predictiveShadowReservation {
	s.retainedBody = input.Body
	if s.phase == "decide" {
		panic("injected predictive decide panic")
	}
	return &failureInjectingPredictiveReservation{phase: s.phase}
}

func (r *failureInjectingPredictiveReservation) MarkPrefillComplete() bool {
	if r.phase == "semantic" {
		panic("injected predictive semantic panic")
	}
	return true
}

func (r *failureInjectingPredictiveReservation) Terminate(runtimepredictive.TerminalCause) bool {
	if r.phase == "terminal" {
		panic("injected predictive terminal panic")
	}
	return true
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

func TestPredictiveShadowScrubsEphemeralBodyAfterDecision(t *testing.T) {
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
	if len(shadow.retainedBody) == 0 {
		t.Fatal("fake did not retain the supplied body view")
	}
	for i, value := range shadow.retainedBody {
		if value != 0 {
			t.Fatalf("retained raw body byte %d = %d, want scrubbed zero", i, value)
		}
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
