package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type admissionBackendObserverConfig struct {
	BackendKind           string
	MetricsURL            string
	UpstreamURL           string
	ModelName             string
	RevalidateMetadata    bool
	CapabilityFingerprint string
	MaxModelLenTokens     int64
	KVCapacityTokens      int64
	KVBlockSize           int64
	PollInterval          time.Duration
	MaximumAge            time.Duration
	RequestTimeout        time.Duration
	Controller            *coreadmission.AdmissionController
	Now                   func() time.Time
}

type admissionBackendObserver struct {
	pollMu                sync.Mutex
	backendKind           string
	metricsURL            string
	capabilityFingerprint string
	maxModelLenTokens     int64
	kvCapacityTokens      int64
	kvBlockSize           int64
	pollInterval          time.Duration
	maximumAge            time.Duration
	controller            *coreadmission.AdmissionController
	now                   func() time.Time
	client                *http.Client
	metadataClient        *http.Client
	upstreamURL           string
	modelName             string
	revalidateMetadata    bool
	metadataAvailable     bool
	requestTimeout        time.Duration
	rebindCandidate       admissionCapabilityRebindCandidate
	hasRebindCandidate    bool
	cancel                context.CancelFunc
	done                  chan struct{}
	closeOnce             sync.Once
}

type admissionCapabilityRebindCandidate struct {
	capabilityFingerprint string
	maxModelLenTokens     int64
	kvCapacityTokens      int64
	kvBlockSize           int64
	runtimeStartTime      float64
}

type admissionSampleDisposition uint8

const (
	admissionSampleTransient admissionSampleDisposition = iota
	admissionSampleUsable
	admissionSampleCapabilityDrift
)

