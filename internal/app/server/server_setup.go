package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/attestation"
)

type admissionReservationContextKey struct{}

func newProxyServer(cfg config) (*proxyServer, error) {
	return newProxyServerWithDependencies(cfg, serverDependencies{NewAdmission: newDefaultAdmissionService})
}

func newProxyServerWithDependencies(cfg config, dependencies serverDependencies) (*proxyServer, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if dependencies.NewAdmission == nil {
		return nil, fmt.Errorf("admission service is required")
	}
	admission, err := dependencies.NewAdmission(cfg)
	if err != nil {
		return nil, fmt.Errorf("construct admission service: %w", err)
	}
	if admission == nil {
		return nil, fmt.Errorf("admission service constructor returned nil")
	}
	backends, _, _, err := backend.Build([]backend.Config{{
		Name: "upstream", Upstream: strings.TrimRight(cfg.Upstream, "/"), MetricsURL: cfg.PredictiveMetricsURL,
	}})
	if err != nil || len(backends) != 1 {
		_ = admission.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("construct exactly one upstream proxy")
	}
	attestationService, err := newAttestationService(cfg)
	if err != nil {
		_ = admission.Close()
		return nil, err
	}
	srv := &proxyServer{
		cfg:                   cfg,
		backend:               backends[0],
		attestation:           attestationService,
		admission:             admission,
		publicRoutes:          PublicRoutePolicy{},
		admissionRoutes:       AdmissionRoutePolicy{},
		authentication:        AuthenticationPolicy{enabled: cfg.APIAuthEnabled},
		localManagementRoutes: LocalManagementRoutePolicy{},
		started:               time.Now(),
		decisionDuration:      newPredictiveDurationHistogram(),
		bodyReadDuration:      newPredictiveDurationHistogram(),
		shapeScanDuration:     newPredictiveDurationHistogram(),
		proxyTTFB:             newDurationHistogram(),
		proxyTotal:            newDurationHistogram(),
		internalOverhead:      newDurationHistogram(),
	}
	srv.requestClassifier = request.New(request.Config{
		MaximumBodyBytes:  cfg.PredictiveScannerBodyBytes,
		MaximumConcurrent: cfg.PredictiveScannerConcurrency,
	})
	srv.backend.SetHandlers(srv.modifyBackendResponse, func(w http.ResponseWriter, r *http.Request, _ error) {
		if srv.recordClientDisconnect(r.Context(), clientDisconnectPhaseUpstream, true) {
			return
		}
		srv.backend.ObserveProxyError()
		openai.WriteTooManyRequests(w)
	})
	return srv, nil
}

func (s *proxyServer) modifyBackendResponse(response *http.Response) error {
	s.classifyUpstreamErrorResponse(response)
	if evidence := responseUsageRequestEvidenceFrom(response); evidence != nil {
		evidence.WrapResponse(response)
	}
	if response != nil && response.Request != nil {
		reservation, _ := response.Request.Context().Value(admissionReservationContextKey{}).(admissionReservation)
		var onFirst func()
		if reservation != nil {
			onFirst = func() {
				if !reservation.MarkFirstByte() {
					s.admissionFailures.firstByte.Add(1)
				}
			}
		}
		var onComplete func()
		if reservation != nil && response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
			responseContext := response.Request.Context()
			onComplete = func() {
				if responseContext.Err() == nil && !reservation.Terminate(coreadmission.TerminalSuccess) {
					s.admissionFailures.terminal.Add(1)
				}
			}
		}
		response.Body = observeAdmissionResponseBody(response.Body, onFirst, onComplete)
	}
	return nil
}

func attachAdmissionReservation(ctx context.Context, reservation admissionReservation) context.Context {
	if reservation == nil {
		return ctx
	}
	return context.WithValue(ctx, admissionReservationContextKey{}, reservation)
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
		NVIDIAPayloadURL:      cfg.AttestationNVIDIAPayloadURL,
		NVIDIAPayloadAuth:     cfg.AttestationNVIDIAPayloadAuth,
		NVIDIACommand:         cfg.AttestationNVIDIACommand,
		NVIDIACommandArgs:     cfg.AttestationNVIDIACommandArgs,
		NVIDIACommandTimeout:  cfg.AttestationNVIDIACommandTimeout,
		RequireNVIDIAEvidence: cfg.AttestationRequireNVIDIAEvidence,
	}, attestation.NewDstackClient(cfg.AttestationDstackEndpoint, 3*time.Second))
}
