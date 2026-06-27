package capacity

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type EstimateInput struct {
	Config             Config
	Previous           Previous
	BaseLimit          int
	GenerationTPS      float64
	GenerationTPSValid bool
	Running            int
	DecodeRunning      int
	Waiting            int
	QueueCurrent       int64
	DynamicRejected    uint64
	DemandPressure     bool
	PrefillTransition  bool
}

type EstimateResult struct {
	ObservedTPS        float64
	ObservedTPSValid   bool
	SmoothedTPS        float64
	RawLimit           int
	SafeLimit          int
	LowConfidenceLimit int
	Confidence         string
	RepresentativeLoad bool
}

func EstimateCleanCapacity(input EstimateInput) EstimateResult {
	cfg := input.Config
	result := EstimateResult{
		ObservedTPS:      input.GenerationTPS,
		ObservedTPSValid: input.GenerationTPSValid,
		Confidence:       "none",
	}
	if !cfg.UserTPSEnabled || input.BaseLimit <= 0 || cfg.UserTPSYellow <= 0 {
		return result
	}
	result.SmoothedTPS = SmoothTPS(cfg, input.GenerationTPS, input.GenerationTPSValid, input.Previous)
	if (input.Running == 0 || input.DecodeRunning == 0 || input.PrefillTransition) && input.Previous.CapacityTPS > result.SmoothedTPS {
		result.SmoothedTPS = input.Previous.CapacityTPS
	}
	if result.SmoothedTPS <= 0 {
		return result
	}
	result.RawLimit = num.ClampInt(int(math.Floor(result.SmoothedTPS/cfg.UserTPSYellow)), 1, input.BaseLimit)
	safetyRatio := cfg.CapacitySafetyRatio
	if safetyRatio <= 0 {
		safetyRatio = 1
	}
	if safetyRatio > 1 {
		safetyRatio = 1
	}
	result.SafeLimit = num.ClampInt(int(math.Floor(float64(result.RawLimit)*safetyRatio)), 1, input.BaseLimit)
	if input.Previous.CapacityLearnedLimit > 0 {
		result.LowConfidenceLimit = num.ClampInt(num.MinInt(input.Previous.CapacityLearnedLimit, result.SafeLimit), 1, input.BaseLimit)
	} else {
		result.LowConfidenceLimit = num.ClampInt(num.MinInt(InitialLimit(input.BaseLimit), result.SafeLimit), 1, input.BaseLimit)
	}

	representative := estimateRepresentativeLoad(input, input.Previous.CapacityLearnedLimit)
	result.RepresentativeLoad = representative
	switch {
	case input.Waiting > 0:
		result.Confidence = "pressure"
	case input.PrefillTransition:
		result.Confidence = "prefill"
	case !input.GenerationTPSValid:
		result.Confidence = "stale"
	case input.DecodeRunning < cfg.UserTPSMinRun || input.DecodeRunning <= 0:
		result.Confidence = "low"
	case representative:
		result.Confidence = "representative"
	default:
		result.Confidence = "sparse"
	}
	return result
}

func estimateRepresentativeLoad(input EstimateInput, learned int) bool {
	if input.Waiting > 0 || input.QueueCurrent > 0 || input.DynamicRejected > 0 || input.DemandPressure {
		return true
	}
	threshold := 8
	if learned > 0 {
		threshold = int(math.Ceil(float64(learned) * representativeCapacityLoadRatio))
		threshold = num.ClampInt(threshold, 8, learned)
	}
	return input.Running >= threshold || input.DecodeRunning >= threshold || input.Running >= learned || input.DecodeRunning >= learned
}