func newAdmissionBackendObserver(config admissionBackendObserverConfig) (*admissionBackendObserver, error) {
	parsed, err := url.Parse(config.MetricsURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("admission backend metrics URL is invalid")
	}
	backendKind := strings.TrimSpace(config.BackendKind)
	fingerprint := strings.ToLower(strings.TrimSpace(config.CapabilityFingerprint))
	if (backendKind != "vllm" && backendKind != "sglang") ||
		!validPredictiveModelIdentitySHA256(fingerprint) || config.MaxModelLenTokens <= 0 ||
		config.KVCapacityTokens <= 0 || config.KVBlockSize <= 0 || config.PollInterval <= 0 ||
		config.MaximumAge < config.PollInterval || config.RequestTimeout <= 0 || config.Controller == nil {
		return nil, fmt.Errorf("admission backend observer configuration is invalid")
	}
	metadataAvailable := strings.TrimSpace(config.ModelName) != "" || strings.TrimSpace(config.UpstreamURL) != ""
	if config.RevalidateMetadata || metadataAvailable {
		if strings.TrimSpace(config.ModelName) == "" {
			return nil, fmt.Errorf("admission backend observer metadata identity is invalid")
		}
		if _, err := predictiveUpstreamEndpoint(config.UpstreamURL, "/v1/models"); err != nil {
			return nil, fmt.Errorf("admission backend observer metadata endpoint is invalid")
		}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	observer := &admissionBackendObserver{
		backendKind:           backendKind,
		metricsURL:            config.MetricsURL,
		capabilityFingerprint: fingerprint,
		maxModelLenTokens:     config.MaxModelLenTokens,
		kvCapacityTokens:      config.KVCapacityTokens,
		kvBlockSize:           config.KVBlockSize,
		pollInterval:          config.PollInterval,
		maximumAge:            config.MaximumAge,
		controller:            config.Controller,
		now:                   now,
		client:                &http.Client{Timeout: config.RequestTimeout, Transport: transport},
		metadataClient:        newPredictiveMetadataHTTPClient(),
		upstreamURL:           config.UpstreamURL,
		modelName:             config.ModelName,
		revalidateMetadata:    config.RevalidateMetadata,
		metadataAvailable:     metadataAvailable,
		requestTimeout:        config.RequestTimeout,
		done:                  make(chan struct{}),
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
	sample, err := prometheus.FetchSampleContext(ctx, o.client, o.metricsURL)
	if err != nil {
		o.resetRebindCandidate()
		return
	}
	observation, disposition := o.observation(sample, o.now())
	if disposition == admissionSampleTransient {
		o.resetRebindCandidate()
		return
	}
	if disposition == admissionSampleCapabilityDrift && o.isRuntimeCapacityRebindCandidate(observation) {
		if !o.controller.BeginRuntimeRebind(observation) {
			o.resetRebindCandidate()
		} else {
			if !o.revalidateObservationMetadata(ctx, &observation) {
				o.resetRebindCandidate()
				return
			}
			if !o.confirmRebindCandidate(observation) {
				return
			}
		}
	} else {
		o.resetRebindCandidate()
		if disposition == admissionSampleUsable && o.requiresMetadataRevalidation(observation) &&
			!o.revalidateObservationMetadata(ctx, &observation) {
			return
		}
	}
	previous := o.controller.Snapshot(observation.ObservedAt)
	result := o.controller.PublishObservation(window, observation)
	if result.Accepted && result.CapabilityRebound {
		oldCapacity := o.kvCapacityTokens
		o.kvCapacityTokens = observation.KVCapacityTokens
		accepted := o.controller.Snapshot(observation.ObservedAt)
		log.Printf(
			"level=info component=capability event=runtime_epoch_rebound backend_kind=%s old_runtime_start=%.6f new_runtime_start=%.6f old_kv_capacity_tokens=%d new_kv_capacity_tokens=%d runtime_epoch=%d capability_rebinds=%d",
			o.backendKind,
			previous.Observation.RuntimeStartTime,
			observation.RuntimeStartTime,
			oldCapacity,
			observation.KVCapacityTokens,
			result.RuntimeEpoch,
			accepted.CapabilityRebinds,
		)
	}
	if !result.Accepted && result.Reason != coreadmission.ReasonObservationInvalid && o.cancel != nil {
		o.cancel()
	}
}

func (o *admissionBackendObserver) isRuntimeCapacityRebindCandidate(
	observation coreadmission.BackendObservation,
) bool {
	if o == nil || o.controller == nil || !o.metadataAvailable ||
		observation.CapabilityFingerprint != o.capabilityFingerprint ||
		observation.MaxModelLenTokens != o.maxModelLenTokens ||
		observation.KVBlockSize != o.kvBlockSize ||
		observation.KVCapacityTokens == o.kvCapacityTokens {
		return false
	}
	current := o.controller.Snapshot(observation.ObservedAt)
	return current.HasObservation && current.Observation.RuntimeStartTime > 0 &&
		observation.RuntimeStartTime > 0 &&
		observation.RuntimeStartTime != current.Observation.RuntimeStartTime
}

func (o *admissionBackendObserver) revalidateObservationMetadata(
	ctx context.Context,
	observation *coreadmission.BackendObservation,
) bool {
	if o == nil || observation == nil || !o.metadataAvailable || o.metadataClient == nil {
		return false
	}
	metadataContext, cancel := context.WithTimeout(ctx, o.requestTimeout)
	defer cancel()
	metadata, err := fetchPredictiveModelMetadata(
		metadataContext,
		o.metadataClient,
		o.upstreamURL,
		o.modelName,
	)
	if err != nil {
		return false
	}
	observation.MaxModelLenTokens = metadata.MaxModelLen
	return true
}

func (o *admissionBackendObserver) confirmRebindCandidate(
	observation coreadmission.BackendObservation,
) bool {
	candidate := admissionCapabilityRebindCandidate{
		capabilityFingerprint: observation.CapabilityFingerprint,
		maxModelLenTokens:     observation.MaxModelLenTokens,
		kvCapacityTokens:      observation.KVCapacityTokens,
		kvBlockSize:           observation.KVBlockSize,
		runtimeStartTime:      observation.RuntimeStartTime,
	}
	if !o.hasRebindCandidate || o.rebindCandidate != candidate {
		o.rebindCandidate = candidate
		o.hasRebindCandidate = true
		return false
	}
	o.resetRebindCandidate()
	return true
}

func (o *admissionBackendObserver) resetRebindCandidate() {
	if o == nil {
		return
	}
	o.rebindCandidate = admissionCapabilityRebindCandidate{}
	o.hasRebindCandidate = false
}

func (o *admissionBackendObserver) requiresMetadataRevalidation(
	observation coreadmission.BackendObservation,
) bool {
	if o == nil || !o.revalidateMetadata || o.controller == nil {
		return false
	}
	current := o.controller.Snapshot(observation.ObservedAt)
	if !current.HasObservation {
		return false
	}
	return observation.GenerationTokensTotal < current.Observation.GenerationTokensTotal ||
		observation.PreemptionsTotal < current.Observation.PreemptionsTotal ||
		(observation.RuntimeStartTime > 0 && current.Observation.RuntimeStartTime > 0 &&
			observation.RuntimeStartTime != current.Observation.RuntimeStartTime)
}

func (o *admissionBackendObserver) observation(
	sample telemetry.Sample,
	observedAt time.Time,
) (coreadmission.BackendObservation, admissionSampleDisposition) {
	maximumInt := int(^uint(0) >> 1)
	if sample.BackendKind == "" || !sample.ModelNameValid || !sample.KVTokenMetricsValid || !sample.KVBlockSizeValid ||
		!sample.RunningValid || !sample.WaitingValid || !sample.PreemptionsValid || !sample.GenerationValid ||
		sample.KVCapacityTokens <= 0 || sample.KVBlockSize <= 0 || sample.KVUsedTokens < 0 ||
		sample.KVUsedTokens > sample.KVCapacityTokens || sample.Running < 0 || sample.Waiting < 0 ||
		sample.Running > maximumInt-sample.Waiting || observedAt.IsZero() {
		return coreadmission.BackendObservation{}, admissionSampleTransient
	}
	fingerprint := predictiveModelIdentitySHA256(sample.ModelName)
	disposition := admissionSampleUsable
	if sample.BackendKind != o.backendKind || fingerprint != o.capabilityFingerprint ||
		sample.KVCapacityTokens != o.kvCapacityTokens || int64(sample.KVBlockSize) != o.kvBlockSize {
		disposition = admissionSampleCapabilityDrift
		if sample.BackendKind != o.backendKind {
			fingerprint = "capability-drift"
		}
	}
	return coreadmission.BackendObservation{
		CapabilityFingerprint: fingerprint,
		MaxModelLenTokens:     o.maxModelLenTokens,
		KVCapacityTokens:      sample.KVCapacityTokens,
		KVBlockSize:           int64(sample.KVBlockSize),
		ObservedAt:            observedAt,
		MaximumAge:            o.maximumAge,
		UsedKVTokens:          sample.KVUsedTokens,
		Running:               int64(sample.Running),
		Waiting:               int64(sample.Waiting),
		GenerationTokensTotal: sample.Generation,
		PreemptionsTotal:      sample.Preemptions,
		CacheQueryTokensTotal: sample.CacheQueryTokens,
		CacheHitTokensTotal:   sample.CacheHitTokens,
		CacheCountersValid:    sample.CacheTokensValid,
		RuntimeStartTime:      sample.RuntimeStartTime,
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
		if o.metadataClient != nil {
			o.metadataClient.CloseIdleConnections()
		}
	})
	return nil
}
