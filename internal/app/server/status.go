package server

import (
	"log"
	"time"

	statusview "github.com/Phala-Network/phala-inference-guard/internal/observability/status"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
)

func (s *proxyServer) statusLogLine() string {
	snapshot := dynamic.Snapshot{}
	if s.dynamicController == nil {
		snapshot = dynamic.Snapshot{}
	} else {
		snapshot = s.dynamicController.Snapshot()
	}
	tierSnapshot := s.qosGate.TierSnapshot(snapshot.GlobalLimit)
	return statusview.Format(statusview.Input{
		Version:            version,
		Snapshot:           snapshot,
		QueueCurrent:       s.qosGate.QueueCurrent(),
		DynamicRejected:    s.qosGate.DynamicRejected(),
		BackendUnavailable: s.backendUnavailable.Load(),
		Tier: statusview.TierSnapshot{
			BasicInflight:   tierSnapshot.BasicInflight,
			BasicWaiting:    tierSnapshot.BasicWaiting,
			BasicLimit:      tierSnapshot.BasicLimit,
			PremiumInflight: tierSnapshot.PremiumInflight,
			PremiumWaiting:  tierSnapshot.PremiumWaiting,
			PremiumReserved: tierSnapshot.PremiumReserved,
		},
		Backends: s.statusBackendSnapshots(),
	})
}

func (s *proxyServer) statusBackendSnapshots() []statusview.BackendSnapshot {
	backends := make([]statusview.BackendSnapshot, 0, len(s.backends))
	for _, backend := range s.backends {
		backendStatus := backend.Status()
		backends = append(backends, statusview.BackendSnapshot{
			Name:      backend.Name(),
			Running:   backendStatus.Running,
			Waiting:   backendStatus.Waiting,
			Inflight:  backend.Inflight(),
			TTFTValid: backendStatus.TTFTValid,
			TTFTP95:   backendStatus.TTFTP95,
			TTFTP99:   backendStatus.TTFTP99,
			Failed:    backendStatus.Failed,
		})
	}
	return backends
}

func (s *proxyServer) statusLogLoop() {
	if s.cfg.StatusLogInterval <= 0 {
		return
	}
	ticker := time.NewTicker(s.cfg.StatusLogInterval)
	defer ticker.Stop()
	for range ticker.C {
		log.Print(s.statusLogLine())
	}
}
