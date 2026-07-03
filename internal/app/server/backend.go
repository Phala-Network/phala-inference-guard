package server

import (
	"context"
	"math"
	"net/http"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/http"
)

func (s *proxyServer) chooseBackend() *backendProxy {
	if len(s.backends) == 0 {
		return nil
	}
	now := time.Now()
	var best *backendProxy
	bestScore := math.Inf(1)
	capacityRatio := s.currentCapacityRatio()
	for _, backend := range s.backends {
		status := backend.Status()
		stale := !status.Updated.IsZero() && now.Sub(status.Updated) > 3*s.cfg.DynamicPollInterval
		if s.cfg.DynamicEnabled && (status.Failed || stale) {
			continue
		}
		score := decision.BackendScore(decision.BackendScoreInput{
			Running:            status.Running,
			Waiting:            status.Waiting,
			Inflight:           backend.Inflight(),
			KVCacheUsage:       status.KVCacheUsage,
			GenerationTPS:      status.GenerationTPS,
			GenerationTPSValid: status.GenerationTPSValid,
			TargetTPS:          s.cfg.DynamicUserTPSYellow,
			CapacityRatio:      capacityRatio,
		})
		if best == nil || score < bestScore {
			best = backend
			bestScore = score
		}
	}
	if best != nil {
		return best
	}
	for _, backend := range s.backends {
		score := float64(backend.Inflight())
		if best == nil || score < bestScore {
			best = backend
			bestScore = score
		}
	}
	return best
}

func (s *proxyServer) proxyRequest(backend *backendProxy, w http.ResponseWriter, r *http.Request) (result proxyResult) {
	done := backend.Begin()
	defer done()
	r.Header.Set("X-PIG-Backend", backend.Name())
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.ProxyTimeout)
	defer cancel()
	proxyCtx := attachClientContext(ctx, r.Context())
	started := time.Now()
	recorder := httpx.NewStatusRecorder(w)
	defer func() {
		if recovered := recover(); recovered != nil {
			if recovered == http.ErrAbortHandler && s.recordClientDisconnect(proxyCtx, clientDisconnectPhaseResponse, true) {
				result = proxyResult{status: clientClosedRequestStatus, total: time.Since(started)}
				if firstByte, ok := recorder.FirstByteSince(started); ok {
					result.firstByte = firstByte
					result.firstByteOK = true
				}
				return
			}
			panic(recovered)
		}
	}()
	backend.ServeHTTP(recorder, r.WithContext(proxyCtx))
	result = proxyResult{
		status: recorder.StatusOrOK(),
		total:  time.Since(started),
	}
	if firstByte, ok := recorder.FirstByteSince(started); ok {
		result.firstByte = firstByte
		result.firstByteOK = true
	}
	if s.recordClientDisconnect(proxyCtx, clientDisconnectPhaseResponse, true) {
		result.status = clientClosedRequestStatus
	}
	return result
}
