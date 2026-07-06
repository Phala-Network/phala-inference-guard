package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCopyResponseCancelsUpstreamBodyWhenClientContextCancels(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := newBlockingReadCloser()
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       body,
	}
	srv := &proxyServer{}
	errCh := make(chan error, 1)

	go func() {
		errCh <- srv.copyResponseWithOptionalKeepAlive(ctx, httptest.NewRecorder(), response, true, time.Now())
	}()

	cancel()

	select {
	case <-body.closed:
	case <-time.After(time.Second):
		t.Fatal("upstream body was not closed after client context cancellation")
	}

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("copy error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("copy did not return after client context cancellation")
	}
}

func TestProxyRequestRecordsResponseDisconnectOnBodyCopyAbort(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("chunk"))
	}))
	defer backend.Close()
	srv, err := newProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	clientCtx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(clientCtx)
	writer := &disconnectingResponseWriter{header: make(http.Header), cancel: cancel}

	result := srv.proxyRequest(srv.backends[0], writer, request)

	if result.status != clientClosedRequestStatus {
		t.Fatalf("status=%d want %d", result.status, clientClosedRequestStatus)
	}
	if got := srv.clientDisconnectResponse.Load(); got != 1 {
		t.Fatalf("clientDisconnectResponse=%d want 1", got)
	}
	if got := srv.clientDisconnectCancel.Load(); got != 1 {
		t.Fatalf("clientDisconnectCancel=%d want 1", got)
	}
	if got := srv.proxyCopyErr.Load(); got != 0 {
		t.Fatalf("proxyCopyErr=%d want 0", got)
	}
	if got := srv.proxyUpstreamErr.Load(); got != 0 {
		t.Fatalf("proxyUpstreamErr=%d want 0", got)
	}
}

func TestServeHTTPRecordsQueueDisconnectWithoutReject(t *testing.T) {
	started := make(chan struct{})
	releaseBackend := make(chan struct{})
	var startedOnce sync.Once
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedOnce.Do(func() { close(started) })
		<-releaseBackend
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.GlobalLimit = 1
	cfg.QoSQueueWait = time.Second
	cfg.QoSQueuePoll = time.Millisecond
	srv, err := newProxyServer(cfg)
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}

	firstRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	firstRequest.Header.Set("Authorization", "Bearer secret")
	firstRequest.Header.Set("Content-Type", "application/json")
	firstDone := make(chan struct{})
	go func() {
		srv.ServeHTTP(httptest.NewRecorder(), firstRequest)
		close(firstDone)
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("first request did not reach backend")
	}

	clientCtx, cancel := context.WithCancel(context.Background())
	secondRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`)).WithContext(clientCtx)
	secondRequest.Header.Set("Authorization", "Bearer secret")
	secondRequest.Header.Set("Content-Type", "application/json")
	cancel()

	srv.ServeHTTP(httptest.NewRecorder(), secondRequest)

	if got := srv.clientDisconnectQueue.Load(); got != 1 {
		t.Fatalf("clientDisconnectQueue=%d want 1", got)
	}
	if got := srv.total429.Load(); got != 0 {
		t.Fatalf("total429=%d want 0", got)
	}

	close(releaseBackend)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first request did not finish")
	}
}

func TestStreamingResponseDisconnectDoesNotCountProxyCopyError(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"chunk\"}}]}\n\n"))
	}))
	defer backend.Close()
	srv, err := newProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	clientCtx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[],"stream":true}`)).WithContext(clientCtx)
	writer := &disconnectingResponseWriter{header: make(http.Header), cancel: cancel}

	result := srv.proxyStreamingRequest(srv.backends[0], writer, request, false, time.Now())

	if result.status != clientClosedRequestStatus {
		t.Fatalf("status=%d want %d", result.status, clientClosedRequestStatus)
	}
	if got := srv.clientDisconnectResponse.Load(); got != 1 {
		t.Fatalf("clientDisconnectResponse=%d want 1", got)
	}
	if got := srv.clientDisconnectCancel.Load(); got != 1 {
		t.Fatalf("clientDisconnectCancel=%d want 1", got)
	}
	if got := srv.proxyCopyErr.Load(); got != 0 {
		t.Fatalf("proxyCopyErr=%d want 0", got)
	}
	if got := srv.sseBridgeCopyErr.Load(); got != 0 {
		t.Fatalf("sseBridgeCopyErr=%d want 0", got)
	}
}

func TestAttachedClientDisconnectRecordsOnce(t *testing.T) {
	srv := &proxyServer{}
	clientCtx, cancel := context.WithCancel(context.Background())
	proxyCtx := attachClientContext(context.Background(), clientCtx)
	cancel()

	if !srv.recordClientDisconnect(proxyCtx, clientDisconnectPhaseUpstream, true) {
		t.Fatal("first client disconnect was not recorded")
	}
	if !srv.recordClientDisconnect(proxyCtx, clientDisconnectPhaseResponse, true) {
		t.Fatal("second client disconnect was not recognized")
	}
	if got := srv.clientDisconnectUpstream.Load(); got != 1 {
		t.Fatalf("clientDisconnectUpstream=%d want 1", got)
	}
	if got := srv.clientDisconnectResponse.Load(); got != 0 {
		t.Fatalf("clientDisconnectResponse=%d want 0", got)
	}
	if got := srv.clientDisconnectCancel.Load(); got != 1 {
		t.Fatalf("clientDisconnectCancel=%d want 1", got)
	}
}

type blockingReadCloser struct {
	closed chan struct{}
	once   sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{closed: make(chan struct{})}
}

func (b *blockingReadCloser) Read([]byte) (int, error) {
	<-b.closed
	return 0, io.ErrClosedPipe
}

func (b *blockingReadCloser) Close() error {
	b.once.Do(func() { close(b.closed) })
	return nil
}

type disconnectingResponseWriter struct {
	header http.Header
	cancel context.CancelFunc
}

func (w *disconnectingResponseWriter) Header() http.Header {
	return w.header
}

func (w *disconnectingResponseWriter) WriteHeader(int) {}

func (w *disconnectingResponseWriter) Write([]byte) (int, error) {
	w.cancel()
	return 0, io.ErrClosedPipe
}
