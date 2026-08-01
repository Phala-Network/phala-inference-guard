package server

import (
	"fmt"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive/nativeffi"
)

func newDefaultPredictiveShadow(cfg config) (predictiveAdmissionShadow, error) {
	profile, err := loadPredictiveProfile(cfg.PredictiveAdmissionProfilePath, cfg.PredictiveAdmissionProfileSHA256)
	if err != nil {
		return nil, err
	}
	renderer, err := newGemma4TextRenderer(gemma4TextRendererConfig{
		ServedModel:          profile.manifest.ServedModel,
		BOSToken:             profile.manifest.BOSToken,
		DefaultDecodeHorizon: profile.manifest.DefaultDecodeHorizon,
		MaximumDecodeHorizon: profile.manifest.MaximumDecodeHorizon,
	})
	if err != nil {
		return nil, fmt.Errorf("construct predictive renderer: %w", err)
	}
	identity := runtimepredictive.ModelIdentity{
		ProfileID:        profile.manifest.ProfileID,
		BackendEpoch:     profile.manifest.BackendEpoch,
		PredictorVersion: profile.manifest.PredictorVersion,
	}
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity:                      identity,
		BaseCompletionTPS:             profile.manifest.BaseCompletionTPS,
		PrefillTPSPenaltyPerKToken:    profile.manifest.PrefillTPSPenaltyPerKToken,
		BaseTTFT:                      time.Duration(profile.manifest.BaseTTFTMilliseconds) * time.Millisecond,
		TTFTPerUncachedPrefillToken:   time.Duration(profile.manifest.TTFTPerPrefillTokenMicroseconds) * time.Microsecond,
		BaseTPOT:                      time.Duration(profile.manifest.BaseTPOTMilliseconds) * time.Millisecond,
		TPOTPerExistingDecodeSequence: time.Duration(profile.manifest.TPOTPerExistingSequenceMilliseconds) * time.Millisecond,
		WorkspaceRiskUpper:            profile.manifest.WorkspaceRiskUpper,
		PreemptionRiskUpper:           profile.manifest.PreemptionRiskUpper,
		Confidence:                    profile.manifest.ProfileConfidence,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity:                 identity,
		MinimumSamples:           profile.manifest.CalibratorMinimumSamples,
		MaximumSamplesPerCell:    profile.manifest.CalibratorMaximumSamplesPerCell,
		MaximumCells:             profile.manifest.CalibratorMaximumCells,
		MaxAge:                   time.Duration(profile.manifest.CalibratorMaxAgeSeconds) * time.Second,
		LowerQuantile:            profile.manifest.CalibratorLowerQuantile,
		UpperQuantile:            profile.manifest.CalibratorUpperQuantile,
		MinimumTPSMultiplier:     profile.manifest.CalibratorMinimumTPSMultiplier,
		MaximumTPSMultiplier:     profile.manifest.CalibratorMaximumTPSMultiplier,
		MinimumLatencyMultiplier: profile.manifest.CalibratorMinimumLatencyMultiplier,
		MaximumLatencyMultiplier: profile.manifest.CalibratorMaximumLatencyMultiplier,
		CalibratedConfidence:     profile.manifest.CalibratorConfidence,
		DecodeSequenceBucket:     profile.manifest.CalibratorDecodeSequenceBucket,
		ContextTokenBucket:       profile.manifest.CalibratorContextTokenBucket,
		PrefillTokenBucket:       profile.manifest.CalibratorPrefillTokenBucket,
		KVTokenBucket:            profile.manifest.CalibratorKVTokenBucket,
	})
	if err != nil {
		return nil, fmt.Errorf("construct predictive scheduler: %w", err)
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID:   profile.manifest.ManifestID,
			BackendEpoch: profile.manifest.BackendEpoch,
			Scheduler:    identity,
			BlockSize:    profile.manifest.BlockSize,
		},
		ModelMaximumLength: profile.manifest.ModelMaximumLength,
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard:       profile.manifest.ProtectedKVTokens,
			ActiveKVHard:         profile.manifest.ProtectedKVTokens,
			UserTPSTarget:        profile.manifest.UserTPSTarget,
			TTFTSLO:              time.Duration(profile.manifest.TTFTSLOMilliseconds) * time.Millisecond,
			TPOTSLO:              time.Duration(profile.manifest.TPOTSLOMilliseconds) * time.Millisecond,
			WorkspaceRiskBudget:  profile.manifest.WorkspaceRiskBudget,
			PreemptionRiskBudget: profile.manifest.PreemptionRiskBudget,
			MinimumConfidence:    profile.manifest.MinimumConfidence,
		},
		Scheduler: scheduler,
	})
	if err != nil {
		return nil, fmt.Errorf("construct predictive coordinator: %w", err)
	}
	counter, err := nativeffi.OpenCounter(nativeffi.CounterConfig{
		TokenizerPath:              profile.tokenizerPath,
		ManifestID:                 profile.manifest.ManifestID,
		BackendEpoch:               profile.manifest.BackendEpoch,
		CompletionAddSpecialTokens: *profile.manifest.CompletionAddSpecialTokens,
		ChatAddSpecialTokens:       *profile.manifest.ChatAddSpecialTokens,
	})
	if err != nil {
		return nil, fmt.Errorf("open predictive count-only tokenizer: %w", err)
	}
	if err := verifyPredictiveAsset(profile.tokenizerPath, profile.manifest.Tokenizer.SHA256); err != nil {
		_ = counter.Close()
		return nil, fmt.Errorf("reverify predictive tokenizer after open: %w", err)
	}
	metricsURL, err := predictiveVLLMMetricsURL(cfg)
	if err != nil {
		_ = counter.Close()
		return nil, err
	}
	observer, err := newPredictiveVLLMObserver(predictiveVLLMObserverConfig{
		MetricsURL:         metricsURL,
		ServedModel:        profile.manifest.ServedModel,
		MaximumKVTokens:    profile.manifest.MaximumKVTokens,
		BlockSize:          profile.manifest.BlockSize,
		PollInterval:       time.Duration(profile.manifest.MetricsPollIntervalMilliseconds) * time.Millisecond,
		MaximumAge:         time.Duration(profile.manifest.MetricsMaximumAgeMilliseconds) * time.Millisecond,
		RequestTimeout:     time.Duration(profile.manifest.MetricsRequestTimeoutMilliseconds) * time.Millisecond,
		PreemptionCooldown: time.Duration(profile.manifest.PreemptionCooldownMilliseconds) * time.Millisecond,
		Coordinator:        coordinator,
	})
	if err != nil {
		_ = counter.Close()
		return nil, fmt.Errorf("construct predictive vLLM observer: %w", err)
	}
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Counter:     counter,
		Coordinator: coordinator,
		Learner:     scheduler,
		Upstream:    observer,
	})
	if err != nil {
		_ = observer.Close()
		_ = counter.Close()
		return nil, err
	}
	return adapter, nil
}
