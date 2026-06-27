package dynamic

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	runtimedynamic "github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type cleanSignals struct {
	Now                       time.Time
	BackendCount              int
	Aggregated                telemetry.Sample
	Running                   int
	Waiting                   int
	KVCacheUsage              float64
	Preemptions               uint64
	Generation                uint64
	PreemptionDelta           uint64
	GenerationTPS             float64
	GenerationTPSValid        bool
	CapacityTPS               float64
	CapacityEstimate          capacity.EstimateResult
	QOSTPS                    float64
	QOSTPSValid               bool
	UserTPS                   float64
	UserTPSYellowCount        int
	UserTPSRedCount           int
	UserTPSYellowReady        bool
	UserTPSRedReady           bool
	QOSHealthy                bool
	PrefillProtected          int
	DecodeRunning             int
	PrefillTransition         bool
	PrefillFreeze             bool
	PrefillSettling           bool
	DynamicRejected           uint64
	DynamicRejectedDelta      uint64
	TierBasicRejected         uint64
	TierPremiumRejected       uint64
	TierBasicRejectedDelta    uint64
	TierPremiumRejectedDelta  uint64
	TierBasicWaiting          int64
	TierPremiumWaiting        int64
	TierDemandPressure        bool
	CapacityDemandPressure    bool
	RepresentativeUserTPSLoad bool
	PreviousMetrics           PreviousMetrics
	PreviousSnapshot          runtimedynamic.Snapshot
	CapacityPrevious          capacity.Previous
}

type cleanPrefillState struct {
	Protected     int
	DecodeRunning int
	Transition    bool
	Freeze        bool
	Settling      bool
}

func deriveCleanSignals(cfg Config, input Input, now time.Time) cleanSignals {
	previousMetrics := input.Previous
	previousSnapshot := previousMetrics.Snapshot
	aggregated := telemetry.AggregateSamples(input.Samples)
	generationWindow := observeGenerationWindow(now, aggregated, previousSnapshot)
	capacityPrevious := capacityPrevious(previousSnapshot)

	prefill := deriveCleanPrefillState(cfg, aggregated.Running, aggregated.Waiting, input.PrefillProtected, previousSnapshot, generationWindow)
	dynamicRejectedDelta := metricsCounterDelta(input.DynamicRejected, previousSnapshot.DynamicRejected, previousSnapshot.Source)
	basicRejectedDelta := metricsCounterDelta(input.Tier.BasicRejected, previousSnapshot.TierBasicRejected, previousSnapshot.Source)
	premiumRejectedDelta := metricsCounterDelta(input.Tier.PremiumRejected, previousSnapshot.TierPremiumRejected, previousSnapshot.Source)
	tierPressure := tierDemandPressure(input, aggregated.Running, basicRejectedDelta)
	capacityDemandPressure := input.QueueCurrent > 0 || dynamicRejectedDelta > 0 || tierPressure
	capacityEstimate := capacity.EstimateCleanCapacity(capacity.EstimateInput{
		Config:             cfg.Capacity,
		Previous:           capacityPrevious,
		BaseLimit:          input.GlobalLimit,
		GenerationTPS:      generationWindow.GenerationTPS,
		GenerationTPSValid: generationWindow.GenerationTPSValid,
		Running:            aggregated.Running,
		DecodeRunning:      prefill.DecodeRunning,
		Waiting:            aggregated.Waiting,
		QueueCurrent:       input.QueueCurrent,
		DynamicRejected:    dynamicRejectedDelta,
		DemandPressure:     capacityDemandPressure,
		PrefillTransition:  prefill.Freeze,
	})
	capacityTPS := capacityEstimate.SmoothedTPS

	qosTPS := generationWindow.GenerationTPS
	qosTPSValid := generationWindow.GenerationTPSValid
	if capacityTPS > qosTPS {
		qosTPS = capacityTPS
		qosTPSValid = true
	}

	userTPS := 0.0
	userTPSYellowCount := 0
	userTPSRedCount := 0
	representativeUserTPSLoad := aggregated.Waiting > 0 || prefill.DecodeRunning >= capacity.MinUserTPSEnforceRunning || aggregated.Running >= capacity.MinUserTPSEnforceRunning
	if cfg.UserTPSEnabled && !prefill.Freeze && prefill.DecodeRunning >= cfg.UserTPSMinRun && prefill.DecodeRunning > 0 && qosTPSValid {
		userTPS = qosTPS / float64(prefill.DecodeRunning)
		if representativeUserTPSLoad && cfg.UserTPSYellow > 0 && userTPS < cfg.UserTPSYellow {
			userTPSYellowCount = previousMetrics.UserTPSYellowCount + 1
		}
		if representativeUserTPSLoad && cfg.UserTPSRed > 0 && userTPS < cfg.UserTPSRed {
			userTPSRedCount = previousMetrics.UserTPSRedCount + 1
		}
	}

	return cleanSignals{
		Now:                       now,
		BackendCount:              len(input.Samples) + input.BackendFailed,
		Aggregated:                aggregated,
		Running:                   aggregated.Running,
		Waiting:                   aggregated.Waiting,
		KVCacheUsage:              aggregated.KVCacheUsage,
		Preemptions:               aggregated.Preemptions,
		Generation:                aggregated.Generation,
		PreemptionDelta:           generationWindow.PreemptionDelta,
		GenerationTPS:             generationWindow.GenerationTPS,
		GenerationTPSValid:        generationWindow.GenerationTPSValid,
		CapacityTPS:               capacityTPS,
		CapacityEstimate:          capacityEstimate,
		QOSTPS:                    qosTPS,
		QOSTPSValid:               qosTPSValid,
		UserTPS:                   userTPS,
		UserTPSYellowCount:        userTPSYellowCount,
		UserTPSRedCount:           userTPSRedCount,
		UserTPSYellowReady:        userTPSYellowCount >= cfg.UserTPSYellowN,
		UserTPSRedReady:           userTPSRedCount >= cfg.UserTPSRedN,
		QOSHealthy:                cfg.UserTPSEnabled && qosTPSValid && prefill.DecodeRunning >= cfg.UserTPSMinRun && prefill.DecodeRunning > 0 && cfg.UserTPSYellow > 0 && userTPS >= cfg.UserTPSYellow,
		PrefillProtected:          prefill.Protected,
		DecodeRunning:             prefill.DecodeRunning,
		PrefillTransition:         prefill.Transition,
		PrefillFreeze:             prefill.Freeze,
		PrefillSettling:           prefill.Settling,
		DynamicRejected:           input.DynamicRejected,
		DynamicRejectedDelta:      dynamicRejectedDelta,
		TierBasicRejected:         input.Tier.BasicRejected,
		TierPremiumRejected:       input.Tier.PremiumRejected,
		TierBasicRejectedDelta:    basicRejectedDelta,
		TierPremiumRejectedDelta:  premiumRejectedDelta,
		TierBasicWaiting:          input.Tier.BasicWaiting,
		TierPremiumWaiting:        input.Tier.PremiumWaiting,
		TierDemandPressure:        tierPressure,
		CapacityDemandPressure:    capacityDemandPressure,
		RepresentativeUserTPSLoad: representativeUserTPSLoad,
		PreviousMetrics:           previousMetrics,
		PreviousSnapshot:          previousSnapshot,
		CapacityPrevious:          capacityPrevious,
	}
}

