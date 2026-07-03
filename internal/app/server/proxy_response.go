package server

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/http"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/sse"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/semantic"
)

func (s *proxyServer) copyResponseBody(ctx context.Context, w http.ResponseWriter, body io.Reader, streaming bool, semanticTTFT *semantic.Observer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, readErr := body.Read(buffer)
		if err := ctx.Err(); err != nil {
			return err
		}
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
				if err := ctx.Err(); err != nil {
					return err
				}
				return writeErr
			}
			if streaming {
				if flusher, ok := w.(http.Flusher); ok {
					flusher.Flush()
				}
			}
			if semanticTTFT != nil {
				if found, limited := semanticTTFT.Observe(buffer[:n]); found {
					s.observeSemanticTTFT(semanticTTFT)
					semanticTTFT = nil
				} else if limited {
					s.semanticTTFTLimited.Add(1)
					semanticTTFT = nil
				}
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return nil
			}
			return readErr
		}
	}
}

func (s *proxyServer) copyResponseWithOptionalKeepAlive(ctx context.Context, w http.ResponseWriter, response *http.Response, streaming bool, started time.Time) error {
	body := response.Body
	var semanticTTFT *semantic.Observer
	if semantic.Eligible(response, streaming) {
		semanticTTFT = semantic.New(started)
	}
	if streaming && s.shouldInjectSSEKeepAlive(response) {
		body = sse.NewKeepAliveBody(response.Body, sseKeepAliveInterval, &s.sseKeepAliveComments)
		s.sseKeepAliveStreams.Add(1)
	}
	stopClosingOnCancel := closeBodyOnContextDone(ctx, body)
	defer stopClosingOnCancel()
	defer body.Close()
	return s.copyResponseBody(ctx, w, body, streaming, semanticTTFT)
}

func closeBodyOnContextDone(ctx context.Context, body io.Closer) func() {
	if ctx == nil || body == nil {
		return func() {}
	}
	done := make(chan struct{})
	var once sync.Once
	go func() {
		select {
		case <-ctx.Done():
			_ = body.Close()
		case <-done:
		}
	}()
	return func() {
		once.Do(func() { close(done) })
	}
}

func (s *proxyServer) writeUpstreamResponse(ctx context.Context, w http.ResponseWriter, response *http.Response, streaming bool, semanticStarted time.Time) (int, error) {
	s.classifyUpstreamErrorResponse(response)
	httpx.CopyHeader(w.Header(), response.Header)
	if streaming && s.shouldInjectSSEKeepAlive(response) {
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(response.StatusCode)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return response.StatusCode, s.copyResponseWithOptionalKeepAlive(ctx, w, response, streaming, semanticStarted)
}

func (s *proxyServer) recordProxyUpstreamError(backend *backendProxy) {
	s.proxyUpstreamErr.Add(1)
	if backend != nil {
		backend.ObserveProxyError()
	}
}

func (s *proxyServer) recordProxyCopyError(backend *backendProxy) {
	s.proxyCopyErr.Add(1)
	if backend != nil {
		backend.ObserveCopyError()
	}
}
