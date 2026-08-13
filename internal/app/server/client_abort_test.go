package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
