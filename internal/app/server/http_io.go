package server

import (
	"context"
	"net"
	"net/http"
	"sync"
	"time"
)

// Bound a pending write, not time spent computing/waiting for the next token.
// Deadlines are cleared between successful operations so silent prefill/SSE
// intervals do not inherit a completed write's deadline.
type responseDeadlineWriter struct {
	http.ResponseWriter
	controller  *http.ResponseController
	timeout     time.Duration
	mu          sync.Mutex
	interrupted bool
}

func newResponseDeadlineWriter(w http.ResponseWriter, timeout time.Duration) *responseDeadlineWriter {
	return &responseDeadlineWriter{ResponseWriter: w, controller: http.NewResponseController(w), timeout: timeout}
}

func (w *responseDeadlineWriter) setDeadline(active bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var deadline time.Time
	if w.interrupted {
		deadline = time.Now()
	} else if active {
		deadline = time.Now().Add(w.timeout)
	}
	_ = w.controller.SetWriteDeadline(deadline)
}

func (w *responseDeadlineWriter) interruptOnCancel(ctx context.Context) func() {
	done := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		defer close(done)
		w.mu.Lock()
		defer w.mu.Unlock()
		w.interrupted = true
		_ = w.controller.SetWriteDeadline(time.Now())
	})
	return func() {
		if !stop() {
			<-done
		}
	}
}

func (w *responseDeadlineWriter) WriteHeader(status int) {
	w.setDeadline(true)
	w.ResponseWriter.WriteHeader(status)
	w.setDeadline(false)
}

func (w *responseDeadlineWriter) Write(body []byte) (int, error) {
	w.setDeadline(true)
	n, err := w.ResponseWriter.Write(body)
	if err == nil {
		w.setDeadline(false)
	}
	return n, err
}

func (w *responseDeadlineWriter) FlushError() error {
	w.setDeadline(true)
	err := w.controller.Flush()
	if err == nil {
		w.setDeadline(false)
	}
	return err
}

func (w *responseDeadlineWriter) Flush() { _ = w.FlushError() }

func (w *responseDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// Keep only live connections and three aggregate states. Terminal states remove
// the entry, so telemetry cardinality is bounded by the actual open sockets.
type connectionStates struct {
	mu     sync.Mutex
	live   map[net.Conn]http.ConnState
	counts [3]int64
}

func (s *connectionStates) observe(conn net.Conn, state http.ConnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if previous, exists := s.live[conn]; exists {
		s.counts[previous]--
		delete(s.live, conn)
	}
	if state == http.StateClosed || state == http.StateHijacked {
		return
	}
	if s.live == nil {
		s.live = make(map[net.Conn]http.ConnState)
	}
	s.live[conn] = state
	s.counts[state]++
}

func (s *connectionStates) snapshot() [3]int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts
}
