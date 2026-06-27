package sse

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var keepAliveComment = []byte(": keep-alive\n\n")

type readResult struct {
	data []byte
	err  error
}

type KeepAliveBody struct {
	src      io.ReadCloser
	interval time.Duration
	comments *atomic.Uint64
	results  chan readResult
	done     chan struct{}
	once     sync.Once
	pending  []byte
}

func EligibleResponse(response *http.Response) bool {
	if response == nil || response.Body == nil || response.StatusCode != http.StatusOK {
		return false
	}
	contentType := strings.ToLower(response.Header.Get("Content-Type"))
	return strings.Contains(contentType, "text/event-stream")
}

func NewKeepAliveBody(src io.ReadCloser, interval time.Duration, comments *atomic.Uint64) *KeepAliveBody {
	body := &KeepAliveBody{
		src:      src,
		interval: interval,
		comments: comments,
		results:  make(chan readResult, 1),
		done:     make(chan struct{}),
	}
	go body.readLoop()
	return body
}

func (b *KeepAliveBody) readLoop() {
	buffer := make([]byte, 32*1024)
	for {
		n, err := b.src.Read(buffer)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buffer[:n])
			if !b.send(readResult{data: chunk}) {
				return
			}
		}
		if err != nil {
			_ = b.send(readResult{err: err})
			return
		}
	}
}

func (b *KeepAliveBody) send(result readResult) bool {
	select {
	case b.results <- result:
		return true
	case <-b.done:
		return false
	}
}

func (b *KeepAliveBody) Read(p []byte) (int, error) {
	if len(b.pending) > 0 {
		n := copy(p, b.pending)
		b.pending = b.pending[n:]
		return n, nil
	}
	select {
	case result := <-b.results:
		if len(result.data) > 0 {
			n := copy(p, result.data)
			if n < len(result.data) {
				b.pending = result.data[n:]
			}
			return n, nil
		}
		return 0, result.err
	case <-time.After(b.interval):
		b.comments.Add(1)
		n := copy(p, keepAliveComment)
		if n < len(keepAliveComment) {
			b.pending = keepAliveComment[n:]
		}
		return n, nil
	case <-b.done:
		return 0, io.ErrClosedPipe
	}
}

func (b *KeepAliveBody) Close() error {
	var err error
	b.once.Do(func() {
		close(b.done)
		err = b.src.Close()
	})
	return err
}

func WriteHeaders(w http.ResponseWriter) {
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("X-Accel-Buffering", "no")
	header.Del("Content-Length")
	w.WriteHeader(http.StatusOK)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func WriteComment(w http.ResponseWriter, comments *atomic.Uint64) bool {
	if _, err := w.Write(keepAliveComment); err != nil {
		return false
	}
	comments.Add(1)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return true
}
