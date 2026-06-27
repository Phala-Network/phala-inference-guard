package dynamic

import (
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/capacity"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/tier"
	runtimebackend "github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type Backend interface {
	Name() string
	MetricsURL() string
	StoreStatus(runtimebackend.Runtime)
	ObserveMetricsFailure()
	UpdateStatusFromSample(telemetry.Sample) runtimebackend.Runtime
}

type Dependencies struct {
	Backends         []Backend
	GlobalLimit      func() int
	QueueCurrent     func() int64
	DynamicRejected  func() uint64
	TierSnapshot     func(globalLimit int) tier.Snapshot
	SemanticTTFT     func() telemetry.HistogramSample
	PrefillProtected func(time.Time) int
	Notify           func()
}

type Counters struct {
	PollOK     uint64
	PollFailed uint64
}

type Controller struct {
	cfg                 Config
	deps                Dependencies
	snapshot            atomic.Value
	lastMetricsSnapshot atomic.Value
	staticMetricsState  atomic.Value
	pollOK              atomic.Uint64
	pollFailed          atomic.Uint64
	pressureCap         capacity.PressureCap
}
