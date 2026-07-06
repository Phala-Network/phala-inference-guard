package server

import (
	"net/http/httputil"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/app/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/app/gate"
	"github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/config/pigconfig"
	domainlane "github.com/Phala-Network/phala-inference-guard/internal/domain/lane"
	infrabackend "github.com/Phala-Network/phala-inference-guard/internal/infra/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/attestation"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/prefill"
)

const version = "PIG-v0.8.11"

const maxQoSQueueWait = 500 * time.Millisecond

const severeDynamicQueueWait = 100 * time.Millisecond

const saturatedDynamicQueueWait = 250 * time.Millisecond

const sseKeepAliveInterval = 2 * time.Second

const sseKeepAliveMaxKVCacheUsage = 0.70

const sseBridgeHeaderGrace = 500 * time.Millisecond

var durationBucketsSeconds = histogram.DurationBucketsSeconds

var bodyBucketsBytes = []int64{16 * 1024, 64 * 1024, 128 * 1024, 512 * 1024, 1024 * 1024, 4 * 1024 * 1024, 16 * 1024 * 1024}

type config = pigconfig.Config
type backendConfig = pigconfig.Backend
type backendProxy = infrabackend.Proxy
type durationHistogram = histogram.DurationHistogram
type qosLane = domainlane.Lane

type proxyResult struct {
	status      int
	total       time.Duration
	firstByte   time.Duration
	firstByteOK bool
}

func loadConfig() (config, error) {
	return pigconfig.Load()
}

func validateConfig(cfg config) error {
	return pigconfig.Validate(cfg)
}

func newLane(name string, limit int) *qosLane {
	return domainlane.New(name, limit, domainlane.Buckets{
		DurationSeconds: durationBucketsSeconds,
		BodyBytes:       bodyBucketsBytes,
	})
}

type proxyServer struct {
	cfg                      config
	target                   *url.URL
	proxy                    *httputil.ReverseProxy
	backends                 []*backendProxy
	globalLn                 *qosLane
	defaultLn                *qosLane
	mediumLn                 *qosLane
	longLn                   *qosLane
	veryLongLn               *qosLane
	mediumOutputLn           *qosLane
	longOutputLn             *qosLane
	veryLongOutputLn         *qosLane
	unknownLn                *qosLane
	requestClassifier        *request.Classifier
	priorityInjector         *request.PriorityInjector
	attestation              *attestation.Service
	dynamicController        *dynamic.Controller
	started                  time.Time
	total429                 atomic.Uint64
	backendUnavailable       atomic.Uint64
	qosGate                  *gate.Gate
	activeRequests           *prefill.Tracker
	nextActiveID             atomic.Uint64
	decisionDuration         durationHistogram
	proxyTTFB                durationHistogram
	requestSemanticTTFT      durationHistogram
	proxyTotal               durationHistogram
	internalOverhead         durationHistogram
	sseKeepAliveStreams      atomic.Uint64
	sseKeepAliveComments     atomic.Uint64
	sseBridgeStreams         atomic.Uint64
	sseBridgeUpstreamErr     atomic.Uint64
	sseBridgeInvalid         atomic.Uint64
	sseBridgeCopyErr         atomic.Uint64
	semanticTTFTLimited      atomic.Uint64
	proxyUpstreamErr         atomic.Uint64
	proxyCopyErr             atomic.Uint64
	clientDisconnectQueue    atomic.Uint64
	clientDisconnectUpstream atomic.Uint64
	clientDisconnectResponse atomic.Uint64
	clientDisconnectCancel   atomic.Uint64
}
