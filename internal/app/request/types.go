package request

import (
	"bytes"
	"sync/atomic"
	"time"
)

type Config struct {
	MaximumBodyBytes  int64
	MaximumConcurrent int
}

type Classifier struct {
	cfg                      Config
	tokens                   chan struct{}
	bodyPool                 chan *bytes.Buffer
	maximumReservedBodyBytes int64
	inflight                 atomic.Int64
	reservedBodyBytes        atomic.Int64
	rejected                 atomic.Uint64
}

type Classification struct {
	Supported              bool
	SingleSequenceFallback bool
	UnsupportedReason      string
	Timing                 ClassificationTiming
	JSONFieldsKnown        bool
	StreamingPresent       bool
	StreamingKnown         bool
	Streaming              bool
	BasePromptCount        int64
	DecodeSequences        int64
}

type ClassificationTiming struct {
	BodyRead          time.Duration
	ShapeScan         time.Duration
	BodyReadMeasured  bool
	ShapeScanMeasured bool
}

type ProtocolError struct {
	Reason string
}
