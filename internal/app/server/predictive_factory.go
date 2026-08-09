package server

import (
	"fmt"
	"log"
	"math"
	"strings"

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
	initialization, err := initializePredictiveCapability(predictiveCapabilityInitializationConfig{
		UpstreamURL:    cfg.Upstream,
		RequestTimeout: cfg.PredictiveMetricsRequestTimeout,
		KVHardRatio:    cfg.PredictiveKVHardRatio,
		Prefill: runtimepredictive.PrefillTokenBounds{
			Regular:   cfg.PredictivePrefillRegularTokens,
			Exclusive: cfg.PredictivePrefillExclusiveTokens,
			Quiescent: cfg.PredictivePrefillQuiescentTokens,
			Aggregate: cfg.PredictivePrefillAggregateBudgetTokens,
		},
	}, startup)
	if err != nil {
		return nil, err
	}
	profile := initialization.Profile
	log.Printf(
		"predictive_capability event=profile_initialized schema=%s source=%s reason=%s kv_capacity_tokens=%d kv_block_size=%d kv_hard_limit_tokens=%d prefill_regular_tokens=%d prefill_exclusive_tokens=%d prefill_quiescent_tokens=%d prefill_aggregate_budget_tokens=%d",
		profile.SchemaVersion,
		profile.Source,
		initialization.Reason,
		profile.KVCapacityTokens,
		profile.KVBlockSize,
		profile.KVHardLimitTokens,
		profile.PrefillRegularTokens,
		profile.PrefillExclusiveTokens,
		profile.PrefillQuiescentTokens,
		profile.PrefillAggregateBudgetTokens,
	)
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		HardKVLimitTokens:            profile.KVHardLimitTokens,
		BlockSize:                    int64(startup.BlockSize),
		PrefillRegularTokens:         profile.PrefillRegularTokens,
		PrefillExclusiveTokens:       profile.PrefillExclusiveTokens,
		PrefillQuiescentTokens:       profile.PrefillQuiescentTokens,
		PrefillAggregateBudgetTokens: profile.PrefillAggregateBudgetTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("construct deterministic request-aware policy: %w", err)
	}
	manager := runtimepredictive.NewManager(
		predictiveApproximateManifestID,
		domainpredictive.VirtualState{
			PhysicalKVUpper:         startup.UsedTokens,
			ActiveKVUpper:           startup.UsedTokens,
			DecodeSequences:         startup.Running + startup.Waiting,
			PendingPrefillSequences: startup.Waiting,
			ActiveContextTokens:     startup.UsedTokens,
		},
	)
	observer, err := newPredictiveVLLMObserver(predictiveVLLMObserverConfig{
		MetricsURL:          metricsURL,
		ModelIdentitySHA256: startup.ModelIdentitySHA256,
		MaximumKVTokens:     startup.CapacityTokens,
		BlockSize:           startup.BlockSize,
		PollInterval:        cfg.PredictiveObservationPollInterval,
		MaximumAge:          cfg.PredictiveMaximumMetricsAge,
		RequestTimeout:      cfg.PredictiveMetricsRequestTimeout,
		Coordinator:         manager,
		Initial:             startup,
	})
	if err != nil {
		return nil, fmt.Errorf("construct deterministic vLLM observer: %w", err)
	}
	adapter, err := newRequestAwarePredictiveAdapter(requestAwarePredictiveAdapterConfig{
		Manager:           manager,
		Policy:            policy,
		CapabilityProfile: profile,
		CapabilityReason:  initialization.Reason,
		Snapshot:          observer,
		ManifestID:        predictiveApproximateManifestID,
		BlockSize:         int64(startup.BlockSize),
		Mode:              cfg.PredictiveAdmissionMode,
		OnDecision:        logRequestAwareDecision,
	})
	if err != nil {
		_ = observer.Close()
		return nil, err
	}
	return adapter, nil
}

func predictiveVLLMMetricsURL(cfg config) (string, error) {
	metricsURL := strings.TrimSpace(cfg.PredictiveMetricsURL)
	if metricsURL == "" {
		return "", fmt.Errorf("predictive vLLM metrics URL is empty")
	}
	return metricsURL, nil
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
