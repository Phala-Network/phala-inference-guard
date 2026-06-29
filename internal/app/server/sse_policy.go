package server

import (
	"net/http"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/sse"
)

func (s *proxyServer) streamBridgeAllowed() bool {
	if !s.cfg.DynamicEnabled {
		return true
	}
	if s.dynamicController == nil {
		return true
	}
	snapshot := s.dynamicController.Snapshot()
	if snapshot.Source != "metrics" {
		return false
	}
	return snapshot.DecisionState() == "green" && snapshot.Waiting == 0 && snapshot.BackendFailed == 0 && snapshot.KVCacheUsage < sseKeepAliveMaxKVCacheUsage && s.qosGate.QueueCurrent() == 0
}

func (s *proxyServer) modifyBackendResponse(response *http.Response) error {
	s.classifyUpstreamErrorResponse(response)
	if !s.shouldInjectSSEKeepAlive(response) {
		return nil
	}
	response.Body = sse.NewKeepAliveBody(response.Body, sseKeepAliveInterval, &s.sseKeepAliveComments)
	response.ContentLength = -1
	response.Header.Del("Content-Length")
	s.sseKeepAliveStreams.Add(1)
	return nil
}

func (s *proxyServer) shouldInjectSSEKeepAlive(response *http.Response) bool {
	if !s.cfg.SSEKeepAliveEnabled || !sse.EligibleResponse(response) {
		return false
	}
	if !s.cfg.DynamicEnabled {
		return true
	}
	if s.dynamicController == nil {
		return true
	}
	snapshot := s.dynamicController.Snapshot()
	if snapshot.Source != "metrics" {
		return false
	}
	if snapshot.DecisionState() != "green" || snapshot.Waiting != 0 || snapshot.BackendFailed != 0 {
		return false
	}
	if snapshot.KVCacheUsage >= sseKeepAliveMaxKVCacheUsage {
		return false
	}
	return s.qosGate.QueueCurrent() == 0
}
