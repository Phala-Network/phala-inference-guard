package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunServerCancellationDuringStartup(t *testing.T) {
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer metrics.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if err := runServer(ctx, testProxyConfig(metrics.URL)); err != nil {
		t.Fatalf("normal startup shutdown returned fatal error: %v", err)
	}
}

func TestServeHTTPContextCancellationAndBindFailure(t *testing.T) {
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := serveHTTPContext(ctx, &http.Server{Addr: "127.0.0.1:0"}); err != nil {
			t.Fatalf("canceled HTTP server returned fatal error: %v", err)
		}
	})
	t.Run("bind_failure", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer listener.Close()
		if err := serveHTTPContext(context.Background(), &http.Server{Addr: listener.Addr().String()}); err == nil {
			t.Fatal("bind error swallowed")
		}
	})
}
