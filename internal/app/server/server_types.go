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

// Keep the last released identity until a separate release step assigns a new version.
const version = "PIG-v0.12.24"

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

type admissionFailureCounters struct {
	close     atomic.Uint64
	decide    atomic.Uint64
	forward   atomic.Uint64
	firstByte atomic.Uint64
	terminal  atomic.Uint64
}

type predictivePolicyUpdateCounters struct {
	applied  atomic.Uint64
	invalid  atomic.Uint64
	conflict atomic.Uint64
	failed   atomic.Uint64
}

func loadConfig() (config, error)     { return pigconfig.Load() }
func validateConfig(cfg config) error { return pigconfig.Validate(cfg) }

type proxyServer struct {
	cfg                       config
	backend                   *backendProxy
	requestClassifier         *request.Classifier
	attestation               *attestation.Service
	admission                 admissionService
	publicRoutes              PublicRoutePolicy
	admissionRoutes           AdmissionRoutePolicy
	authentication            AuthenticationPolicy
	localManagementRoutes     LocalManagementRoutePolicy
	closeOnce                 sync.Once
	closeErr                  error
	started                   time.Time
	total429                  atomic.Uint64
	clientProtocolInvalidJSON atomic.Uint64
	backendUnavailable        atomic.Uint64
	routeNotAllowed           atomic.Uint64
	predictiveEnforcedRejects atomic.Uint64
	admissionFailures         admissionFailureCounters
	requestEvidence           requestEvidence
	responseUsageEvidence     responseUsageEvidence
	policyUpdates             predictivePolicyUpdateCounters
	decisionDuration          durationHistogram
	bodyReadDuration          durationHistogram
	shapeScanDuration         durationHistogram
	proxyTTFB                 durationHistogram
	proxyTotal                durationHistogram
	internalOverhead          durationHistogram
	clientDisconnectQueue     atomic.Uint64
	clientDisconnectUpstream  atomic.Uint64
	clientDisconnectResponse  atomic.Uint64
	clientDisconnectCancel    atomic.Uint64
}
