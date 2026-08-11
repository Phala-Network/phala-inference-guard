package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestRequestAwareHTTPLifecycleMatrixDrainsReservationExactlyOnce(t *testing.T) {
	t.Run("non-streaming completion", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"complete"}`))
		}))
		defer backend.Close()
		srv, manager, recorder := newLifecycleMatrixServer(t, backend.URL, nil)
		defer srv.Close()

		response := serveRequestAwareHTTP(t, srv, "non-streaming")
		if response.Code != http.StatusOK {
			t.Fatalf("non-streaming status=%d body=%q", response.Code, response.Body.String())
		}
		assertLifecycleMatrixDrain(t, manager, recorder, 1, runtimepredictive.TerminalCompleted)
	})

	t.Run("streaming completion", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = w.Write([]byte("data: first\n\n"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			_, _ = w.Write([]byte("data: [DONE]\n\n"))
		}))
		defer backend.Close()
		srv, manager, recorder := newLifecycleMatrixServer(t, backend.URL, nil)
		defer srv.Close()

		response := serveRequestAwareHTTP(t, srv, "streaming")
		if response.Code != http.StatusOK {
			t.Fatalf("streaming status=%d body=%q", response.Code, response.Body.String())
		}
		assertLifecycleMatrixDrain(t, manager, recorder, 1, runtimepredictive.TerminalCompleted)
	})

	t.Run("upstream HTTP error", func(t *testing.T) {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("upstream failed"))
		}))
		defer backend.Close()
		srv, manager, recorder := newLifecycleMatrixServer(t, backend.URL, nil)
		defer srv.Close()

		response := serveRequestAwareHTTP(t, srv, "upstream error")
		if response.Code != http.StatusInternalServerError {
			t.Fatalf("upstream-error status=%d body=%q", response.Code, response.Body.String())
		}
		assertLifecycleMatrixDrain(t, manager, recorder, 1, runtimepredictive.TerminalUpstreamFailure)
	})

	t.Run("upstream transport failure", func(t *testing.T) {
		srv, manager, recorder := newLifecycleMatrixServer(t, "http://127.0.0.1:1", nil)
		defer srv.Close()

		response := serveRequestAwareHTTP(t, srv, "transport failure")
		if response.Code == http.StatusOK {
			t.Fatalf("transport failure unexpectedly returned 200: %q", response.Body.String())
		}
		assertLifecycleMatrixDrain(t, manager, recorder, 0, runtimepredictive.TerminalUpstreamFailure)
	})

	t.Run("timeout", func(t *testing.T) {
		release := make(chan struct{})
		backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}))
		defer func() {
			close(release)
			backend.Close()
		}()
		srv, manager, recorder := newLifecycleMatrixServer(t, backend.URL, func(cfg *config) {
			cfg.ProxyTimeout = 25 * time.Millisecond
		})
		defer srv.Close()

		response := serveRequestAwareHTTP(t, srv, "timeout")
		if response.Code == http.StatusOK {
			t.Fatalf("timeout unexpectedly returned 200: %q", response.Body.String())
		}
		assertLifecycleMatrixDrain(t, manager, recorder, 0, runtimepredictive.TerminalTimeout)
	})

	t.Run("client cancellation", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			close(started)
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}))
		defer func() {
			close(release)
			backend.Close()
		}()
		srv, manager, recorder := newLifecycleMatrixServer(t, backend.URL, nil)
		defer srv.Close()

		clientContext, cancel := context.WithCancel(context.Background())
		request := lifecycleMatrixRequest(t, clientContext, "cancel")
		done := make(chan struct{})
		go func() {
			srv.ServeHTTP(httptest.NewRecorder(), request)
			close(done)
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("cancel test did not reach upstream")
		}
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cancelled request did not return")
		}
		assertLifecycleMatrixDrain(t, manager, recorder, 0, runtimepredictive.TerminalClientDisconnected)
	})

	t.Run("response disconnect", func(t *testing.T) {
		release := make(chan struct{})
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("chunk"))
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			select {
			case <-request.Context().Done():
			case <-release:
			}
		}))
		defer func() {
			close(release)
			backend.Close()
		}()
		srv, manager, recorder := newLifecycleMatrixServer(t, backend.URL, nil)
		defer srv.Close()

		clientContext, cancel := context.WithCancel(context.Background())
		request := lifecycleMatrixRequest(t, clientContext, "disconnect")
		writer := &disconnectingResponseWriter{header: make(http.Header), cancel: cancel}
		srv.ServeHTTP(writer, request)
		assertLifecycleMatrixDrain(t, manager, recorder, 1, runtimepredictive.TerminalClientDisconnected)
	})

	t.Run("adapter close during in-flight request", func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			close(started)
			<-release
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("complete after close"))
		}))
		defer backend.Close()
		srv, manager, recorder := newLifecycleMatrixServer(t, backend.URL, nil)

		request := lifecycleMatrixRequest(t, context.Background(), "close")
		response := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			srv.ServeHTTP(response, request)
			close(done)
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("close test did not reach upstream")
		}
		if snapshot := manager.Snapshot(); snapshot.Reservations != 1 {
			t.Fatalf("in-flight close setup=%+v, want one reservation", snapshot)
		}
		if err := srv.Close(); err != nil {
			t.Fatalf("close in-flight adapter: %v", err)
		}
		close(release)
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("in-flight request did not finish after close")
		}
		if response.Code != http.StatusOK {
			t.Fatalf("post-close response=%d body=%q", response.Code, response.Body.String())
		}
		assertLifecycleMatrixDrain(t, manager, recorder, 1, runtimepredictive.TerminalCompleted)
		if manager.Snapshot().IntakeOpen {
			t.Fatal("closed adapter reopened intake after in-flight drain")
		}
	})
}

func newLifecycleMatrixServer(
	t testing.TB,
	upstream string,
	configure func(*config),
) (*proxyServer, *runtimepredictive.Manager, *lifecycleMatrixShadow) {
	t.Helper()
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "enforce")
	setRequestAwareAdapterObservation(t, adapter, manager, 0, 0, 0)
	srv := newRequestAwareHTTPTestServerWithConfig(t, upstream, adapter, "enforce", configure)
	recorder := &lifecycleMatrixShadow{delegate: adapter}
	srv.predictiveShadow = recorder
	return srv, manager, recorder
}

func lifecycleMatrixRequest(t testing.TB, ctx context.Context, content string) *http.Request {
	t.Helper()
	body := `{"model":"model-agnostic","messages":[{"role":"user","content":"` + content + `"}],"max_tokens":8}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func assertLifecycleMatrixDrain(
	t testing.TB,
	manager *runtimepredictive.Manager,
	recorder *lifecycleMatrixShadow,
	wantPrefill int,
	wantCause runtimepredictive.TerminalCause,
) {
	t.Helper()
	reservations := recorder.reservationSnapshots()
	if len(reservations) != 1 {
		t.Fatalf("recorded reservations=%d, want exactly one", len(reservations))
	}
	reservation := reservations[0]
	if reservation.forwarded != 1 || reservation.prefill != wantPrefill || reservation.terminal != 1 ||
		len(reservation.causes) != 1 || reservation.causes[0] != wantCause {
		t.Fatalf("lifecycle=%+v, want forwarded=1 prefill=%d terminal=1 cause=%s", reservation, wantPrefill, wantCause)
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 ||
		snapshot.ForwardedPendingPrefills != 0 || snapshot.ForwardedPendingPrefillTokens != 0 {
		t.Fatalf("terminal lifecycle did not drain Manager exactly: %+v", snapshot)
	}
}

type lifecycleMatrixShadow struct {
	delegate predictiveAdmissionShadow
	mu       sync.Mutex
	records  []*lifecycleMatrixReservation
}

func (s *lifecycleMatrixShadow) Decide(
	ctx context.Context,
	requestID string,
	input predictiveShadowInput,
) predictiveAdmissionDecision {
	decision := s.delegate.Decide(ctx, requestID, input)
	if decision.Reservation == nil {
		return decision
	}
	record := &lifecycleMatrixReservation{delegate: decision.Reservation}
	s.mu.Lock()
	s.records = append(s.records, record)
	s.mu.Unlock()
	decision.Reservation = record
	return decision
}

func (s *lifecycleMatrixShadow) Close() error { return s.delegate.Close() }

func (s *lifecycleMatrixShadow) reservationSnapshots() []lifecycleMatrixReservationSnapshot {
	s.mu.Lock()
	records := append([]*lifecycleMatrixReservation(nil), s.records...)
	s.mu.Unlock()
	result := make([]lifecycleMatrixReservationSnapshot, 0, len(records))
	for _, record := range records {
		result = append(result, record.snapshot())
	}
	return result
}

type lifecycleMatrixReservation struct {
	delegate predictiveShadowReservation
	mu       sync.Mutex
	forward  int
	prefill  int
	terminal int
	causes   []runtimepredictive.TerminalCause
}

func (r *lifecycleMatrixReservation) MarkForwarded() bool {
	r.mu.Lock()
	r.forward++
	r.mu.Unlock()
	return r.delegate.MarkForwarded()
}

func (r *lifecycleMatrixReservation) MarkPrefillComplete() bool {
	r.mu.Lock()
	r.prefill++
	r.mu.Unlock()
	return r.delegate.MarkPrefillComplete()
}

func (r *lifecycleMatrixReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	r.mu.Lock()
	r.terminal++
	r.causes = append(r.causes, cause)
	r.mu.Unlock()
	return r.delegate.Terminate(cause)
}

func (r *lifecycleMatrixReservation) snapshot() lifecycleMatrixReservationSnapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return lifecycleMatrixReservationSnapshot{
		forwarded: r.forward,
		prefill:   r.prefill,
		terminal:  r.terminal,
		causes:    append([]runtimepredictive.TerminalCause(nil), r.causes...),
	}
}

type lifecycleMatrixReservationSnapshot struct {
	forwarded int
	prefill   int
	terminal  int
	causes    []runtimepredictive.TerminalCause
}
