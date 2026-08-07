package server

import (
	"fmt"
	"net/http"
)

const (
	upstreamStatusGreen   = 0
	upstreamStatusYellow  = 1
	upstreamStatusRed     = 2
	upstreamStatusUnknown = 3
)

func (s *proxyServer) upstreamStatus(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintf(w, "%d\n", s.upstreamStatusCode())
}

func (s *proxyServer) upstreamStatusCode() int {
	if s == nil {
		return upstreamStatusUnknown
	}
	provider, ok := s.predictiveShadow.(predictiveAdmissionTelemetryProvider)
	if !ok {
		if s.cfg.PredictiveAdmissionMode == "enforce" {
			return upstreamStatusRed
		}
		return upstreamStatusUnknown
	}
	snapshot := provider.PredictiveAdmissionTelemetry()
	if s.cfg.PredictiveAdmissionMode == "shadow" {
		if snapshot.Observer.MetricsFresh && snapshot.Observer.IdentityValid {
			return upstreamStatusGreen
		}
		return upstreamStatusUnknown
	}
	backpressure := snapshot.RouterBackpressure
	if !backpressure.Active {
		return upstreamStatusGreen
	}
	if backpressure.Scope == predictiveProtectionScopeLoad && backpressure.InspectCapacity > 0 {
		return upstreamStatusYellow
	}
	return upstreamStatusRed
}
