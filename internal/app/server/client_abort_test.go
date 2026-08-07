package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyRequestRecordsResponseDisconnect(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk"))
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("new proxy server: %v", err)
	}
	defer srv.Close()
	clientCtx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`)).WithContext(clientCtx)
	writer := &disconnectingResponseWriter{header: make(http.Header), cancel: cancel}

	result := srv.proxyRequest(srv.backend, writer, request)

	if result.status != clientClosedRequestStatus {
		t.Fatalf("status=%d want %d", result.status, clientClosedRequestStatus)
	}
	if got := srv.clientDisconnectResponse.Load(); got != 1 {
		t.Fatalf("clientDisconnectResponse=%d want 1", got)
	}
	if got := srv.clientDisconnectCancel.Load(); got != 1 {
		t.Fatalf("clientDisconnectCancel=%d want 1", got)
	}
}

func TestRequestAwareClientCancellationDrainsReservation(t *testing.T) {
	adapter, manager := newRequestAwareHTTPAdapter(t, "enforce")
	started := make(chan struct{})
	releaseBackend := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		select {
		case <-r.Context().Done():
		case <-releaseBackend:
		}
	}))
	defer backend.Close()
	defer close(releaseBackend)
	srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "enforce")
	defer srv.Close()

	clientCtx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-agnostic","messages":[{"role":"user","content":"cancel"}],"max_tokens":8}`)).WithContext(clientCtx)
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
		t.Fatal("request did not reach upstream")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled request did not return")
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 {
		t.Fatalf("cancelled request leaked reservation: %+v", snapshot)
	}
}

type disconnectingResponseWriter struct {
	header http.Header
	cancel context.CancelFunc
}

func (w *disconnectingResponseWriter) Header() http.Header { return w.header }

func (*disconnectingResponseWriter) WriteHeader(int) {}

func (w *disconnectingResponseWriter) Write([]byte) (int, error) {
	w.cancel()
	return 0, io.ErrClosedPipe
}
