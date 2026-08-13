package server

import (
	"context"
	"net/http"
	"time"

	httpx "github.com/Phala-Network/phala-inference-guard/internal/infra/http"
)

func (s *proxyServer) proxyRequest(backend *backendProxy, w http.ResponseWriter, r *http.Request) (result proxyResult) {
	if backend == nil {
		return proxyResult{status: http.StatusServiceUnavailable, proxyFailed: true}
	}
	done := backend.Begin()
	defer done()
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ProxyTimeout)
	defer cancel()
	ctx = attachClientContext(ctx, r.Context())
	started := time.Now()
	recorder := httpx.NewStatusRecorder(w)
	defer func() {
		if recovered := recover(); recovered != nil {
			if recovered == http.ErrAbortHandler && s.recordClientDisconnect(ctx, clientDisconnectPhaseResponse, true) {
				result = proxyResult{status: clientClosedRequestStatus, total: time.Since(started)}
				if firstByte, ok := recorder.FirstByteSince(started); ok {
					result.firstByte, result.firstByteOK = firstByte, true
				}
				return
			}
			panic(recovered)
		}
	}()
	backend.ServeHTTP(recorder, r.WithContext(ctx))
	result.status = recorder.StatusOrOK()
	result.total = time.Since(started)
	if firstByte, ok := recorder.FirstByteSince(started); ok {
		result.firstByte, result.firstByteOK = firstByte, true
	}
	if s.recordClientDisconnect(ctx, clientDisconnectPhaseResponse, true) {
		result.status = clientClosedRequestStatus
	}
	result.timedOut = ctx.Err() == context.DeadlineExceeded
	return result
}
