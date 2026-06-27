package server

import (
	"io"
	"net/http"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/http"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/sse"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/semantic"
)

func (s *proxyServer) copyResponseBody(w http.ResponseWriter, body io.Reader, streaming bool, semanticTTFT *semantic.Observer) error {
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buffer)
		if n > 0 {
			if _, writeErr := w.Write(buffer[:n]); writeErr != nil {
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

func (s *proxyServer) copyResponseWithOptionalKeepAlive(w http.ResponseWriter, response *http.Response, streaming bool, started time.Time) error {
	body := response.Body
	var semanticTTFT *semantic.Observer
	if semantic.Eligible(response, streaming) {
		semanticTTFT = semantic.New(started)
	}
	if streaming && s.shouldInjectSSEKeepAlive(response) {
		body = sse.NewKeepAliveBody(response.Body, sseKeepAliveInterval, &s.sseKeepAliveComments)
		defer body.Close()
		s.sseKeepAliveStreams.Add(1)
	} else {
		defer response.Body.Close()
	}
	return s.copyResponseBody(w, body, streaming, semanticTTFT)
}

func (s *proxyServer) writeUpstreamResponse(w http.ResponseWriter, response *http.Response, streaming bool, semanticStarted time.Time) (int, error) {
	httpx.CopyHeader(w.Header(), response.Header)
	if streaming && s.shouldInjectSSEKeepAlive(response) {
		w.Header().Del("Content-Length")
	}
	w.WriteHeader(response.StatusCode)
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	return response.StatusCode, s.copyResponseWithOptionalKeepAlive(w, response, streaming, semanticStarted)
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
