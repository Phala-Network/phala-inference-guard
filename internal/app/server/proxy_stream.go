package server

import (
	"context"
	"net/http"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/http"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/sse"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/semantic"
)

type upstreamRoundTripResult struct {
	response *http.Response
	err      error
}

func (s *proxyServer) proxyStreamingRequest(backend *backendProxy, w http.ResponseWriter, r *http.Request, allowEarlyBridge bool, requestStarted time.Time) proxyResult {
	done := backend.Begin()
	defer done()
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ProxyTimeout)
	defer cancel()
	started := time.Now()
	resultCh := make(chan upstreamRoundTripResult, 1)
	go func() {
		request := backend.NewUpstreamRequest(ctx, r)
		response, err := backend.RoundTrip(request)
		resultCh <- upstreamRoundTripResult{response: response, err: err}
	}()

	headerTimer := time.NewTimer(sseBridgeHeaderGrace)
	defer headerTimer.Stop()
	wroteEarly := false
	firstByte := time.Duration(0)
	status := http.StatusOK

	select {
	case result := <-resultCh:
		if result.err != nil {
			if s.recordClientDisconnect(r.Context(), clientDisconnectPhaseUpstream, true) {
				return proxyResult{status: clientClosedRequestStatus, total: time.Since(started)}
			}
			s.recordProxyUpstreamError(backend)
			openai.WriteTooManyRequests(w)
			return proxyResult{status: http.StatusTooManyRequests, total: time.Since(started), firstByte: time.Since(started), firstByteOK: true}
		}
		recorder := httpx.NewStatusRecorder(w)
		var copyErr error
		status, copyErr = s.writeUpstreamResponse(ctx, recorder, result.response, true, requestStarted)
		if copyErr != nil {
			if s.recordClientDisconnect(r.Context(), clientDisconnectPhaseResponse, true) {
				status = clientClosedRequestStatus
			} else {
				s.recordProxyCopyError(backend)
			}
		}
		firstByte, firstByteOK := recorder.FirstByteSince(started)
		return proxyResult{status: status, total: time.Since(started), firstByte: firstByte, firstByteOK: firstByteOK}
	case <-headerTimer.C:
		if allowEarlyBridge && s.cfg.SSEEarlyBridgeEnabled && s.streamBridgeAllowed() {
			sse.WriteHeaders(w)
			firstByte = time.Since(started)
			wroteEarly = true
			s.sseBridgeStreams.Add(1)
			s.sseKeepAliveStreams.Add(1)
			if !sse.WriteComment(w, &s.sseKeepAliveComments) {
				status = http.StatusOK
				if s.recordClientDisconnect(r.Context(), clientDisconnectPhaseResponse, true) {
					status = clientClosedRequestStatus
				} else {
					s.sseBridgeCopyErr.Add(1)
					s.recordProxyCopyError(backend)
				}
				return proxyResult{status: status, total: time.Since(started), firstByte: firstByte, firstByteOK: true}
			}
		}
	}

	upstream := <-resultCh
	if upstream.err != nil {
		if s.recordClientDisconnect(r.Context(), clientDisconnectPhaseUpstream, true) {
			return proxyResult{status: clientClosedRequestStatus, total: time.Since(started), firstByte: firstByte, firstByteOK: wroteEarly}
		}
		s.recordProxyUpstreamError(backend)
		if wroteEarly {
			s.sseBridgeUpstreamErr.Add(1)
			return proxyResult{status: http.StatusOK, total: time.Since(started), firstByte: firstByte, firstByteOK: true}
		}
		openai.WriteTooManyRequests(w)
		return proxyResult{status: http.StatusTooManyRequests, total: time.Since(started), firstByte: time.Since(started), firstByteOK: true}
	}
	if !wroteEarly {
		recorder := httpx.NewStatusRecorder(w)
		var copyErr error
		status, copyErr = s.writeUpstreamResponse(ctx, recorder, upstream.response, true, requestStarted)
		if copyErr != nil {
			if s.recordClientDisconnect(r.Context(), clientDisconnectPhaseResponse, true) {
				status = clientClosedRequestStatus
			} else {
				s.recordProxyCopyError(backend)
			}
		}
		firstByte, firstByteOK := recorder.FirstByteSince(started)
		return proxyResult{status: status, total: time.Since(started), firstByte: firstByte, firstByteOK: firstByteOK}
	}
	stopClosingOnCancel := closeBodyOnContextDone(ctx, upstream.response.Body)
	defer stopClosingOnCancel()
	defer upstream.response.Body.Close()
	if !semantic.Eligible(upstream.response, true) {
		s.sseBridgeInvalid.Add(1)
		return proxyResult{status: http.StatusOK, total: time.Since(started), firstByte: firstByte, firstByteOK: true}
	}
	semanticTTFT := semantic.New(requestStarted)
	if copyErr := s.copyResponseBody(ctx, w, upstream.response.Body, true, semanticTTFT); copyErr != nil {
		if s.recordClientDisconnect(r.Context(), clientDisconnectPhaseResponse, true) {
			status = clientClosedRequestStatus
		} else {
			s.sseBridgeCopyErr.Add(1)
			s.recordProxyCopyError(backend)
		}
	}
	return proxyResult{status: status, total: time.Since(started), firstByte: firstByte, firstByteOK: true}
}
