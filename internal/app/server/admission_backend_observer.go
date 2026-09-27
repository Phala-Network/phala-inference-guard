package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type admissionBackendObserverConfig struct {
	BackendKind     string
	MetricsURL      string
	MetricsURLs     []string
	RuntimeIdentity string
	PollInterval    time.Duration
	MaximumAge      time.Duration
	RequestTimeout  time.Duration
	Controller      *coreadmission.AdmissionController
	Now             func() time.Time
}

type admissionBackendObserver struct {
	pollMu          sync.Mutex
	backendKind     string
	metricsURLs     []string
	runtimeIdentity string
	pollInterval    time.Duration
	maximumAge      time.Duration
	controller      *coreadmission.AdmissionController
	now             func() time.Time
	client          *http.Client
	cancel          context.CancelFunc
	done            chan struct{}
	closeOnce       sync.Once
}

type admissionSampleDisposition uint8

const (
	admissionSampleTransient admissionSampleDisposition = iota
	admissionSampleUsable
	admissionSampleIdentityDrift
)

func newAdmissionBackendObserver(config admissionBackendObserverConfig) (*admissionBackendObserver, error) {
	metricsURLs, err := predictiveMetricsURLSet(config.MetricsURL, config.MetricsURLs)
	if err != nil {
		return nil, fmt.Errorf("admission backend metrics URL is invalid")
	}
	backendKind := strings.TrimSpace(config.BackendKind)
	identity := strings.ToLower(strings.TrimSpace(config.RuntimeIdentity))
	if (backendKind != "vllm" && backendKind != "sglang") ||
		!validPredictiveModelIdentitySHA256(identity) || config.PollInterval <= 0 ||
		config.MaximumAge < config.PollInterval || config.RequestTimeout <= 0 || config.Controller == nil {
		return nil, fmt.Errorf("admission backend observer configuration is invalid")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	observer := &admissionBackendObserver{
		backendKind:     backendKind,
		metricsURLs:     metricsURLs,
		runtimeIdentity: identity,
		pollInterval:    config.PollInterval,
		maximumAge:      config.MaximumAge,
		controller:      config.Controller,
		now:             now,
		client:          &http.Client{Timeout: config.RequestTimeout, Transport: transport},
		done:            make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer.cancel = cancel
	go observer.loop(ctx)
	return observer, nil
}

func (o *admissionBackendObserver) loop(ctx context.Context) {
	defer close(o.done)
	o.poll(ctx)
	ticker := time.NewTicker(o.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.poll(ctx)
		}
	}
}

func (o *admissionBackendObserver) poll(ctx context.Context) {
	if o == nil || o.controller == nil || o.client == nil {
		return
	}
	o.pollMu.Lock()
	defer o.pollMu.Unlock()
	window, ok := o.controller.StartSampleWindow()
	if !ok {
		return
	}
	sample, err := prometheus.FetchAggregateSampleContext(ctx, o.client, o.metricsURLs)
	if err != nil {
		return
	}
	observation, disposition := o.observation(sample, o.now())
	if disposition == admissionSampleTransient {
		return
	}
	result := o.controller.PublishObservation(window, observation)
	if !result.Accepted && result.Reason != coreadmission.ReasonObservationInvalid && o.cancel != nil {
		o.cancel()
	}
}

func (o *admissionBackendObserver) observation(
	sample telemetry.Sample,
	observedAt time.Time,
) (coreadmission.BackendObservation, admissionSampleDisposition) {
	if sample.BackendKind == "" || !sample.ModelNameValid || strings.TrimSpace(sample.ModelName) == "" ||
		!validPredictiveSampleRequestCounts(sample) || !sample.PreemptionsValid || !sample.GenerationValid ||
		observedAt.IsZero() {
		return coreadmission.BackendObservation{}, admissionSampleTransient
	}
	identity := predictiveSampleIdentitySHA256(sample)
	disposition := admissionSampleUsable
	if sample.BackendKind != o.backendKind || identity != o.runtimeIdentity {
		disposition = admissionSampleIdentityDrift
		if sample.BackendKind != o.backendKind {
			identity = "runtime-identity-drift"
		}
	}
	return coreadmission.BackendObservation{
		RuntimeIdentity:       identity,
		ObservedAt:            observedAt,
		MaximumAge:            o.maximumAge,
		Running:               int64(sample.Running),
		Waiting:               int64(sample.Waiting),
		DecodePending:         int64(sample.DecodePending),
		GenerationTokensTotal: sample.Generation,
		PreemptionsTotal:      sample.Preemptions,
		RuntimeStartTime:      sample.RuntimeStartTime,
		RuntimeEpochIdentity:  sample.RuntimeEpochIdentity,
	}, disposition
}

func (o *admissionBackendObserver) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		if o.cancel != nil {
			o.cancel()
		}
		if o.done != nil {
			<-o.done
		}
		if o.client != nil {
			o.client.CloseIdleConnections()
		}
	})
	return nil
}
