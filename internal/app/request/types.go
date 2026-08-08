package request

import (
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
)

type Config struct {
	Paths             []string
	SuffixMatch       bool
	MaximumBodyBytes  int64
	MaximumConcurrent int
	OutputTokenFields []string
	Estimator         kvadmission.EstimatorConfig
}

type Classifier struct {
	cfg      Config
	tokens   chan struct{}
	inflight atomic.Int64
	rejected atomic.Uint64
}

type Classification struct {
	Cost   kvadmission.Cost
	Timing ClassificationTiming
}

type ClassificationTiming struct {
	BodyRead          time.Duration
	Estimator         time.Duration
	BodyReadMeasured  bool
	EstimatorMeasured bool
}

type ProtocolError struct {
	Reason string
}
