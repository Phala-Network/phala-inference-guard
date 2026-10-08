package server

import (
	"context"
	"net/http"
	"net/url"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

// readiness checks backend health and usable telemetry, not spare capacity.
// It is unauthenticated so Router health checks can use its HTTP status.
func (s *proxyServer) readiness(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	capacity := s.admissionTelemetry(now).Capacity
	decision := capacity.MinimumDecision
	validDecision := decision.Admitted() ||
		(decision.Action == coreadmission.ActionProtect && decision.Scope == coreadmission.ProtectionLoad)
	if !capacityObservationFresh(capacity, now) || !validDecision || !s.backendHealthy(r.Context()) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *proxyServer) backendHealthy(ctx context.Context) bool {
	if s == nil || s.backend == nil {
		return false
	}
	target, err := url.Parse(s.backend.Upstream())
	if err != nil {
		return false
	}
	target.Path, target.RawPath, target.RawQuery, target.Fragment = "/health", "", "", ""
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return false
	}
	// RoundTrip reuses the backend transport without following redirects or
	// forwarding caller headers. The response body never becomes public output.
	response, err := s.backend.RoundTrip(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	return response.StatusCode == http.StatusOK
}
