package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type admissionVLLMObserverConfig struct {
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

type admissionVLLMObserver struct {
	pollMu                sync.Mutex
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
	requestTimeout        time.Duration
	cancel                context.CancelFunc
	done                  chan struct{}
	closeOnce             sync.Once
}

type admissionSampleDisposition uint8

const (
	admissionSampleTransient admissionSampleDisposition = iota
	admissionSampleUsable
	admissionSampleCapabilityDrift
)

func newAdmissionVLLMObserver(config admissionVLLMObserverConfig) (*admissionVLLMObserver, error) {
	parsed, err := url.Parse(config.MetricsURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("admission vLLM metrics URL is invalid")
	}
	fingerprint := strings.ToLower(strings.TrimSpace(config.CapabilityFingerprint))
	if !validPredictiveModelIdentitySHA256(fingerprint) || config.MaxModelLenTokens <= 0 ||
		config.KVCapacityTokens <= 0 || config.KVBlockSize <= 0 ||
		config.PollInterval <= 0 || config.MaximumAge < config.PollInterval ||
		config.RequestTimeout <= 0 || config.Controller == nil {
		return nil, fmt.Errorf("admission vLLM observer configuration is invalid")
	}
	if config.RevalidateMetadata {
		if strings.TrimSpace(config.ModelName) == "" {
			return nil, fmt.Errorf("admission vLLM observer metadata identity is invalid")
		}
		if _, err := predictiveUpstreamEndpoint(config.UpstreamURL, "/v1/models"); err != nil {
			return nil, fmt.Errorf("admission vLLM observer metadata endpoint is invalid")
		}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	observer := &admissionVLLMObserver{
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
		requestTimeout:        config.RequestTimeout,
		done:                  make(chan struct{}),
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer.cancel = cancel
	go observer.loop(ctx)
	return observer, nil
}

func (o *admissionVLLMObserver) loop(ctx context.Context) {
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

func (o *admissionVLLMObserver) poll(ctx context.Context) {
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
		return
	}
	observation, disposition := o.observation(sample, o.now())
	if disposition == admissionSampleTransient {
		return
	}
	if disposition == admissionSampleUsable && o.requiresMetadataRevalidation(observation) {
		metadataContext, cancel := context.WithTimeout(ctx, o.requestTimeout)
		metadata, metadataErr := fetchPredictiveModelMetadata(
			metadataContext,
			o.metadataClient,
			o.upstreamURL,
			o.modelName,
		)
		cancel()
		if metadataErr != nil {
			return
		}
		observation.MaxModelLenTokens = metadata.MaxModelLen
	}
	result := o.controller.PublishObservation(window, observation)
	if !result.Accepted && result.Reason != coreadmission.ReasonObservationInvalid && o.cancel != nil {
		o.cancel()
	}
}

func (o *admissionVLLMObserver) requiresMetadataRevalidation(
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

func (o *admissionVLLMObserver) observation(
	sample telemetry.Sample,
	observedAt time.Time,
) (coreadmission.BackendObservation, admissionSampleDisposition) {
	maximumInt := int(^uint(0) >> 1)
	if sample.BackendKind == "" || !sample.ModelNameValid || !sample.KVTokenMetricsValid || !sample.KVBlockSizeValid ||
		!sample.RunningValid || !sample.WaitingValid || !sample.PreemptionsValid ||
		!sample.GenerationValid || sample.KVCapacityTokens <= 0 || sample.KVBlockSize <= 0 ||
		sample.KVUsedTokens < 0 || sample.KVUsedTokens > sample.KVCapacityTokens ||
		sample.Running < 0 || sample.Waiting < 0 || sample.Running > maximumInt-sample.Waiting ||
		observedAt.IsZero() {
		return coreadmission.BackendObservation{}, admissionSampleTransient
	}
	fingerprint := predictiveModelIdentitySHA256(sample.ModelName)
	disposition := admissionSampleUsable
	if sample.BackendKind != "vllm" || fingerprint != o.capabilityFingerprint ||
		sample.KVCapacityTokens != o.kvCapacityTokens || int64(sample.KVBlockSize) != o.kvBlockSize {
		disposition = admissionSampleCapabilityDrift
		if sample.BackendKind != "vllm" {
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
		RuntimeStartTime:      sample.RuntimeStartTime,
	}, disposition
}

func (o *admissionVLLMObserver) Close() error {
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
