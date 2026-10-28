package server

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type blockingDeadlineWriter struct {
	*httptest.ResponseRecorder
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
	deadline time.Time
}

func (w *blockingDeadlineWriter) SetWriteDeadline(deadline time.Time) error {
	w.once.Do(func() { close(w.entered); <-w.release })
	w.deadline = deadline
	return nil
}

func TestHTTPIOCancellationCleanupJoinsCallback(t *testing.T) {
	underlying := &blockingDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	writer := newResponseDeadlineWriter(underlying, time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	stop := writer.interruptOnCancel(ctx)
	cancel()
	<-underlying.entered
	cleaned := make(chan struct{})
	go func() { stop(); close(cleaned) }()
	select {
	case <-cleaned:
		t.Fatal("cleanup returned while the cancellation callback was still running")
	case <-time.After(20 * time.Millisecond):
	}
	close(underlying.release)
	select {
	case <-cleaned:
	case <-time.After(time.Second):
		t.Fatal("cleanup did not join the completed callback")
	}
	// A successful/late operation cannot clear or renew an interrupted write.
	writer.setDeadline(false)
	if underlying.deadline.IsZero() || underlying.deadline.After(time.Now()) {
		t.Fatal("an interrupted response's deadline was revived")
	}
}

func TestHTTPIOConnectionStatesForgetTerminalConnections(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	var states connectionStates
	for _, terminal := range []http.ConnState{http.StateClosed, http.StateHijacked} {
		for _, state := range []http.ConnState{http.StateNew, http.StateActive, http.StateIdle, http.StateActive, terminal} {
			states.observe(server, state)
		}
		if got := states.snapshot(); got != [3]int64{} || len(states.live) != 0 {
			t.Fatalf("terminal connection retained: states=%v entries=%d", got, len(states.live))
		}
	}
}
