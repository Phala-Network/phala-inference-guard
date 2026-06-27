package request

import (
	"sync/atomic"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/lane"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/token"
)

type Config struct {
	QoSPaths              []string
	PathSuffixMatch       bool
	ClassifyOutputTokens  bool
	JSONClassifyBodyBytes int64
	JSONClassifyLimit     int
	OutputTokenFields     []string
	MediumBodyBytes       int64
	LongBodyBytes         int64
	VeryLongBodyBytes     int64
	MediumOutputTokens    int
	LongOutputTokens      int
	VeryLongOutputTokens  int
	AdaptiveOutput        bool
	AdaptiveOutputWindow  int
	AdaptiveOutputMin     int
	AdaptiveOutputMediumQ float64
	AdaptiveOutputLongQ   float64
	AdaptiveOutputVeryQ   float64
	AdaptiveOutputGreen   float64
	AdaptiveOutputYellow  float64
	AdaptiveOutputRed     float64
	DynamicEnabled        bool
	DynamicFailsafeState  string
}

type Lanes struct {
	Default        *lane.Lane
	MediumBody     *lane.Lane
	LongBody       *lane.Lane
	VeryLongBody   *lane.Lane
	UnknownBody    *lane.Lane
	MediumOutput   *lane.Lane
	LongOutput     *lane.Lane
	VeryLongOutput *lane.Lane
}

type Classifier struct {
	cfg       Config
	lanes     Lanes
	stateFunc func() string
	tokens    chan struct{}
	inflight  atomic.Int64
	rejected  atomic.Uint64
	outputs   *token.Window
}
