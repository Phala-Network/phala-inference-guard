package server

import (
	"sync"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveShadowPendingPrefillSnapshot struct {
	Count                   int
	Tokens                  int64
	Features                runtimepredictive.SchedulerFeatures
	FeaturesValid           bool
	DecisionManagerSequence uint64
	Exploratory             bool
	EventSequence           uint64
}

type predictiveShadowPendingPrefillStore struct {
	mu            sync.Mutex
	maximum       int
	active        map[*predictiveShadowPendingPrefillHandle]runtimepredictive.PendingPrefillObservation
	eventSequence uint64
}

type predictiveShadowPendingPrefillHandle struct {
	owner *predictiveShadowPendingPrefillStore
}

func newPredictiveShadowPendingPrefillStore(maximum int) *predictiveShadowPendingPrefillStore {
	if maximum == 0 {
		maximum = defaultMaximumShadowObservations
	}
	return &predictiveShadowPendingPrefillStore{
		maximum: maximum,
		active:  make(map[*predictiveShadowPendingPrefillHandle]runtimepredictive.PendingPrefillObservation),
	}
}

func (s *predictiveShadowPendingPrefillStore) Begin(observation runtimepredictive.PendingPrefillObservation) *predictiveShadowPendingPrefillHandle {
	if s == nil || observation.Tokens <= 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.maximum <= 0 || len(s.active) >= s.maximum {
		return nil
	}
	handle := &predictiveShadowPendingPrefillHandle{owner: s}
	s.active[handle] = observation
	s.eventSequence++
	return handle
}

func (h *predictiveShadowPendingPrefillHandle) End() bool {
	if h == nil || h.owner == nil {
		return false
	}
	s := h.owner
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.active[h]; !ok {
		return false
	}
	delete(s.active, h)
	s.eventSequence++
	return true
}

func (s *predictiveShadowPendingPrefillStore) Snapshot() predictiveShadowPendingPrefillSnapshot {
	if s == nil {
		return predictiveShadowPendingPrefillSnapshot{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := predictiveShadowPendingPrefillSnapshot{
		Count:         len(s.active),
		EventSequence: s.eventSequence,
	}
	for _, observation := range s.active {
		result.Tokens = addPredictivePendingPrefillTokens(result.Tokens, observation.Tokens)
		if result.Count == 1 {
			result.Features = observation.Features
			result.FeaturesValid = true
			result.DecisionManagerSequence = observation.DecisionManagerSequence
			result.Exploratory = observation.Exploratory
		}
	}
	if result.Count != 1 {
		result.Features = runtimepredictive.SchedulerFeatures{}
		result.FeaturesValid = false
		result.DecisionManagerSequence = 0
		result.Exploratory = false
	}
	return result
}

func (s *predictiveShadowPendingPrefillStore) Clear() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	count := len(s.active)
	if count == 0 {
		return 0
	}
	clear(s.active)
	s.eventSequence++
	return count
}

func addPredictivePendingPrefillTokens(left, right int64) int64 {
	if right <= 0 {
		return left
	}
	const maximumInt64 = int64(^uint64(0) >> 1)
	if left > maximumInt64-right {
		return maximumInt64
	}
	return left + right
}
