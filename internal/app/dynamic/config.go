package dynamic

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/dynamic"
)

type Config struct {
	Enabled                   bool
	Enforce                   bool
	MetricsURLs               []string
	PollInterval              time.Duration
	FailsafeState             string
	BackendRouting            bool
	KVYellow                  float64
	KVRed                     float64
	RunningYellow             int
	RunningRed                int
	WaitingYellow             int
	WaitingRed                int
	PreemptRed                uint64
	PressureEnabled           bool
	PressureHeadroom          int
	PressureMinLimit          int
	PressureLearnRatio        float64
	PressureLearnMinRunning   int
	UserTPSEnabled            bool
	TTFTEnabled               bool
	UserTPSYellow             float64
	UserTPSRed                float64
	UserTPSMinRun             int
	UserTPSYellowN            int
	UserTPSRedN               int
	UserTPSGraceMin           time.Duration
	UserTPSGraceMax           time.Duration
	UserTPSGraceBps           float64
	UserTPSGraceMul           float64
	UserTPSCapacityRatio      float64
	UserTPSCapacityRatioMax   float64
	UserTPSCapacitySmoothing  float64
	UserTPSCapacityLearn      bool
	UserTPSCapacityStepUp     float64
	UserTPSCapacityHealthyN   int
	UserTPSCapacityHealthyMul float64
	GlobalGreen               int
	GlobalYellow              int
	GlobalRed                 int
}

func (cfg Config) capacityPolicyConfig() capacity.Config {
	return capacity.Config{
		UserTPSEnabled:      cfg.UserTPSEnabled,
		UserTPSYellow:       cfg.UserTPSYellow,
		UserTPSRed:          cfg.UserTPSRed,
		UserTPSMinRun:       cfg.UserTPSMinRun,
		CapacityLearn:       cfg.UserTPSCapacityLearn,
		CapacitySafetyRatio: cfg.UserTPSCapacityRatio,
		CapacitySmoothing:   cfg.UserTPSCapacitySmoothing,
		CapacityStepUp:      cfg.UserTPSCapacityStepUp,
		CapacityHealthyN:    cfg.UserTPSCapacityHealthyN,
		CapacityHealthyMul:  cfg.UserTPSCapacityHealthyMul,
		PressureEnabled:     cfg.PressureEnabled,
		PressureHeadroom:    cfg.PressureHeadroom,
		PressureMinLimit:    cfg.PressureMinLimit,
		PressureLearnRatio:  cfg.PressureLearnRatio,
		PressureLearnMinRun: cfg.PressureLearnMinRunning,
		KVYellow:            cfg.KVYellow,
		KVRed:               cfg.KVRed,
		WaitingYellow:       cfg.WaitingYellow,
		WaitingRed:          cfg.WaitingRed,
	}
}

func (cfg Config) policyConfig() dynamic.Config {
	return dynamic.Config{
		Enabled:        cfg.Enabled,
		Enforce:        cfg.Enforce,
		KVYellow:       cfg.KVYellow,
		KVRed:          cfg.KVRed,
		RunningYellow:  cfg.RunningYellow,
		RunningRed:     cfg.RunningRed,
		WaitingYellow:  cfg.WaitingYellow,
		WaitingRed:     cfg.WaitingRed,
		PreemptRed:     cfg.PreemptRed,
		UserTPSEnabled: cfg.UserTPSEnabled,
		TTFTEnabled:    cfg.TTFTEnabled,
		UserTPSYellow:  cfg.UserTPSYellow,
		UserTPSRed:     cfg.UserTPSRed,
		UserTPSMinRun:  cfg.UserTPSMinRun,
		UserTPSYellowN: cfg.UserTPSYellowN,
		UserTPSRedN:    cfg.UserTPSRedN,
		CapacityRatio:  cfg.UserTPSCapacityRatio,
		CapacityStepUp: cfg.UserTPSCapacityStepUp,
		GlobalGreen:    cfg.GlobalGreen,
		GlobalYellow:   cfg.GlobalYellow,
		GlobalRed:      cfg.GlobalRed,
		Capacity:       cfg.capacityPolicyConfig(),
	}
}
