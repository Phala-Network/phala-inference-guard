package httpx

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type StatusRecorder struct {
	http.ResponseWriter
	status       int
	headerAt     time.Time
	firstWriteAt time.Time
}

func NewStatusRecorder(w http.ResponseWriter) *StatusRecorder {
	return &StatusRecorder{ResponseWriter: w}
}

func (r *StatusRecorder) markHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	if r.headerAt.IsZero() {
		r.headerAt = time.Now()
	}
}

func (r *StatusRecorder) markFirstWrite() {
	if r.firstWriteAt.IsZero() {
		r.firstWriteAt = time.Now()
	}
}

func (r *StatusRecorder) WriteHeader(status int) {
	r.markHeader(status)
	r.ResponseWriter.WriteHeader(status)
}

func (r *StatusRecorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.markHeader(http.StatusOK)
	}
	r.markFirstWrite()
	return r.ResponseWriter.Write(body)
}

func (r *StatusRecorder) Flush() {
	if r.status == 0 {
		r.markHeader(http.StatusOK)
	}
	r.markFirstWrite()
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (r *StatusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying response writer does not support hijacking")
	}
	return hijacker.Hijack()
}

func (r *StatusRecorder) ReadFrom(src io.Reader) (int64, error) {
	if r.status == 0 {
		r.markHeader(http.StatusOK)
	}
	r.markFirstWrite()
	if readerFrom, ok := r.ResponseWriter.(io.ReaderFrom); ok {
		return readerFrom.ReadFrom(src)
	}
	return io.Copy(r.ResponseWriter, src)
}

func (r *StatusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *StatusRecorder) FirstByteSince(started time.Time) (time.Duration, bool) {
	if r == nil {
		return 0, false
	}
	if !r.firstWriteAt.IsZero() {
		return r.firstWriteAt.Sub(started), true
	}
	if !r.headerAt.IsZero() {
		return r.headerAt.Sub(started), true
	}
	return 0, false
}

func (r *StatusRecorder) StatusOrOK() int {
	if r == nil || r.status == 0 {
		return http.StatusOK
	}
	return r.status
}
