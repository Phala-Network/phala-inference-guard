package server

import (
	"fmt"
	"math"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const (
	predictiveApproximateManifestID = "model-agnostic-json-v1"
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
		RetryInterval:  cfg.PredictiveObservationPollInterval,
	})
	if err != nil {
		return nil, err
	}
	hardKVTokens, err := predictiveProtectedTokens(
		startup.CapacityTokens,
		startup.BlockSize,
		cfg.KVAdmissionPolicy.VLLM.HardRatio,
	)
	if err != nil {
		return nil, err
	}
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		SoftKVRatio:                  cfg.KVAdmissionPolicy.VLLM.TargetRatio,
		HardKVRatio:                  cfg.KVAdmissionPolicy.VLLM.HardRatio,
		TPSTarget:                    cfg.PredictiveTPSTarget,
		TPSFloor:                     cfg.PredictiveTPSFloor,
		BlockSize:                    int64(startup.BlockSize),
		PrefillRegularTokens:         cfg.PredictivePrefillRegularTokens,
		PrefillExclusiveTokens:       cfg.PredictivePrefillExclusiveTokens,
		PrefillQuiescentTokens:       cfg.PredictivePrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: cfg.PredictivePrefillAggregateBudgetTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("construct deterministic request-aware policy: %w", err)
	}
	manager := runtimepredictive.NewManager(
		predictiveApproximateManifestID,
		domainpredictive.VirtualState{
			PhysicalKVUpper:     startup.UsedTokens,
			ActiveKVUpper:       startup.UsedTokens,
			DecodeSequences:     startup.Running + startup.Waiting,
			ActiveContextTokens: startup.UsedTokens,
		},
		domainpredictive.Constraints{
			PhysicalKVHard: hardKVTokens,
			ActiveKVHard:   hardKVTokens,
			UserTPSTarget:  cfg.PredictiveTPSFloor,
		},
		nil,
	)
	observer, err := newPredictiveVLLMObserver(predictiveVLLMObserverConfig{
		MetricsURL:          metricsURL,
		ModelIdentitySHA256: startup.ModelIdentitySHA256,
		MaximumKVTokens:     startup.CapacityTokens,
		BlockSize:           startup.BlockSize,
		PollInterval:        cfg.PredictiveObservationPollInterval,
		MaximumAge:          cfg.PredictiveMaximumMetricsAge,
		RequestTimeout:      cfg.PredictiveMetricsRequestTimeout,
		PreemptionCooldown:  cfg.KVAdmissionPolicy.PreemptionCooldown,
		Coordinator:         manager,
		Initial:             startup,
	})
	if err != nil {
		return nil, fmt.Errorf("construct deterministic vLLM observer: %w", err)
	}
	adapter, err := newRequestAwarePredictiveAdapter(requestAwarePredictiveAdapterConfig{
		Manager:    manager,
		Policy:     policy,
		Snapshot:   observer,
		ManifestID: predictiveApproximateManifestID,
		BlockSize:  int64(startup.BlockSize),
		Mode:       cfg.PredictiveAdmissionMode,
		OnDecision: logRequestAwareDecision,
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
