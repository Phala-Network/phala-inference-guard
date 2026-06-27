package server

import (
	"net/http"
	"time"

	qospolicy "github.com/Phala-Network/phala-inference-guard/internal/domain/qos"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

func (s *proxyServer) currentQoSLimit() (limit int, dynamic bool, rejectCode string) {
	if s.cfg.DynamicEnabled && s.cfg.DynamicEnforce {
		limit := 0
		if s.dynamicController != nil {
			limit = s.dynamicController.GlobalLimit()
		}
		if limit <= 0 {
			if s.dynamicController != nil && s.dynamicController.BackendUnavailableActive() {
				return 0, true, "backend_unavailable"
			}
			return 0, true, "global_dynamic_limit"
		}
		return limit, true, ""
	}
	return s.globalLn.Limit(), false, ""
}

func (s *proxyServer) effectiveQoSQueueWait(code string) time.Duration {
	configured := s.cfg.QoSQueueWait
	if configured <= 0 || code == "backend_unavailable" {
		return 0
	}
	wait := num.MinDuration(configured, maxQoSQueueWait)
	if s.dynamicController == nil {
		return wait
	}
	snapshot := s.dynamicController.Snapshot()
	if snapshot.Source != "metrics" {
		return wait
	}
	return qospolicy.EffectiveQueueWait(qospolicy.QueueWaitInput{
		Configured:   configured,
		Max:          maxQoSQueueWait,
		Severe:       severeDynamicQueueWait,
		Saturated:    saturatedDynamicQueueWait,
		Code:         code,
		State:        snapshot.DecisionState(),
		RedReasons:   snapshot.DecisionRedReasons(),
		KVCacheUsage: snapshot.KVCacheUsage,
		KVRed:        s.cfg.DynamicKVRed,
		GlobalLimit:  snapshot.GlobalLimit,
		Running:      snapshot.Running,
		Waiting:      snapshot.Waiting,
		WaitingRed:   s.cfg.DynamicWaitingRed,
	})
}

func (s *proxyServer) prefillGraceDuration(r *http.Request) time.Duration {
	return qospolicy.PrefillGrace(qospolicy.PrefillGraceInput{
		Enabled:        s.cfg.DynamicUserTPSEnabled,
		Min:            s.cfg.DynamicUserTPSGraceMin,
		Max:            s.cfg.DynamicUserTPSGraceMax,
		BodyBytes:      r.ContentLength,
		BytesPerSec:    s.cfg.DynamicUserTPSGraceBps,
		BodyMultiplier: s.cfg.DynamicUserTPSGraceMul,
	})
}

func (s *proxyServer) trackActiveRequest(prefillGrace time.Duration) func() {
	if s.activeRequests == nil {
		return func() {}
	}
	id := s.nextActiveID.Add(1)
	s.activeRequests.Add(id, time.Now().Add(prefillGrace))
	return func() {
		s.activeRequests.Remove(id)
	}
}

func (s *proxyServer) unavailable(w http.ResponseWriter, code string) {
	s.backendUnavailable.Add(1)
	openai.WriteTooManyRequests(w)
}

func (s *proxyServer) currentCapacityRatio() float64 {
	if s.dynamicController == nil {
		return s.cfg.DynamicUserTPSCapacityRatio
	}
	return s.dynamicController.CapacityRatio()
}
