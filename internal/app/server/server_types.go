package server

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/config/pigconfig"
	infrabackend "github.com/Phala-Network/phala-inference-guard/internal/infra/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/attestation"
)

const version = "PIG-v0.12.6"

var durationBucketsSeconds = histogram.DurationBucketsSeconds

type config = pigconfig.Config
type backendProxy = infrabackend.Proxy
type durationHistogram = histogram.DurationHistogram

type proxyResult struct {
	status      int
	total       time.Duration
	firstByte   time.Duration
	firstByteOK bool
	timedOut    bool
	proxyFailed bool
}

type predictiveShadowFailureCounters struct {
	close    atomic.Uint64
	decide   atomic.Uint64
	forward  atomic.Uint64
	prefill  atomic.Uint64
	terminal atomic.Uint64
}

func (c *predictiveShadowFailureCounters) add(phase string) {
	if c == nil {
		return
	}
	switch phase {
	case "forward":
		c.forward.Add(1)
	case "prefill":
		c.prefill.Add(1)
	case "terminal":
		c.terminal.Add(1)
	default:
		c.decide.Add(1)
	}
}

func loadConfig() (config, error)     { return pigconfig.Load() }
func validateConfig(cfg config) error { return pigconfig.Validate(cfg) }

type proxyServer struct {
	cfg                       config
	backend                   *backendProxy
	requestClassifier         *request.Classifier
	attestation               *attestation.Service
	predictiveShadow          predictiveAdmissionShadow
	closeOnce                 sync.Once
	closeErr                  error
	started                   time.Time
	nextPredictiveID          atomic.Uint64
	total429                  atomic.Uint64
	clientProtocolInvalidJSON atomic.Uint64
	backendUnavailable        atomic.Uint64
	predictiveEnforcedRejects atomic.Uint64
	predictiveShadowFailures  predictiveShadowFailureCounters
	decisionDuration          durationHistogram
	bodyReadDuration          durationHistogram
	estimatorDuration         durationHistogram
	proxyTTFB                 durationHistogram
	proxyTotal                durationHistogram
	internalOverhead          durationHistogram
	clientDisconnectQueue     atomic.Uint64
	clientDisconnectUpstream  atomic.Uint64
	clientDisconnectResponse  atomic.Uint64
	clientDisconnectCancel    atomic.Uint64
}
