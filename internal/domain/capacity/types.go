package capacity

import "sync/atomic"

const (
	defaultInitialLimit             = 50
	sparseProbeFloor                = 4
	demandProbeFloor                = 8
	MinUserTPSEnforceRunning        = 4
	prefillPressureFloorRatio       = 0.33
	prefillPressureFloorMin         = 16
	prefillPressureFloorMax         = 20
	representativeCapacityLoadRatio = 0.65
	ProvisionalGrowthRatio          = 0.25
	overloadWaitingDecayRatio       = 0.50
)

type Config struct {
	UserTPSEnabled      bool
	UserTPSYellow       float64
	UserTPSRed          float64
	UserTPSMinRun       int
	CapacityLearn       bool
	CapacitySafetyRatio float64
	CapacitySmoothing   float64
	CapacityStepUp      float64
	CapacityHealthyN    int
	CapacityHealthyMul  float64
	PressureEnabled     bool
	PressureHeadroom    int
	PressureMinLimit    int
	PressureLearnRatio  float64
	PressureLearnMinRun int
	KVYellow            float64
	KVRed               float64
	WaitingYellow       int
	WaitingRed          int
}

type Previous struct {
	Source                    string
	CapacityTPS               float64
	CapacityLearnedLimit      int
	CapacityTargetLimit       int
	CapacityRatioHealthyCount int
	CapacityLearnState        string
	PrefillTransition         bool
	PrefillSettling           bool
	GlobalLimit               int
	CapacityLimit             int
}

type PressureCap struct {
	value atomic.Int64
}

func (c *PressureCap) Load() int64 {
	return c.value.Load()
}

func (c *PressureCap) compareAndSwap(old, new int64) bool {
	return c.value.CompareAndSwap(old, new)
}
