package server

import (
	"fmt"
	"math"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	predictiveApproximateManifestID       = "model-agnostic-json-v1"
	predictiveApproximateProfileID        = "model-agnostic-qos-v2"
	predictiveApproximatePredictorVersion = "adaptive-tps-kv-v6"
	predictiveApproximateEstimatorVersion = "json-cost-lexical-hint-v2"
	predictiveColdParallelUsers           = 2
	predictivePrefillHeadroomSafetyShares = 4
)

func newDefaultPredictiveShadow(cfg config) (predictiveAdmissionShadow, error) {
	metricsURL, err := predictiveVLLMMetricsURL(cfg)
	if err != nil {
		return nil, err
	}
	startup, err := probePredictiveVLLMStartup(predictiveVLLMStartupProbeConfig{
		MetricsURL:     metricsURL,
		StartupTimeout: cfg.PredictiveStartupProbeTimeout,
		RequestTimeout: cfg.PredictiveMetricsRequestTimeout,
		RetryInterval:  cfg.DynamicPollInterval,
	})
	if err != nil {
		return nil, err
	}
	protectedTokens, err := predictiveProtectedTokens(startup.CapacityTokens, startup.BlockSize, cfg.KVAdmissionPolicy.VLLM.TargetRatio)
	if err != nil {
		return nil, err
	}
	targetTPS := cfg.DynamicUserTPSRed
	baseCompletionTPS := predictiveColdBaseCompletionTPS(targetTPS, cfg.DynamicUserTPSYellow)
	featureTokenBucket := predictiveFeatureTokenBucket(protectedTokens, startup.BlockSize)
	prefillTPSPenaltyPerKToken := predictivePrefillTPSPenaltyPerKToken(baseCompletionTPS, targetTPS, protectedTokens)
	ttftReference, err := predictiveSecondsDuration(cfg.DynamicTTFTPolicy.TargetSeconds)
	if err != nil {
		return nil, fmt.Errorf("construct predictive TTFT observation reference: %w", err)
	}
	tpotSLO, err := predictiveTPOTTarget(targetTPS)
	if err != nil {
		return nil, err
	}
	explorationTPOTSLO, err := predictiveTPOTTarget(cfg.DynamicUserTPSYellow)
	if err != nil {
		return nil, fmt.Errorf("construct predictive exploration TPOT target: %w", err)
	}
	backendEpoch := fmt.Sprintf("vllm-%s-%d-%d", startup.ModelIdentitySHA256[:16], startup.CapacityTokens, startup.BlockSize)
	identity := runtimepredictive.ModelIdentity{
		ProfileID:        predictiveApproximateProfileID,
		BackendEpoch:     backendEpoch,
		PredictorVersion: predictiveApproximatePredictorVersion,
	}
	baseTTFT := ttftReference / 4
	if baseTTFT <= 0 {
		baseTTFT = time.Nanosecond
	}
	baseTPOT := tpotSLO / 2
	if baseTPOT <= 0 {
		baseTPOT = time.Nanosecond
	}
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity:                      identity,
		BaseCompletionTPS:             baseCompletionTPS,
		PrefillTPSPenaltyPerKToken:    prefillTPSPenaltyPerKToken,
		BaseTTFT:                      baseTTFT,
		TTFTPerUncachedPrefillToken:   predictiveTTFTPerToken(ttftReference-baseTTFT, protectedTokens),
		BaseTPOT:                      baseTPOT,
		TPOTPerExistingDecodeSequence: baseTPOT,
		WorkspaceRiskUpper:            0,
		PreemptionRiskUpper:           0,
		Confidence:                    cfg.PredictiveColdConfidence,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity:                 identity,
		MinimumSamples:           cfg.PredictiveLearningMinimumSamples,
		MaximumSamplesPerCell:    cfg.PredictiveLearningMaximumSamples,
		MaximumCells:             cfg.PredictiveLearningMaximumCells,
		MaxAge:                   cfg.PredictiveLearningMaxAge,
		LowerQuantile:            0.10,
		UpperQuantile:            0.90,
		MinimumTPSMultiplier:     cfg.PredictiveTPSMinimumMultiplier,
		MaximumTPSMultiplier:     cfg.PredictiveTPSMaximumMultiplier,
		MinimumLatencyMultiplier: cfg.PredictiveLatencyMinimumMultiplier,
		MaximumLatencyMultiplier: cfg.PredictiveLatencyMaximumMultiplier,
		CalibratedConfidence:     cfg.PredictiveLearnedConfidence,
		DecodeSequenceBucket:     1,
		ContextTokenBucket:       featureTokenBucket,
		PrefillTokenBucket:       featureTokenBucket,
		KVTokenBucket:            featureTokenBucket,
		AdverseEvidenceMaxAge:    cfg.KVAdmissionPolicy.MaxMetricsAge,
		HardUserTPSTarget:        targetTPS,
		HardTPOTSLO:              tpotSLO,
		ExplorationUserTPSTarget: cfg.DynamicUserTPSYellow,
		ExplorationTPOTSLO:       explorationTPOTSLO,
	})
	if err != nil {
		return nil, fmt.Errorf("construct model-neutral predictive scheduler: %w", err)
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID:   predictiveApproximateManifestID,
			BackendEpoch: backendEpoch,
			Scheduler:    identity,
			BlockSize:    startup.BlockSize,
		},
		ModelMaximumLength: startup.CapacityTokens,
		Initial: domainpredictive.VirtualState{
			PhysicalKVUpper:     startup.UsedTokens,
			ActiveKVUpper:       startup.UsedTokens,
			DecodeSequences:     startup.Running + startup.Waiting,
			ActiveContextTokens: startup.UsedTokens,
		},
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard:       protectedTokens,
			ActiveKVHard:         protectedTokens,
			UserTPSTarget:        targetTPS,
			TPOTSLO:              tpotSLO,
			WorkspaceRiskBudget:  0,
			PreemptionRiskBudget: 0,
			MinimumConfidence:    cfg.PredictiveMinimumConfidence,
		},
		Scheduler: scheduler,
	})
	if err != nil {
		return nil, fmt.Errorf("construct model-neutral predictive coordinator: %w", err)
	}
	calibrator, err := runtimepredictive.NewInputSizeCalibrator(runtimepredictive.InputSizeCalibratorConfig{
		EstimatorVersion:       predictiveApproximateEstimatorVersion,
		MinimumSamples:         cfg.PredictiveLearningMinimumSamples,
		MaximumSamplesPerClass: cfg.PredictiveLearningMaximumSamples,
		MaxAge:                 cfg.PredictiveLearningMaxAge,
		UpperQuantile:          cfg.PredictiveInputUpperQuantile,
		SafetyMargin:           cfg.PredictiveInputSafetyMargin,
		MinimumMultiplier:      cfg.PredictiveInputMinimumMultiplier,
		MaximumMultiplier:      cfg.PredictiveInputMaximumMultiplier,
		ColdConfidence:         cfg.PredictiveColdConfidence,
		LearnedConfidence:      cfg.PredictiveLearnedConfidence,
	})
	if err != nil {
		return nil, fmt.Errorf("construct predictive input-size calibrator: %w", err)
	}
	shadowPendingPrefills := newPredictiveShadowPendingPrefillStore(cfg.PredictiveShadowObservationLimit)
	observer, err := newPredictiveVLLMObserver(predictiveVLLMObserverConfig{
		MetricsURL:                 metricsURL,
		ModelIdentitySHA256:        startup.ModelIdentitySHA256,
		MaximumKVTokens:            startup.CapacityTokens,
		BlockSize:                  startup.BlockSize,
		PollInterval:               cfg.DynamicPollInterval,
		MaximumAge:                 cfg.KVAdmissionPolicy.MaxMetricsAge,
		RequestTimeout:             cfg.PredictiveMetricsRequestTimeout,
		PreemptionCooldown:         cfg.KVAdmissionPolicy.PreemptionCooldown,
		Coordinator:                coordinator,
		ExistingPrefillLearner:     scheduler,
		AggregateThroughputLearner: scheduler,
		LoadPressureObserver:       scheduler,
		ShadowPendingPrefills:      shadowPendingPrefills,
		LearningInvalidators:       []predictiveLearningInvalidator{calibrator},
		Initial:                    startup,
	})
	if err != nil {
		return nil, fmt.Errorf("construct predictive vLLM observer: %w", err)
	}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             calibrator,
		Coordinator:            coordinator,
		Learner:                scheduler,
		Upstream:               observer,
		Mode:                   cfg.PredictiveAdmissionMode,
		ShadowObservationLimit: cfg.PredictiveShadowObservationLimit,
		ShadowPendingPrefills:  shadowPendingPrefills,
		RouterBackpressureHold: cfg.PredictiveRouterBackpressureHold,
		RouterBackpressure: predictiveRouterBackpressurePolicy{
			PhysicalKVHard: protectedTokens,
			ActiveKVHard:   protectedTokens,
		},
		OnRouterBackpressure: logPredictiveRouterBackpressure,
		OnRequestReject:      logPredictiveRequestReject,
	})
	if err != nil {
		_ = observer.Close()
		return nil, err
	}
	return adapter, nil
}

