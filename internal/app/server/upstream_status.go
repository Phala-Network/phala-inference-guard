package server

import (
	"fmt"
	"net/http"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
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
	if s.admission == nil {
		if s.cfg.PredictiveAdmissionMode == "enforce" {
			return upstreamStatusRed
		}
		return upstreamStatusUnknown
	}
	now := time.Now()
	snapshot := s.admissionTelemetry(now)
	if s.cfg.PredictiveAdmissionMode == "shadow" {
		if capacityObservationFresh(snapshot.Capacity, now) {
			return upstreamStatusGreen
		}
		return upstreamStatusUnknown
	}
	projection := projectAdmissionCapacity(s.cfg.PredictiveAdmissionMode, snapshot.Capacity, snapshot.Report)
	if !projection.Active {
		return upstreamStatusGreen
	}
	if projection.Scope == coreadmission.ProtectionLoad {
		return upstreamStatusYellow
	}
	return upstreamStatusRed
}

func capacityObservationFresh(capacity coreadmission.CapacitySnapshot, now time.Time) bool {
	observation := capacity.Observation
	return capacity.IntakeOpen && capacity.HasObservation && !now.IsZero() &&
		!now.Before(observation.ObservedAt) && now.Sub(observation.ObservedAt) <= observation.MaximumAge
}