func deriveCleanPrefillState(cfg Config, running, waiting, protected int, previous runtimedynamic.Snapshot, generation generationObservation) cleanPrefillState {
	state := cleanPrefillState{DecodeRunning: running}
	if !cfg.UserTPSEnabled {
		return state
	}
	if protected < 0 {
		protected = 0
	}
	if protected > running {
		protected = running
	}
	state.Protected = protected
	state.DecodeRunning = running - protected
	if state.DecodeRunning < 0 {
		state.DecodeRunning = 0
	}
	state.Freeze = capacity.MeaningfulPrefillProtected(running, protected)
	state.Transition = state.Freeze
	if state.Transition || !previous.PrefillTransition || running <= 0 || !generation.GenerationTPSValid || cfg.UserTPSYellow <= 0 {
		return state
	}
	perRunningTPS := generation.GenerationTPS / float64(num.MaxInt(running, 1))
	if waiting > 0 || perRunningTPS < cfg.UserTPSYellow {
		state.Settling = true
		state.Transition = true
	}
	return state
}

func metricsCounterDelta(current, previous uint64, previousSource string) uint64 {
	if previousSource == "metrics" && current >= previous {
		return current - previous
	}
	return current
}

func tierDemandPressure(input Input, running int, basicRejectedDelta uint64) bool {
	if input.Tier.BasicLimit <= 0 {
		return false
	}
	if input.Tier.BasicInflight >= int64(input.Tier.BasicLimit) {
		return true
	}
	premiumReservedIdle := input.Tier.PremiumReserved > 0 &&
		input.Tier.PremiumInflight == 0 &&
		input.Tier.PremiumWaiting == 0
	if !premiumReservedIdle {
		return false
	}
	if input.Tier.BasicWaiting > 0 && running >= input.Tier.BasicLimit {
		return true
	}
	if basicRejectedDelta > 0 && running >= input.Tier.BasicLimit {
		return true
	}
	return running >= input.Tier.BasicLimit
}