func predictiveProtectedTokens(capacity int64, blockSize int, ratio float64) (int64, error) {
	if capacity <= 0 || blockSize <= 0 || ratio <= 0 || ratio > 1 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
		return 0, fmt.Errorf("predictive protected KV budget inputs are invalid")
	}
	tokens := int64(math.Floor(float64(capacity) * ratio))
	tokens -= tokens % int64(blockSize)
	if tokens <= 0 || tokens > capacity {
		return 0, fmt.Errorf("predictive protected KV budget is empty or exceeds capacity")
	}
	return tokens, nil
}

func predictiveFeatureTokenBucket(protectedTokens int64, blockSize int) int64 {
	bucket := protectedTokens / 64
	if bucket < int64(blockSize) {
		bucket = int64(blockSize)
	}
	remainder := bucket % int64(blockSize)
	if remainder != 0 {
		bucket += int64(blockSize) - remainder
	}
	return bucket
}

func predictiveColdBaseCompletionTPS(targetTPS, yellowTPS float64) float64 {
	perUser := math.Max(targetTPS, yellowTPS)
	return perUser * predictiveColdParallelUsers
}

func predictivePrefillTPSPenaltyPerKToken(baseCompletionTPS, targetTPS float64, protectedTokens int64) float64 {
	if baseCompletionTPS <= targetTPS || targetTPS <= 0 || protectedTokens <= 0 {
		return 0
	}
	// Charge prompt pressure against the complete protected KV budget. The
	// safety-share multiplier deliberately consumes the cold TPS headroom at
	// one safety share of that budget; using the feature-cell bucket here made
	// the penalty 64x steeper a second time and collapsed ordinary long prompts
	// to a zero existing-user prior before learning could make progress.
	return float64(predictivePrefillHeadroomSafetyShares) * (baseCompletionTPS - targetTPS) * 1_000 / float64(protectedTokens)
}

func predictiveSecondsDuration(seconds float64) (time.Duration, error) {
	if seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds > float64(math.MaxInt64)/float64(time.Second) {
		return 0, fmt.Errorf("seconds value is invalid")
	}
	duration := time.Duration(seconds * float64(time.Second))
	if duration <= 0 {
		return 0, fmt.Errorf("seconds value rounds to zero")
	}
	return duration, nil
}

func predictiveTPOTTarget(targetTPS float64) (time.Duration, error) {
	if targetTPS <= 0 || math.IsNaN(targetTPS) || math.IsInf(targetTPS, 0) {
		return 0, fmt.Errorf("predictive TPS target is invalid")
	}
	duration := time.Duration(float64(time.Second) / targetTPS)
	if duration <= 0 {
		return 0, fmt.Errorf("predictive TPOT target rounds to zero")
	}
	return duration, nil
}

func predictiveTTFTPerToken(budget time.Duration, protectedTokens int64) time.Duration {
	if budget <= 0 || protectedTokens <= 0 {
		return 0
	}
	return time.Duration(int64(budget) / protectedTokens)
}
