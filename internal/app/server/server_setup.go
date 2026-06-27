package server

import (
	"net/http"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/app/dynamic"
	"github.com/Phala-Network/phala-inference-guard/internal/app/gate"
	"github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/attestation"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/prefill"
)

func newProxyServer(cfg config) (*proxyServer, error) {
	if len(cfg.Backends) == 0 && cfg.Upstream != "" {
		metricsURL := ""
		if len(cfg.DynamicMetricsURLs) > 0 {
			metricsURL = cfg.DynamicMetricsURLs[0]
		}
		cfg.Backends = []backendConfig{{Name: "backend1", Upstream: strings.TrimRight(cfg.Upstream, "/"), MetricsURL: metricsURL}}
	}
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	backends, target, proxy, err := backend.Build(backendProxyConfigs(cfg.Backends))
	if err != nil {
		return nil, err
	}
	attestationService, err := newAttestationService(cfg)
	if err != nil {
		return nil, err
	}
	srv := &proxyServer{
		cfg:                 cfg,
		target:              target,
		proxy:               proxy,
		backends:            backends,
		globalLn:            newLane("global", cfg.GlobalLimit),
		defaultLn:           newLane("default", 0),
		mediumLn:            newLane("medium_body", 0),
		longLn:              newLane("long_body", 0),
		veryLongLn:          newLane("very_long_body", 0),
		mediumOutputLn:      newLane("medium_output", 0),
		longOutputLn:        newLane("long_output", 0),
		veryLongOutputLn:    newLane("very_long_output", 0),
		unknownLn:           newLane("unknown_body", 0),
		attestation:         attestationService,
		priorityInjector:    request.NewPriorityInjector(priorityInjectorConfig(cfg)),
		started:             time.Now(),
		activeRequests:      prefill.New(),
		decisionDuration:    newDurationHistogram(),
		proxyTTFB:           newDurationHistogram(),
		requestSemanticTTFT: newDurationHistogram(),
		proxyTotal:          newDurationHistogram(),
		internalOverhead:    newDurationHistogram(),
	}
	srv.qosGate = gate.New(gate.Config{
		QueueWait: cfg.QoSQueueWait,
		QueuePoll: cfg.QoSQueuePoll,
	}, srv.globalLn, srv.currentQoSLimit, srv.effectiveQoSQueueWait)
	srv.dynamicController = dynamic.New(dynamicQoSConfig(cfg), dynamic.Dependencies{
		Backends:         dynamicQoSBackends(srv.backends),
		GlobalLimit:      srv.globalLn.Limit,
		QueueCurrent:     srv.qosGate.QueueCurrent,
		DynamicRejected:  srv.qosGate.DynamicRejected,
		TierSnapshot:     srv.qosGate.TierSnapshot,
		SemanticTTFT:     srv.requestSemanticTTFT.Sample,
		PrefillProtected: srv.activeRequests.ProtectedCount,
		Notify:           srv.qosGate.Notify,
	})
	srv.requestClassifier = request.New(requestClassifierConfig(cfg), request.Lanes{
		Default:        srv.defaultLn,
		MediumBody:     srv.mediumLn,
		LongBody:       srv.longLn,
		VeryLongBody:   srv.veryLongLn,
		UnknownBody:    srv.unknownLn,
		MediumOutput:   srv.mediumOutputLn,
		LongOutput:     srv.longOutputLn,
		VeryLongOutput: srv.veryLongOutputLn,
	}, srv.currentDynamicState)
	for _, backend := range srv.backends {
		backend := backend
		backend.SetHandlers(srv.modifyBackendResponse, func(w http.ResponseWriter, r *http.Request, err error) {
			srv.recordProxyUpstreamError(backend)
			openai.WriteTooManyRequests(w)
		})
	}
	srv.dynamicController.Start()
	return srv, nil
}

func newAttestationService(cfg config) (*attestation.Service, error) {
	if !cfg.AttestationEnabled {
		return nil, nil
	}
	return attestation.NewService(attestation.Config{
		TLSCertPath:           cfg.AttestationTLSCertPath,
		GPUArch:               cfg.AttestationGPUArch,
		NVIDIAPayload:         cfg.AttestationNVIDIAPayload,
		NVIDIAPayloadFile:     cfg.AttestationNVIDIAPayloadFile,
		NVIDIACommand:         cfg.AttestationNVIDIACommand,
		NVIDIACommandArgs:     cfg.AttestationNVIDIACommandArgs,
		NVIDIACommandTimeout:  cfg.AttestationNVIDIACommandTimeout,
		RequireNVIDIAEvidence: cfg.AttestationRequireNVIDIAEvidence,
	}, attestation.NewDstackClient(cfg.AttestationDstackEndpoint, 3*time.Second))
}
