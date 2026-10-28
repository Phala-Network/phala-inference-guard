package server

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Use the production HTTP server configuration and real TCP sockets. A recorder
// cannot reproduce Go's automatic body drain or a blocked socket Write/Flush.
func startHTTPIOTest(t *testing.T, upstream http.Handler, proxyTimeout time.Duration) (string, <-chan struct{}) {
	t.Helper()
	backend := httptest.NewServer(upstream)
	t.Cleanup(backend.Close)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Listen, cfg.Upstream = "127.0.0.1:0", backend.URL
	cfg.PredictiveMetricsURL = backend.URL + "/metrics"
	cfg.Token, cfg.APIAuthEnabled, cfg.AttestationEnabled = "secret", true, false
	cfg.ProxyTimeout = proxyTimeout
	proxy, err := newTestProxyServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proxy.Close() })
	server := newHTTPServer(proxy)
	closed := make(chan struct{}, 16)
	previous := server.ConnState
	server.ConnState = func(conn net.Conn, state http.ConnState) {
		if previous != nil {
			previous(conn, state)
		}
		if state == http.StateClosed {
			closed <- struct{}{}
		}
	}
	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() { _ = server.Close(); <-done })
	return listener.Addr().String(), closed
}

func rawHTTPIOTest(t *testing.T, address, request string) *net.TCPConn {
	t.Helper()
	conn, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	tcp := conn.(*net.TCPConn)
	t.Cleanup(func() { _ = tcp.Close() })
	if err := tcp.SetReadBuffer(1024); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(tcp, request); err != nil {
		t.Fatal(err)
	}
	return tcp
}

func requireHTTPIOClosed(t *testing.T, closed <-chan struct{}, timeout time.Duration) {
	t.Helper()
	select {
	case <-closed:
	case <-time.After(timeout):
		t.Fatal("server retained the live client socket beyond its I/O deadline")
	}
}

func TestHTTPIOIncompleteBodiesExpire(t *testing.T) {
	t.Setenv("HTTP_BODY_READ_TIMEOUT_SECONDS", "1")
	t.Setenv("HTTP_WRITE_IDLE_TIMEOUT_SECONDS", "10")
	for _, fixture := range []struct{ name, head string }{
		{"authenticated_classifier", "POST /v1/chat/completions HTTP/1.1\r\nAuthorization: Bearer secret\r\n"},
		{"unauthorized_auto_drain", "POST /v1/chat/completions HTTP/1.1\r\n"},
		{"local_health_auto_drain", "POST /healthz HTTP/1.1\r\n"},
		{"unknown_route_auto_drain", "POST /not-public HTTP/1.1\r\n"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			address, closed := startHTTPIOTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}), 10*time.Second)
			conn := rawHTTPIOTest(t, address, fixture.head+"Host: fixture\r\nContent-Type: application/json\r\nContent-Length: 1024\r\n\r\n{")
			// Read the response if one is emitted; the client keeps the incomplete
			// upload open, so only the server's own deadline can release it.
			go func() { _, _ = io.Copy(io.Discard, conn) }()
			requireHTTPIOClosed(t, closed, 2500*time.Millisecond)
		})
	}
}

func TestHTTPIOBlockedDownstreamExpires(t *testing.T) {
	t.Setenv("HTTP_BODY_READ_TIMEOUT_SECONDS", "1")
	for _, fixture := range []struct {
		name, writeIdle string
		proxyTimeout    time.Duration
	}{
		{"write_idle", "1", 10 * time.Second},
		{"proxy_context", "10", time.Second},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			t.Setenv("HTTP_WRITE_IDLE_TIMEOUT_SECONDS", fixture.writeIdle)
			started := make(chan struct{})
			address, closed := startHTTPIOTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				close(started)
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Length", "134217728")
				block := bytes.Repeat([]byte("x"), 65536)
				for i := 0; i < 2048; i++ {
					if _, err := w.Write(block); err != nil {
						return
					}
				}
			}), fixture.proxyTimeout)
			rawHTTPIOTest(t, address, "GET /v1/models HTTP/1.1\r\nHost: fixture\r\nAuthorization: Bearer secret\r\n\r\n")
			select {
			case <-started:
			case <-time.After(2 * time.Second):
				t.Fatal("upstream response did not start")
			}
			requireHTTPIOClosed(t, closed, 2500*time.Millisecond)
		})
	}
}

func TestHTTPIOLongSSESurvivesReadAndWriteBudgets(t *testing.T) {
	t.Setenv("HTTP_BODY_READ_TIMEOUT_SECONDS", "1")
	t.Setenv("HTTP_WRITE_IDLE_TIMEOUT_SECONDS", "1")
	address, _ := startHTTPIOTest(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		// No socket write is pending during prefill or the silent interval.
		time.Sleep(1200 * time.Millisecond)
		for i := 0; i < 10; i++ {
			_, _ = fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			time.Sleep(150 * time.Millisecond)
		}
		time.Sleep(1200 * time.Millisecond)
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		w.(http.Flusher).Flush()
	}), 10*time.Second)
	request, err := http.NewRequest(http.MethodPost, "http://"+address+"/v1/chat/completions", strings.NewReader(`{"model":"fixture","messages":[],"stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer secret")
	client := &http.Client{Timeout: 8 * time.Second}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || !bytes.Contains(body, []byte("data: [DONE]")) {
		t.Fatalf("long SSE truncated: status=%d read=%v body=%q", response.StatusCode, err, body)
	}
}

func TestHTTPIOKeepAliveNextRequest(t *testing.T) {
	t.Setenv("HTTP_WRITE_IDLE_TIMEOUT_SECONDS", "1")
	t.Setenv("HTTP_IDLE_TIMEOUT_SECONDS", "10")
	address, _ := startHTTPIOTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), time.Second)
	request := "GET /v1/models HTTP/1.1\r\nHost: fixture\r\nAuthorization: Bearer secret\r\n\r\n"
	conn := rawHTTPIOTest(t, address, request)
	reader := bufio.NewReader(conn)
	for i := 0; i < 2; i++ {
		if i > 0 {
			// Outlive the previous proxy deadline and write budget on the same TCP
			// connection. A late AfterFunc must not poison this next request.
			time.Sleep(1200 * time.Millisecond)
			if _, err := io.WriteString(conn, request); err != nil {
				t.Fatal(err)
			}
		}
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		response, err := http.ReadResponse(reader, nil)
		if err != nil {
			t.Fatalf("request %d failed: %v", i+1, err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || response.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Fatalf("request %d status=%d read=%v body=%q", i+1, response.StatusCode, err, body)
		}
	}
}

func TestHTTPIOIdleKeepAliveExpires(t *testing.T) {
	t.Setenv("HTTP_IDLE_TIMEOUT_SECONDS", "1")
	address, closed := startHTTPIOTest(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}), 10*time.Second)
	conn := rawHTTPIOTest(t, address, "GET /v1/models HTTP/1.1\r\nHost: fixture\r\nAuthorization: Bearer secret\r\n\r\n")
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	requireHTTPIOClosed(t, closed, 2500*time.Millisecond)
}
