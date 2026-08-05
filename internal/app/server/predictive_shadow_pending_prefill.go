package server

import (
	"math"
	"sync"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveShadowPrefillAttributionState string

const (
	predictiveShadowPrefillAttributionEmpty        predictiveShadowPrefillAttributionState = "empty"
	predictiveShadowPrefillAttributionSingle       predictiveShadowPrefillAttributionState = "single"
	predictiveShadowPrefillAttributionAggregate    predictiveShadowPrefillAttributionState = "aggregate"
	predictiveShadowPrefillAttributionIncompatible predictiveShadowPrefillAttributionState = "incompatible"
)

type predictiveShadowPendingPrefillSnapshot struct {
	Count                   int
	Tokens                  int64
	Features                runtimepredictive.SchedulerFeatures
	FeaturesValid           bool
	DecisionManagerSequence uint64
	Exploratory             bool
	EventSequence           uint64
	AttributionState        predictiveShadowPrefillAttributionState
}

type predictiveShadowPendingPrefillStore struct {
	mu                sync.Mutex
	maximum           int
	active            map[*predictiveShadowPendingPrefillHandle]runtimepredictive.PendingPrefillObservation
	eventSequence     uint64
	sequenceExhausted bool
}

type predictiveShadowPendingPrefillHandle struct {
	owner    *predictiveShadowPendingPrefillStore
	sequence uint64
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
	if s.maximum <= 0 || len(s.active) >= s.maximum || !s.advanceEventSequenceLocked() {
		return nil
	}
	handle := &predictiveShadowPendingPrefillHandle{owner: s, sequence: s.eventSequence}
	s.active[handle] = observation
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
	s.advanceEventSequenceLocked()
	return true
}

func (s *predictiveShadowPendingPrefillStore) Snapshot() predictiveShadowPendingPrefillSnapshot {
	if s == nil {
		return predictiveShadowPendingPrefillSnapshot{AttributionState: predictiveShadowPrefillAttributionEmpty}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := predictiveShadowPendingPrefillSnapshot{
		Count:            len(s.active),
		EventSequence:    s.eventSequence,
		AttributionState: predictiveShadowPrefillAttributionEmpty,
	}
	if result.Count == 0 {
		return result
	}

	var latestHandle *predictiveShadowPendingPrefillHandle
	for handle, observation := range s.active {
		result.Tokens = addPredictivePendingPrefillTokens(result.Tokens, observation.Tokens)
		if latestHandle == nil || handle.sequence > latestHandle.sequence {
			latestHandle = handle
		}
	}
	if s.sequenceExhausted {
		result.AttributionState = predictiveShadowPrefillAttributionIncompatible
		return result
	}
	latest := s.active[latestHandle]
	if result.Count == 1 {
		// Preserve the pre-v0.10.13 single-observation contract exactly. The
		// observation was already validated when it was extracted from a real
		// admission result; injected fixtures also historically passed through
		// unchanged. Structural compatibility is needed only before combining
		// multiple independently predicted shadow requests.
		result.Features = latest.Features
		result.FeaturesValid = true
		result.DecisionManagerSequence = latest.DecisionManagerSequence
		result.Exploratory = latest.Exploratory
		result.AttributionState = predictiveShadowPrefillAttributionSingle
		return result
	}

	features, managerSequence, ok := aggregatePredictiveShadowPrefills(s.active, latestHandle, latest)
	if !ok || features.UncachedPrefillTokens < features.ExistingUncachedPrefill ||
		features.UncachedPrefillTokens-features.ExistingUncachedPrefill != latest.Tokens {
		result.AttributionState = predictiveShadowPrefillAttributionIncompatible
		return result
	}
	result.Features = features
	result.FeaturesValid = true
	result.DecisionManagerSequence = managerSequence
	// No individual shadow decision predicted the reconstructed aggregate.
	// Preserve that causal boundary in hard-origin telemetry and future learning.
	result.Exploratory = true
	result.AttributionState = predictiveShadowPrefillAttributionAggregate
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
	s.advanceEventSequenceLocked()
	return count
}

func (s *predictiveShadowPendingPrefillStore) advanceEventSequenceLocked() bool {
	if s.sequenceExhausted || s.eventSequence == ^uint64(0) {
		s.sequenceExhausted = true
		return false
	}
	s.eventSequence++
	return true
}

type predictiveShadowPrefillBase struct {
	decodeSequences         int
	pendingPrefillSequences int
	activeContextTokens     int64
	uncachedPrefillTokens   int64
	physicalKVUpper         int64
	activeKVUpper           int64
}

type predictiveShadowPrefillDelta struct {
	activeContextTokens int64
	uncachedPrefill     int64
	physicalKVUpper     int64
	activeKVUpper       int64
}

func validPredictiveShadowPrefillObservation(observation runtimepredictive.PendingPrefillObservation) (predictiveShadowPrefillBase, predictiveShadowPrefillDelta, bool) {
	features := observation.Features
	if observation.Tokens <= 0 ||
		features.ExistingDecodeSequences < 0 || features.ExistingDecodeSequences == math.MaxInt ||
		features.DecodeSequences != features.ExistingDecodeSequences+1 ||
		features.ExistingPendingPrefillSequences < 0 ||
		features.ExistingPendingPrefillSequences > features.ExistingDecodeSequences ||
		features.PendingPrefillSequences != features.ExistingPendingPrefillSequences+1 ||
		features.ExistingActiveContextTokens < 0 || features.ExistingUncachedPrefill < 0 ||
		features.ExistingPhysicalKVUpper < 0 || features.ExistingActiveKVUpper < 0 ||
		features.ActiveContextTokens < features.ExistingActiveContextTokens ||
		features.UncachedPrefillTokens < features.ExistingUncachedPrefill ||
		features.PhysicalKVUpper < features.ExistingPhysicalKVUpper ||
		features.ActiveKVUpper < features.ExistingActiveKVUpper ||
		features.UncachedPrefillTokens-features.ExistingUncachedPrefill != observation.Tokens ||
		features.RequestComplexityTokensUpper < observation.Tokens ||
		features.AccruedLocalAdmissionLatency < 0 || features.DecodeHorizonUpper < 0 {
		return predictiveShadowPrefillBase{}, predictiveShadowPrefillDelta{}, false
	}
	return predictiveShadowPrefillBase{
			decodeSequences:         features.ExistingDecodeSequences,
			pendingPrefillSequences: features.ExistingPendingPrefillSequences,
			activeContextTokens:     features.ExistingActiveContextTokens,
			uncachedPrefillTokens:   features.ExistingUncachedPrefill,
			physicalKVUpper:         features.ExistingPhysicalKVUpper,
			activeKVUpper:           features.ExistingActiveKVUpper,
		}, predictiveShadowPrefillDelta{
			activeContextTokens: features.ActiveContextTokens - features.ExistingActiveContextTokens,
			uncachedPrefill:     observation.Tokens,
			physicalKVUpper:     features.PhysicalKVUpper - features.ExistingPhysicalKVUpper,
			activeKVUpper:       features.ActiveKVUpper - features.ExistingActiveKVUpper,
		}, true
}

func aggregatePredictiveShadowPrefills(
	active map[*predictiveShadowPendingPrefillHandle]runtimepredictive.PendingPrefillObservation,
	latestHandle *predictiveShadowPendingPrefillHandle,
	latest runtimepredictive.PendingPrefillObservation,
) (runtimepredictive.SchedulerFeatures, uint64, bool) {
	if len(active) < 2 || latestHandle == nil || latestHandle.sequence == 0 {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	base, _, ok := validPredictiveShadowPrefillObservation(latest)
	if !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	managerSequence := latest.DecisionManagerSequence
	var prior, total predictiveShadowPrefillDelta
	for handle, observation := range active {
		entryBase, delta, valid := validPredictiveShadowPrefillObservation(observation)
		if !valid || observation.DecisionManagerSequence != managerSequence || entryBase != base ||
			!addPredictiveShadowPrefillDelta(&total, delta) {
			return runtimepredictive.SchedulerFeatures{}, 0, false
		}
		if handle != latestHandle && !addPredictiveShadowPrefillDelta(&prior, delta) {
			return runtimepredictive.SchedulerFeatures{}, 0, false
		}
	}

	priorCount := len(active) - 1
	totalCount := len(active)
	existingDecode, ok := checkedAddPredictiveShadowInt(base.decodeSequences, priorCount)
	if !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	decode, ok := checkedAddPredictiveShadowInt(base.decodeSequences, totalCount)
	if !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	existingPending, ok := checkedAddPredictiveShadowInt(base.pendingPrefillSequences, priorCount)
	if !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	pending, ok := checkedAddPredictiveShadowInt(base.pendingPrefillSequences, totalCount)
	if !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}

	features := latest.Features
	features.ExistingDecodeSequences = existingDecode
	features.DecodeSequences = decode
	features.ExistingPendingPrefillSequences = existingPending
	features.PendingPrefillSequences = pending
	if features.ExistingActiveContextTokens, ok = checkedAddPredictiveShadowInt64(base.activeContextTokens, prior.activeContextTokens); !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	if features.ActiveContextTokens, ok = checkedAddPredictiveShadowInt64(base.activeContextTokens, total.activeContextTokens); !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	if features.ExistingUncachedPrefill, ok = checkedAddPredictiveShadowInt64(base.uncachedPrefillTokens, prior.uncachedPrefill); !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	if features.UncachedPrefillTokens, ok = checkedAddPredictiveShadowInt64(base.uncachedPrefillTokens, total.uncachedPrefill); !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	if features.ExistingPhysicalKVUpper, ok = checkedAddPredictiveShadowInt64(base.physicalKVUpper, prior.physicalKVUpper); !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	if features.PhysicalKVUpper, ok = checkedAddPredictiveShadowInt64(base.physicalKVUpper, total.physicalKVUpper); !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	if features.ExistingActiveKVUpper, ok = checkedAddPredictiveShadowInt64(base.activeKVUpper, prior.activeKVUpper); !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	if features.ActiveKVUpper, ok = checkedAddPredictiveShadowInt64(base.activeKVUpper, total.activeKVUpper); !ok {
		return runtimepredictive.SchedulerFeatures{}, 0, false
	}
	return features, managerSequence, true
}

func addPredictiveShadowPrefillDelta(total *predictiveShadowPrefillDelta, delta predictiveShadowPrefillDelta) bool {
	var ok bool
	if total.activeContextTokens, ok = checkedAddPredictiveShadowInt64(total.activeContextTokens, delta.activeContextTokens); !ok {
		return false
	}
	if total.uncachedPrefill, ok = checkedAddPredictiveShadowInt64(total.uncachedPrefill, delta.uncachedPrefill); !ok {
		return false
	}
	if total.physicalKVUpper, ok = checkedAddPredictiveShadowInt64(total.physicalKVUpper, delta.physicalKVUpper); !ok {
		return false
	}
	if total.activeKVUpper, ok = checkedAddPredictiveShadowInt64(total.activeKVUpper, delta.activeKVUpper); !ok {
		return false
	}
	return true
}

func checkedAddPredictiveShadowInt(left, right int) (int, bool) {
	if left < 0 || right < 0 || left > math.MaxInt-right {
		return 0, false
	}
	return left + right, true
}

func checkedAddPredictiveShadowInt64(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
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
