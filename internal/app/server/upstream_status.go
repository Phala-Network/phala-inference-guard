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
	if s == nil || s.qosGate == nil || s.globalLn == nil {
		return upstreamStatusUnknown
	}
	limit, _, rejectCode := s.currentQoSLimit()
	if rejectCode != "" || limit <= 0 {
		return upstreamStatusRed
	}
	if s.qosGate.QueueCurrent() > 0 {
		return upstreamStatusRed
	}
	if s.dynamicController != nil {
		if s.dynamicController.BackendUnavailableActive() {
			return upstreamStatusRed
		}
		snapshot := s.dynamicController.Snapshot()
		if snapshot.Waiting > 0 {
			return upstreamStatusRed
		}
		switch snapshot.DecisionState() {
		case "red":
			return upstreamStatusRed
		case "yellow":
			return upstreamStatusYellow
		}
	}

	inflight := s.globalLn.Inflight()
	if inflight >= int64(limit) {
		return upstreamStatusRed
	}
	tierSnapshot := s.qosGate.TierSnapshot(limit)
	if tierSnapshot.BasicWaiting > 0 || tierSnapshot.PremiumWaiting > 0 {
		return upstreamStatusRed
	}
	if tierSnapshot.BasicLimit > 0 && tierSnapshot.BasicInflight >= int64(tierSnapshot.BasicLimit) {
		return upstreamStatusRed
	}
	if upstreamStatusNearLimit(inflight, int64(limit)) ||
		(tierSnapshot.BasicLimit > 0 && upstreamStatusNearLimit(tierSnapshot.BasicInflight, int64(tierSnapshot.BasicLimit))) {
		return upstreamStatusYellow
	}
	return upstreamStatusGreen
}

func upstreamStatusNearLimit(used, limit int64) bool {
	if limit <= 0 {
		return false
	}
	if limit-used <= 1 {
		return true
	}
	return used*100 >= limit*85
}
