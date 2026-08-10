package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/app/request"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/openai"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/attestation"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type predictiveReservationContextKey struct{}

func newProxyServer(cfg config) (*proxyServer, error) {
	return newProxyServerWithDependencies(cfg, serverDependencies{NewPredictiveShadow: newDefaultPredictiveShadow})
}

func newProxyServerWithDependencies(cfg config, dependencies serverDependencies) (*proxyServer, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if dependencies.NewPredictiveShadow == nil {
		return nil, fmt.Errorf("predictive admission adapter is required")
	}
	predictive, err := dependencies.NewPredictiveShadow(cfg)
	if err != nil {
		return nil, fmt.Errorf("construct predictive admission adapter: %w", err)
	}
	if predictive == nil {
		return nil, fmt.Errorf("predictive admission adapter constructor returned nil")
	}
	backends, _, _, err := backend.Build([]backend.Config{{
		Name: "upstream", Upstream: strings.TrimRight(cfg.Upstream, "/"), MetricsURL: cfg.PredictiveMetricsURL,
	}})
	if err != nil || len(backends) != 1 {
		_ = predictive.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("construct exactly one upstream proxy")
	}
	attestationService, err := newAttestationService(cfg)
	if err != nil {
		_ = predictive.Close()
		return nil, err
	}
	srv := &proxyServer{
		cfg:               cfg,
		backend:           backends[0],
		attestation:       attestationService,
		predictiveShadow:  predictive,
		started:           time.Now(),
		decisionDuration:  newPredictiveDurationHistogram(),
		bodyReadDuration:  newPredictiveDurationHistogram(),
		estimatorDuration: newPredictiveDurationHistogram(),
		proxyTTFB:         newDurationHistogram(),
		proxyTotal:        newDurationHistogram(),
		internalOverhead:  newDurationHistogram(),
	}
	srv.requestClassifier = request.New(request.Config{
		Paths:             cfg.QoSPaths,
		SuffixMatch:       cfg.PathSuffixMatch,
		MaximumBodyBytes:  cfg.PredictiveScannerBodyBytes,
		MaximumConcurrent: cfg.PredictiveScannerConcurrency,
		OutputTokenFields: cfg.OutputTokenFields,
		Estimator:         cfg.PredictiveEstimator,
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
	if response != nil && response.Request != nil {
		if reservation, ok := response.Request.Context().Value(predictiveReservationContextKey{}).(predictiveShadowReservation); ok && reservation != nil {
			var onComplete func()
			if response.StatusCode >= http.StatusOK && response.StatusCode < http.StatusMultipleChoices {
				responseContext := response.Request.Context()
				onComplete = func() {
					if responseContext.Err() == nil {
						reservation.Terminate(runtimepredictive.TerminalCompleted)
					}
				}
			}
			response.Body = observePredictiveResponseBody(
				response.Body,
				func() { reservation.MarkPrefillComplete() },
				onComplete,
			)
		}
	}
	return nil
}

func attachPredictiveReservation(ctx context.Context, reservation predictiveShadowReservation) context.Context {
	if reservation == nil {
		return ctx
	}
	return context.WithValue(ctx, predictiveReservationContextKey{}, reservation)
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
