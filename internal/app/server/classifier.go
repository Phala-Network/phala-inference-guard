package server

import (
	"net/http"

	"github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/output"
)

func (s *proxyServer) admittedPath(r *http.Request) bool {
	return s.requestClassifier.AdmittedPath(r)
}

func (s *proxyServer) safeForEarlySSEBridge(r *http.Request, outputTokens int, hasOutputTokens bool) bool {
	return s.requestClassifier.SafeForEarlySSEBridge(r, outputTokens, hasOutputTokens)
}

func (s *proxyServer) classifyRequest(r *http.Request) request.Classification {
	return s.requestClassifier.ClassifyRequest(r)
}

func (s *proxyServer) effectiveOutputThresholds() output.Thresholds {
	return s.requestClassifier.EffectiveOutputThresholds()
}

func (s *proxyServer) outputSampleCount() int {
	return s.requestClassifier.OutputSampleCount()
}

func (s *proxyServer) currentDynamicState() string {
	if s.dynamicController == nil {
		return "green"
	}
	return s.dynamicController.State()
}
