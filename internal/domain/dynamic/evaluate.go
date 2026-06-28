package dynamic

import (
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/latency"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/tier"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type Config struct {
	Enabled        bool
	Enforce        bool
	KVYellow       float64
	KVRed          float64
	RunningYellow  int
	RunningRed     int
	WaitingYellow  int
	WaitingRed     int
	PreemptRed     uint64
	UserTPSEnabled bool
	TTFTEnabled    bool
	UserTPSYellow  float64
	UserTPSRed     float64
	UserTPSMinRun  int
	UserTPSYellowN int
	UserTPSRedN    int
	TTFTPolicy     latency.Policy
	CapacityRatio  float64
	CapacityStepUp float64
	GlobalGreen    int
	GlobalYellow   int
	GlobalRed      int
	Capacity       capacity.Config
}

type PreviousMetrics struct {
	Snapshot           dynamic.Snapshot
	UserTPSYellowCount int
	UserTPSRedCount    int
}

type Input struct {
	Now              time.Time
	Samples          []telemetry.Sample
	BackendFailed    int
	Previous         PreviousMetrics
	SemanticTTFT     telemetry.HistogramSample
	PrefillProtected int
	GlobalLimit      int
	QueueCurrent     int64
	DynamicRejected  uint64
	Tier             tier.Snapshot
	PressureCap      *capacity.PressureCap
}

type generationObservation struct {
	PreemptionDelta    uint64
	GenerationTPS      float64
	GenerationTPSValid bool
}

func Evaluate(cfg Config, input Input) dynamic.Snapshot {
	now := input.Now
	if now.IsZero() {
		now = time.Now()
	}
	result := evaluateCleanPipeline(cfg, input, now)
	return buildCleanSnapshot(cfg, input, now, result)
}
