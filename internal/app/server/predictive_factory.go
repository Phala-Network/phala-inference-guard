package server

import (
	"context"
	"fmt"
	"log"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

func newDefaultAdmissionService(cfg config) (admissionService, error) {
	return newDefaultAdmissionServiceContext(context.Background(), cfg)
}

func newDefaultAdmissionServiceContext(ctx context.Context, cfg config) (admissionService, error) {
	metricsURLs, err := predictiveBackendMetricsURLs(cfg)
	if err != nil {
		return nil, err
	}
	startup, err := waitPredictiveBackendStartup(ctx, predictiveBackendStartupProbeConfig{
		MetricsURLs:    metricsURLs,
		StartupTimeout: cfg.PredictiveStartupProbeTimeout,
		RequestTimeout: cfg.PredictiveMetricsRequestTimeout,
		RetryInterval:  cfg.PredictiveObservationPollInterval,
	})
	if err != nil {
		return nil, err
	}
	runningLimit := initializePredictiveRunningLimit(ctx, cfg, startup, metricsURLs)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	log.Printf(
		"level=info component=tps_controller event=initialized backend_kind=%s model_identity=%s tps_reference=%.6f observation_poll_ms=%d window_concurrency=%d running_limit=%d running_limit_source=%s",
		startup.BackendKind,
		startup.ModelIdentitySHA256,
		cfg.PredictiveTPSReference,
		cfg.PredictiveObservationPollInterval.Milliseconds(),
		cfg.PredictiveWindowConcurrency,
		runningLimit.Value,
		runningLimit.Source,
	)
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		RuntimeIdentity:               startup.ModelIdentitySHA256,
		TPS:                           coreadmission.TPSPolicyConfig{Reference: cfg.PredictiveTPSReference},
		PDDecode:                      startup.PDDecode,
		WindowConcurrency:             cfg.PredictiveWindowConcurrency,
		RunningLimit:                  runningLimit.Value,
		RunningLimitSource:            runningLimit.Source,
		PendingFirstByteLeaseDuration: 3 * cfg.PredictiveObservationPollInterval,
	})
	if err != nil {
		return nil, fmt.Errorf("construct admission Controller: %w", err)
	}
	window, ok := controller.StartSampleWindow()
	if !ok {
		controller.Close()
		return nil, fmt.Errorf("initialize admission observation window")
	}
	publication := controller.PublishObservation(window, coreadmission.BackendObservation{
		RuntimeIdentity:       startup.ModelIdentitySHA256,
		ObservedAt:            startup.ObservedAt,
		MaximumAge:            cfg.PredictiveMaximumMetricsAge,
		Running:               int64(startup.Running),
		Waiting:               int64(startup.Waiting),
		DecodePending:         int64(startup.DecodePending),
		GenerationTokensTotal: startup.Generation,
		PreemptionsTotal:      startup.Preemptions,
		RuntimeStartTime:      startup.RuntimeStartTime,
		RuntimeEpochIdentity:  startup.RuntimeEpochIdentity,
	})
	if !publication.Accepted {
		controller.Close()
		return nil, fmt.Errorf("initialize admission observation: %s", publication.Reason)
	}
	runtime, err := newAdmissionRuntime(
		controller,
		newAdmissionReporter(defaultAdmissionDecisionLogInterval, newAdmissionDecisionLogger(cfg.LogLevel)),
		startup.BackendKind,
		cfg.PredictiveAdmissionMode,
		nil,
	)
	if err != nil {
		controller.Close()
		return nil, err
	}
	observer, err := newAdmissionBackendObserver(admissionBackendObserverConfig{
		BackendKind:     startup.BackendKind,
		MetricsURLs:     metricsURLs,
		RuntimeIdentity: startup.ModelIdentitySHA256,
		PollInterval:    cfg.PredictiveObservationPollInterval,
		MaximumAge:      cfg.PredictiveMaximumMetricsAge,
		RequestTimeout:  cfg.PredictiveMetricsRequestTimeout,
		Controller:      controller,
	})
	if err != nil {
		_ = runtime.Close()
		return nil, fmt.Errorf("construct admission backend observer: %w", err)
	}
	runtime.observer = observer
	return runtime, nil
}

func predictiveBackendMetricsURLs(cfg config) ([]string, error) {
	metricsURLs, err := predictiveMetricsURLSet(cfg.PredictiveMetricsURL, cfg.PredictiveMetricsURLs)
	if err != nil {
		return nil, fmt.Errorf("predictive backend metrics URL set is invalid")
	}
	return metricsURLs, nil
}
