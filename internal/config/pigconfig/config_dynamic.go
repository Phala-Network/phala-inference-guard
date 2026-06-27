package pigconfig

import (
	"fmt"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/infra/env"
)

func loadDynamicConfig(cfg *Config) error {
	hasDynamicMetrics := len(cfg.DynamicMetricsURLs) > 0
	dynamicEnabled, err := env.Bool("DYNAMIC_PIG_ENABLED", hasDynamicMetrics)
	if err != nil {
		return err
	}
	dynamicEnforce, err := env.Bool("DYNAMIC_PIG_ENFORCE", dynamicEnabled)
	if err != nil {
		return err
	}
	dynamicPollIntervalMs, err := env.Int("DYNAMIC_POLL_INTERVAL_MS", 1000)
	if err != nil {
		return err
	}
	dynamicKVYellow, err := env.Float("DYNAMIC_KV_YELLOW", 0.70)
	if err != nil {
		return err
	}
	dynamicKVRed, err := env.Float("DYNAMIC_KV_RED", 0.80)
	if err != nil {
		return err
	}
	dynamicRunningYellow, err := env.Int("DYNAMIC_RUNNING_YELLOW", 0)
	if err != nil {
		return err
	}
	dynamicRunningRed, err := env.Int("DYNAMIC_RUNNING_RED", dynamicRunningYellow)
	if err != nil {
		return err
	}
	dynamicWaitingYellow, err := env.Int("DYNAMIC_WAITING_YELLOW", 1)
	if err != nil {
		return err
	}
	dynamicWaitingRed, err := env.Int("DYNAMIC_WAITING_RED", 2)
	if err != nil {
		return err
	}
	dynamicPreemptRed, err := env.Int("DYNAMIC_PREEMPTION_DELTA_RED", 1)
	if err != nil {
		return err
	}
	if dynamicPreemptRed < 0 {
		return fmt.Errorf("DYNAMIC_PREEMPTION_DELTA_RED must be >= 0")
	}
	dynamicPressureEnabled, err := env.Bool("DYNAMIC_PRESSURE_LIMIT_ENABLED", dynamicEnabled)
	if err != nil {
		return err
	}
	dynamicPressureHeadroom, err := env.Int("DYNAMIC_PRESSURE_HEADROOM", 1)
	if err != nil {
		return err
	}
	dynamicPressureMinLimit, err := env.Int("DYNAMIC_PRESSURE_MIN_LIMIT", 1)
	if err != nil {
		return err
	}
	dynamicPressureLearnRatio, err := env.Float("DYNAMIC_PRESSURE_LEARN_RATIO", 0.75)
	if err != nil {
		return err
	}
	dynamicPressureLearnMinRunning, err := env.Int("DYNAMIC_PRESSURE_LEARN_MIN_RUNNING", 16)
	if err != nil {
		return err
	}
	dynamicUserTPSEnabled, err := env.Bool("DYNAMIC_SINGLE_USER_TPS_ENABLED", dynamicEnabled)
	if err != nil {
		return err
	}
	dynamicTTFTEnabled, err := env.Bool("DYNAMIC_TTFT_ENABLED", dynamicUserTPSEnabled)
	if err != nil {
		return err
	}
	dynamicUserTPSYellow, err := env.Float("DYNAMIC_SINGLE_USER_TPS_YELLOW", 25)
	if err != nil {
		return err
	}
	dynamicUserTPSRed, err := env.Float("DYNAMIC_SINGLE_USER_TPS_RED", 20)
	if err != nil {
		return err
	}
	dynamicUserTPSMinRun, err := env.Int("DYNAMIC_SINGLE_USER_TPS_MIN_RUNNING", 1)
	if err != nil {
		return err
	}
	dynamicUserTPSYellowN, err := env.Int("DYNAMIC_SINGLE_USER_TPS_YELLOW_CONSECUTIVE", 2)
	if err != nil {
		return err
	}
	dynamicUserTPSRedN, err := env.Int("DYNAMIC_SINGLE_USER_TPS_RED_CONSECUTIVE", 3)
	if err != nil {
		return err
	}
	dynamicUserTPSGraceMinSeconds, err := env.Float("DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_MIN_SECONDS", 2)
	if err != nil {
		return err
	}
	dynamicUserTPSGraceMaxSeconds, err := env.Float("DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_MAX_SECONDS", 30)
	if err != nil {
		return err
	}
	dynamicUserTPSGraceBps, err := env.Float("DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_BODY_BYTES_PER_SECOND", 65536)
	if err != nil {
		return err
	}
	dynamicUserTPSGraceMul, err := env.Float("DYNAMIC_SINGLE_USER_TPS_PREFILL_GRACE_MULTIPLIER", 1)
	if err != nil {
		return err
	}
	dynamicUserTPSCapacityRatio, err := env.Float("DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO", 0.42)
	if err != nil {
		return err
	}
	dynamicUserTPSCapacitySmoothing, err := env.Float("DYNAMIC_SINGLE_USER_TPS_CAPACITY_SMOOTHING", 0.85)
	if err != nil {
		return err
	}
	dynamicUserTPSCapacityLearn, err := env.Bool("DYNAMIC_SINGLE_USER_TPS_CAPACITY_LEARN_ENABLED", dynamicEnabled)
	if err != nil {
		return err
	}
	dynamicUserTPSCapacityRatioMax, err := env.Float("DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO_MAX", 0.85)
	if err != nil {
		return err
	}
	dynamicUserTPSCapacityStepUp, err := env.Float("DYNAMIC_SINGLE_USER_TPS_CAPACITY_RATIO_STEP_UP", 0.02)
	if err != nil {
		return err
	}
	dynamicUserTPSCapacityHealthyN, err := env.Int("DYNAMIC_SINGLE_USER_TPS_CAPACITY_HEALTHY_CONSECUTIVE", 10)
	if err != nil {
		return err
	}
	dynamicUserTPSCapacityHealthyMul, err := env.Float("DYNAMIC_SINGLE_USER_TPS_CAPACITY_HEALTHY_MULTIPLIER", 1.5)
	if err != nil {
		return err
	}
	dynamicGlobalGreen, err := env.Int("DYNAMIC_GLOBAL_GREEN_LIMIT", cfg.GlobalLimit)
	if err != nil {
		return err
	}
	dynamicGlobalYellow, err := env.Int("DYNAMIC_GLOBAL_YELLOW_LIMIT", dynamicGlobalGreen)
	if err != nil {
		return err
	}
	dynamicGlobalRed, err := env.Int("DYNAMIC_GLOBAL_RED_LIMIT", dynamicGlobalYellow)
	if err != nil {
		return err
	}
	cfg.DynamicEnabled = dynamicEnabled
	cfg.DynamicEnforce = dynamicEnforce
	cfg.DynamicPollInterval = time.Duration(dynamicPollIntervalMs) * time.Millisecond
	cfg.DynamicFailsafeState = strings.ToLower(env.String("DYNAMIC_FAILSAFE_STATE", "yellow"))
	cfg.DynamicKVYellow = dynamicKVYellow
	cfg.DynamicKVRed = dynamicKVRed
	cfg.DynamicRunningYellow = dynamicRunningYellow
	cfg.DynamicRunningRed = dynamicRunningRed
	cfg.DynamicWaitingYellow = dynamicWaitingYellow
	cfg.DynamicWaitingRed = dynamicWaitingRed
	cfg.DynamicPreemptRed = uint64(dynamicPreemptRed)
	cfg.DynamicPressureEnabled = dynamicPressureEnabled
	cfg.DynamicPressureHeadroom = dynamicPressureHeadroom
	cfg.DynamicPressureMinLimit = dynamicPressureMinLimit
	cfg.DynamicPressureLearnRatio = dynamicPressureLearnRatio
	cfg.DynamicPressureLearnMinRunning = dynamicPressureLearnMinRunning
	cfg.DynamicUserTPSEnabled = dynamicUserTPSEnabled
	cfg.DynamicTTFTEnabled = dynamicTTFTEnabled
	cfg.DynamicUserTPSYellow = dynamicUserTPSYellow
	cfg.DynamicUserTPSRed = dynamicUserTPSRed
	cfg.DynamicUserTPSMinRun = dynamicUserTPSMinRun
	cfg.DynamicUserTPSYellowN = dynamicUserTPSYellowN
	cfg.DynamicUserTPSRedN = dynamicUserTPSRedN
	cfg.DynamicUserTPSGraceMin = time.Duration(dynamicUserTPSGraceMinSeconds * float64(time.Second))
	cfg.DynamicUserTPSGraceMax = time.Duration(dynamicUserTPSGraceMaxSeconds * float64(time.Second))
	cfg.DynamicUserTPSGraceBps = dynamicUserTPSGraceBps
	cfg.DynamicUserTPSGraceMul = dynamicUserTPSGraceMul
	cfg.DynamicUserTPSCapacityRatio = dynamicUserTPSCapacityRatio
	cfg.DynamicUserTPSCapacitySmoothing = dynamicUserTPSCapacitySmoothing
	cfg.DynamicUserTPSCapacityLearn = dynamicUserTPSCapacityLearn
	cfg.DynamicUserTPSCapacityRatioMax = dynamicUserTPSCapacityRatioMax
	cfg.DynamicUserTPSCapacityStepUp = dynamicUserTPSCapacityStepUp
	cfg.DynamicUserTPSCapacityHealthyN = dynamicUserTPSCapacityHealthyN
	cfg.DynamicUserTPSCapacityHealthyMul = dynamicUserTPSCapacityHealthyMul
	cfg.DynamicGlobalGreen = dynamicGlobalGreen
	cfg.DynamicGlobalYellow = dynamicGlobalYellow
	cfg.DynamicGlobalRed = dynamicGlobalRed
	return nil
}
