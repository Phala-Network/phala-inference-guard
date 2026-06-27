package server

import (
	"github.com/Phala-Network/phala-inference-guard/internal/app/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/backend"
)

func backendProxyConfigs(configs []backendConfig) []backend.Config {
	backends := make([]backend.Config, 0, len(configs))
	for _, cfg := range configs {
		backends = append(backends, backend.Config{
			Name:       cfg.Name,
			Upstream:   cfg.Upstream,
			MetricsURL: cfg.MetricsURL,
		})
	}
	return backends
}

func requestClassifierConfig(cfg config) request.Config {
	return request.Config{
		QoSPaths:              cfg.QoSPaths,
		PathSuffixMatch:       cfg.PathSuffixMatch,
		ClassifyOutputTokens:  cfg.ClassifyOutputTokens,
		JSONClassifyBodyBytes: cfg.JSONClassifyBodyBytes,
		JSONClassifyLimit:     cfg.JSONClassifyLimit,
		OutputTokenFields:     cfg.OutputTokenFields,
		MediumBodyBytes:       cfg.MediumBodyBytes,
		LongBodyBytes:         cfg.LongBodyBytes,
		VeryLongBodyBytes:     cfg.VeryLongBodyBytes,
		MediumOutputTokens:    cfg.MediumOutputTokens,
		LongOutputTokens:      cfg.LongOutputTokens,
		VeryLongOutputTokens:  cfg.VeryLongOutputTokens,
		AdaptiveOutput:        cfg.AdaptiveOutput,
		AdaptiveOutputWindow:  cfg.AdaptiveOutputWindow,
		AdaptiveOutputMin:     cfg.AdaptiveOutputMin,
		AdaptiveOutputMediumQ: cfg.AdaptiveOutputMediumQ,
		AdaptiveOutputLongQ:   cfg.AdaptiveOutputLongQ,
		AdaptiveOutputVeryQ:   cfg.AdaptiveOutputVeryQ,
		AdaptiveOutputGreen:   cfg.AdaptiveOutputGreen,
		AdaptiveOutputYellow:  cfg.AdaptiveOutputYellow,
		AdaptiveOutputRed:     cfg.AdaptiveOutputRed,
		DynamicEnabled:        cfg.DynamicEnabled,
		DynamicFailsafeState:  cfg.DynamicFailsafeState,
	}
}

func priorityInjectorConfig(cfg config) request.PriorityConfig {
	return request.PriorityConfig{
		Enabled:             cfg.BackendPriorityInjectionEnabled,
		Mode:                cfg.BackendPriorityMode,
		Strategy:            cfg.BackendPriorityRewriteStrategy,
		Field:               cfg.BackendPriorityField,
		PremiumValue:        cfg.BackendPriorityPremiumValue,
		BasicValue:          cfg.BackendPriorityBasicValue,
		BodyBytes:           cfg.BackendPriorityBodyBytes,
		BufferBytes:         cfg.BackendPriorityBufferBytes,
		StreamBufferBytes:   cfg.BackendPriorityStreamBufferBytes,
		Limit:               cfg.BackendPriorityRewriteLimit,
		FailOpen:            cfg.BackendPriorityFailOpen,
		StripEmptyToolCalls: cfg.OpenAICompatStripEmptyToolCalls,
		CompatBodyBytes:     cfg.OpenAICompatBodyBytes,
		CompatFailOpen:      cfg.OpenAICompatFailOpen,
	}
}

func dynamicQoSConfig(cfg config) dynamic.Config {
	return dynamic.Config{
		Enabled:                   cfg.DynamicEnabled,
		Enforce:                   cfg.DynamicEnforce,
		MetricsURLs:               cfg.DynamicMetricsURLs,
		PollInterval:              cfg.DynamicPollInterval,
		FailsafeState:             cfg.DynamicFailsafeState,
		BackendRouting:            cfg.BackendRouting,
		KVYellow:                  cfg.DynamicKVYellow,
		KVRed:                     cfg.DynamicKVRed,
		RunningYellow:             cfg.DynamicRunningYellow,
		RunningRed:                cfg.DynamicRunningRed,
		WaitingYellow:             cfg.DynamicWaitingYellow,
		WaitingRed:                cfg.DynamicWaitingRed,
		PreemptRed:                cfg.DynamicPreemptRed,
		PressureEnabled:           cfg.DynamicPressureEnabled,
		PressureHeadroom:          cfg.DynamicPressureHeadroom,
		PressureMinLimit:          cfg.DynamicPressureMinLimit,
		PressureLearnRatio:        cfg.DynamicPressureLearnRatio,
		PressureLearnMinRunning:   cfg.DynamicPressureLearnMinRunning,
		UserTPSEnabled:            cfg.DynamicUserTPSEnabled,
		TTFTEnabled:               cfg.DynamicTTFTEnabled,
		UserTPSYellow:             cfg.DynamicUserTPSYellow,
		UserTPSRed:                cfg.DynamicUserTPSRed,
		UserTPSMinRun:             cfg.DynamicUserTPSMinRun,
		UserTPSYellowN:            cfg.DynamicUserTPSYellowN,
		UserTPSRedN:               cfg.DynamicUserTPSRedN,
		UserTPSGraceMin:           cfg.DynamicUserTPSGraceMin,
		UserTPSGraceMax:           cfg.DynamicUserTPSGraceMax,
		UserTPSGraceBps:           cfg.DynamicUserTPSGraceBps,
		UserTPSGraceMul:           cfg.DynamicUserTPSGraceMul,
		UserTPSCapacityRatio:      cfg.DynamicUserTPSCapacityRatio,
		UserTPSCapacityRatioMax:   cfg.DynamicUserTPSCapacityRatioMax,
		UserTPSCapacitySmoothing:  cfg.DynamicUserTPSCapacitySmoothing,
		UserTPSCapacityLearn:      cfg.DynamicUserTPSCapacityLearn,
		UserTPSCapacityStepUp:     cfg.DynamicUserTPSCapacityStepUp,
		UserTPSCapacityHealthyN:   cfg.DynamicUserTPSCapacityHealthyN,
		UserTPSCapacityHealthyMul: cfg.DynamicUserTPSCapacityHealthyMul,
		GlobalGreen:               cfg.DynamicGlobalGreen,
		GlobalYellow:              cfg.DynamicGlobalYellow,
		GlobalRed:                 cfg.DynamicGlobalRed,
	}
}

func dynamicQoSBackends(backends []*backendProxy) []dynamic.Backend {
	result := make([]dynamic.Backend, 0, len(backends))
	for _, backend := range backends {
		result = append(result, backend)
	}
	return result
}
