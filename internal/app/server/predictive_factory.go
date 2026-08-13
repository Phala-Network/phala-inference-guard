package server

import (
	"fmt"
	"log"
	"strings"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func newDefaultAdmissionService(cfg config) (admissionService, error) {
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
		MaxModelLen:    cfg.PredictiveMaxModelLenTokens,
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
		"predictive_capability event=profile_initialized schema=%s source=%s reason=%s kv_capacity_tokens=%d kv_block_size=%d kv_hard_limit_tokens=%d max_model_len_tokens=%d maximum_admissible_input_tokens=%d prefill_regular_tokens=%d prefill_exclusive_tokens=%d prefill_quiescent_tokens=%d prefill_contended_budget_tokens=%d prefill_aggregate_budget_tokens=%d",
		profile.SchemaVersion,
		profile.Source,
		initialization.Reason,
		profile.KVCapacityTokens,
		profile.KVBlockSize,
		profile.KVHardLimitTokens,
		profile.MaxModelLenTokens,
		profile.MaximumAdmissibleInputTokens,
		profile.PrefillRegularTokens,
		profile.PrefillExclusiveTokens,
		profile.PrefillQuiescentTokens,
		profile.PrefillContendedBudgetTokens,
		profile.PrefillAggregateBudgetTokens,
	)
	controller, err := coreadmission.NewAdmissionController(coreadmission.Capability{
		Fingerprint:                  profile.ModelIdentitySHA256,
		MaxModelLenTokens:            profile.MaxModelLenTokens,
		KVCapacityTokens:             profile.KVCapacityTokens,
		KVBlockSize:                  profile.KVBlockSize,
		KVHardLimitTokens:            profile.KVHardLimitTokens,
		MaximumInputTokens:           profile.MaximumAdmissibleInputTokens,
		MinimumDecodeHorizonTokens:   runtimepredictive.DefaultCapabilityDecodeHorizonTokens,
		PrefillRegularTokens:         profile.PrefillRegularTokens,
		PrefillExclusiveTokens:       profile.PrefillExclusiveTokens,
		PrefillQuiescentTokens:       profile.PrefillQuiescentTokens,
		PrefillContendedBudgetTokens: profile.PrefillContendedBudgetTokens,
		PrefillAggregateBudgetTokens: profile.PrefillAggregateBudgetTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("construct admission Controller: %w", err)
	}
	window, ok := controller.StartSampleWindow()
	if !ok {
		controller.Close()
		return nil, fmt.Errorf("initialize admission observation window")
	}
	publication := controller.PublishObservation(window, coreadmission.BackendObservation{
		CapabilityFingerprint: profile.ModelIdentitySHA256,
		MaxModelLenTokens:     profile.MaxModelLenTokens,
		KVCapacityTokens:      profile.KVCapacityTokens,
		KVBlockSize:           profile.KVBlockSize,
		ObservedAt:            startup.ObservedAt,
		MaximumAge:            cfg.PredictiveMaximumMetricsAge,
		UsedKVTokens:          startup.UsedTokens,
		Running:               int64(startup.Running),
		Waiting:               int64(startup.Waiting),
		GenerationTokensTotal: startup.Generation,
		PreemptionsTotal:      startup.Preemptions,
		RuntimeStartTime:      startup.RuntimeStartTime,
	})
	if !publication.Accepted {
		controller.Close()
		return nil, fmt.Errorf("initialize admission observation: %s", publication.Reason)
	}
	runtime, err := newAdmissionRuntime(
		controller,
		newAdmissionReporter(defaultAdmissionDecisionLogInterval, logAdmissionDecision),
		profile,
		initialization.Reason,
		cfg.PredictiveAdmissionMode,
		nil,
	)
	if err != nil {
		controller.Close()
		return nil, err
	}
	observer, err := newAdmissionVLLMObserver(admissionVLLMObserverConfig{
		MetricsURL:            metricsURL,
		UpstreamURL:           cfg.Upstream,
		ModelName:             startup.modelName,
		RevalidateMetadata:    initialization.Reason == "metadata",
		CapabilityFingerprint: profile.ModelIdentitySHA256,
		MaxModelLenTokens:     profile.MaxModelLenTokens,
		KVCapacityTokens:      profile.KVCapacityTokens,
		KVBlockSize:           profile.KVBlockSize,
		PollInterval:          cfg.PredictiveObservationPollInterval,
		MaximumAge:            cfg.PredictiveMaximumMetricsAge,
		RequestTimeout:        cfg.PredictiveMetricsRequestTimeout,
		Controller:            controller,
	})
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("construct admission vLLM observer: %w", err)
	}
	runtime.observer = observer
	return runtime, nil
}

func predictiveVLLMMetricsURL(cfg config) (string, error) {
	metricsURL := strings.TrimSpace(cfg.PredictiveMetricsURL)
	if metricsURL == "" {
		return "", fmt.Errorf("predictive vLLM metrics URL is empty")
	}
	return metricsURL, nil
}
