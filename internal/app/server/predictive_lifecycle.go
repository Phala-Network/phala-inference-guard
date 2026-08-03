package server

import (
	"math"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveUpstreamState interface {
	Healthy(time.Time) bool
	Close() error
}

type predictiveCoordinatorSnapshotter interface {
	Snapshot() runtimepredictive.CountCoordinatorSnapshot
}

type predictiveAvailabilityProvider interface {
	Available() bool
}

func predictiveUpstreamHealthy(upstream predictiveUpstreamState, now time.Time) (healthy bool) {
	if upstream == nil {
		return true
	}
	defer func() {
		if recover() != nil {
			healthy = false
		}
	}()
	return upstream.Healthy(now)
}

func predictiveCoordinatorAvailable(provider predictiveAvailabilityProvider) (available bool) {
	if provider == nil {
		return true
	}
	defer func() {
		if recover() != nil {
			available = false
		}
	}()
	return provider.Available()
}

type predictiveLearningSnapshotter interface {
	Snapshot() runtimepredictive.LearnedSchedulerSnapshot
}

type predictiveProtectionScope string

const (
	predictiveProtectionScopeRequest      predictiveProtectionScope = "request"
	predictiveProtectionScopeLoad         predictiveProtectionScope = "load"
	predictiveProtectionScopeAvailability predictiveProtectionScope = "availability"
)

type predictiveAttemptSnapshot struct {
	Attempts          uint64
	Fits              uint64
	Risks             uint64
	Unknown           uint64
	ExploratoryFits   uint64
	ExploratoryRisks  uint64
	LastReason        domainpredictive.Reason
	LastSource        runtimepredictive.PredictionSource
	LastSamples       int
	LastExploratory   bool
	LastRejectReason  domainpredictive.Reason
	LastRejectSource  runtimepredictive.PredictionSource
	LastRejectScope   predictiveProtectionScope
	LastRejectSamples int
	LastRejectAt      time.Time
}

type predictiveTPSTargetSource uint8

const (
	predictiveTPSTargetNone predictiveTPSTargetSource = iota
	predictiveTPSTargetBackend
	predictiveTPSTargetLocal
)

func validPredictiveCompletionObservation(observation predictiveCompletionObservation, decodeHorizonUpper int64) bool {
	if observation.CompletionTokens <= 1 || decodeHorizonUpper <= 0 || observation.CompletionTokens > decodeHorizonUpper || observation.ElapsedSinceRequest <= 0 || observation.BackendMeanITL < 0 || observation.BackendGenerationTime < 0 {
		return false
	}
	if observation.BackendMeanITL == 0 || observation.BackendGenerationTime == 0 {
		return true
	}
	expected := multiplyPositiveDuration(observation.BackendMeanITL, observation.CompletionTokens-1)
	if expected <= 0 {
		return false
	}
	difference := expected - observation.BackendGenerationTime
	if difference < 0 {
		difference = -difference
	}
	tolerance := expected / 10
	if tolerance < 2*time.Millisecond {
		tolerance = 2 * time.Millisecond
	}
	return difference <= tolerance
}

func multiplyPositiveDuration(value time.Duration, count int64) time.Duration {
	if value <= 0 || count <= 0 || count > math.MaxInt64/int64(value) {
		return 0
	}
	return value * time.Duration(count)
}

func dividePositiveDuration(value time.Duration, divisor int64) time.Duration {
	if value <= 0 || divisor <= 0 {
		return 0
	}
	result := value / time.Duration(divisor)
	if result <= 0 {
		return time.Nanosecond
	}
	return result
}
