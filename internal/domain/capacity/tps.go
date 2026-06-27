package capacity

import (
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

func InitialLimit(baseLimit int) int {
	if baseLimit <= 0 {
		return baseLimit
	}
	return num.ClampInt(defaultInitialLimit, 1, baseLimit)
}

func KVPressureActive(cfg Config, kvValue float64) bool {
	return (cfg.KVYellow > 0 && kvValue >= cfg.KVYellow) || (cfg.KVRed > 0 && kvValue >= cfg.KVRed)
}

func QoSGlobalLimit(cfg Config, baseLimit, running int, generationTPS float64, generationTPSValid, enforce bool) int {
	if !enforce || !cfg.UserTPSEnabled || baseLimit <= 0 || running < cfg.UserTPSMinRun || !generationTPSValid {
		return baseLimit
	}
	targetTPS := cfg.UserTPSYellow
	if targetTPS <= 0 {
		return baseLimit
	}
	capacity := int(math.Floor(generationTPS / targetTPS))
	if capacity < 1 {
		capacity = 1
	}
	if capacity > baseLimit {
		return baseLimit
	}
	return capacity
}

func SmoothTPS(cfg Config, current float64, currentValid bool, previous Previous) float64 {
	if !currentValid || current < 0 {
		if previous.Source == "metrics" && previous.CapacityTPS > 0 {
			return previous.CapacityTPS
		}
		return 0
	}
	if previous.Source != "metrics" || previous.CapacityTPS <= 0 || current >= previous.CapacityTPS {
		return current
	}
	decay := cfg.CapacitySmoothing
	if decay < 0 {
		decay = 0
	}
	if decay >= 1 {
		decay = 0.85
	}
	return previous.CapacityTPS*decay + current*(1-decay)
}
