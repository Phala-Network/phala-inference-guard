package dynamic

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/decision"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
)

func (c *Controller) initialSnapshot(source string) dynamic.Snapshot {
	state := "disabled"
	globalLimit := c.recommendedGlobalLimit(state)
	backendCount := c.backendCount()
	backendFailed := 0
	yellowReasons := []string{}
	finalLimitReason := "disabled"
	if c.cfg.Enabled {
		state = c.cfg.FailsafeState
		globalLimit = 0
		backendFailed = backendCount
		yellowReasons = append(yellowReasons, "backend_unavailable")
		finalLimitReason = "backend_unavailable"
	}
	return dynamic.Snapshot{
		Enabled:                    c.cfg.Enabled,
		Enforce:                    c.cfg.Enforce,
		Decision:                   decision.New(state, yellowReasons, nil, decision.Limits{HardGlobal: c.globalLimit(), BaseGlobal: globalLimit, State: globalLimit, QOS: globalLimit, Throughput: globalLimit, Capacity: globalLimit, TTFT: globalLimit, Pressure: globalLimit, Prefill: globalLimit, Availability: globalLimit, Final: globalLimit}),
		State:                      state,
		Source:                     source,
		Updated:                    time.Now(),
		BackendCount:               backendCount,
		BackendFailed:              backendFailed,
		GlobalLimit:                globalLimit,
		FinalLimitReason:           finalLimitReason,
		QOSLimit:                   globalLimit,
		CapacityRatio:              c.cfg.UserTPSCapacityRatio,
		CapacityLimit:              globalLimit,
		CapacityEstimateConfidence: "startup",
		HardGlobalLimit:            c.globalLimit(),
		StateLimit:                 globalLimit,
		ThroughputLimit:            globalLimit,
		AvailabilityLimit:          globalLimit,
		CapacityLearnReason:        "startup",
		CapacityTargetReason:       "startup",
		CapacityProjectedLimit:     globalLimit,
		PressureLimit:              globalLimit,
		PressureReason:             "startup",
		PressureTargetReason:       "startup",
		PrefillLimit:               globalLimit,
		PrefillReason:              "startup",
		PrefillTargetReason:        "startup",
		TTFTLearnState:             "startup",
		TTFTLearnReason:            "startup",
		TTFTTargetReason:           "startup",
		TTFTLearnedLimit:           globalLimit,
		TTFTTargetLimit:            globalLimit,
		TTFTLimit:                  globalLimit,
		YellowReasons:              yellowReasons,
	}
}

func (c *Controller) storeError(err error) {
	c.pollFailed.Add(1)
	state := c.cfg.FailsafeState
	backendCount := c.backendCount()
	backendFailed := backendCount
	c.snapshot.Store(dynamic.Snapshot{
		Enabled:                    c.cfg.Enabled,
		Enforce:                    c.cfg.Enforce,
		Decision:                   decision.New(state, []string{"backend_unavailable"}, nil, decision.Limits{Availability: 0, Final: 0}),
		State:                      state,
		Source:                     "error",
		Error:                      err.Error(),
		Updated:                    time.Now(),
		BackendCount:               backendCount,
		BackendFailed:              backendFailed,
		GlobalLimit:                0,
		FinalLimitReason:           "backend_unavailable",
		QOSLimit:                   0,
		CapacityRatio:              c.cfg.UserTPSCapacityRatio,
		CapacityLimit:              0,
		CapacityEstimateConfidence: "unavailable",
		HardGlobalLimit:            c.globalLimit(),
		StateLimit:                 0,
		ThroughputLimit:            0,
		AvailabilityLimit:          0,
		CapacityLearnReason:        "backend_unavailable",
		CapacityTargetReason:       "backend_unavailable",
		CapacityProjectedLimit:     0,
		PressureLimit:              0,
		PressureReason:             "backend_unavailable",
		PressureTargetReason:       "backend_unavailable",
		PrefillLimit:               0,
		PrefillReason:              "backend_unavailable",
		PrefillTargetReason:        "backend_unavailable",
		TTFTLearnState:             "backend_unavailable",
		TTFTLearnReason:            "backend_unavailable",
		TTFTTargetReason:           "backend_unavailable",
		TTFTLearnedLimit:           0,
		TTFTTargetLimit:            0,
		TTFTLimit:                  0,
		YellowReasons:              []string{"backend_unavailable"},
	})
	c.notify()
}

func (c *Controller) recommendedGlobalLimit(state string) int {
	value := c.cfg.GlobalGreen
	switch state {
	case "yellow":
		value = c.cfg.GlobalYellow
	case "red":
		value = c.cfg.GlobalRed
	}
	globalLimit := c.globalLimit()
	if value > globalLimit {
		value = globalLimit
	}
	return value
}
