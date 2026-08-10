package server

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type predictiveVLLMObserverConfig struct {
	MetricsURL          string
	ModelIdentitySHA256 string
	ServedModel         string
	MaximumKVTokens     int64
	BlockSize           int
	PollInterval        time.Duration
	MaximumAge          time.Duration
	RequestTimeout      time.Duration
	Coordinator         predictiveSampleCoordinator
	Initial             predictiveVLLMStartup
	Now                 func() time.Time
}

type predictiveSampleCoordinator interface {
	StartSampleWindow() uint64
	EventSequence() uint64
	ReconcileSample(runtimepredictive.SampleWindow) error
}

type predictiveEpochInvalidator interface {
	InvalidateEpoch() bool
}

type predictiveVLLMObserver struct {
	mu                      sync.Mutex
	pollMu                  sync.Mutex
	metricsURL              string
	modelIdentitySHA256     string
	maximumKVTokens         int64
	blockSize               int
	pollInterval            time.Duration
	maximumAge              time.Duration
	coordinator             predictiveSampleCoordinator
	now                     func() time.Time
	client                  *http.Client
	cancel                  context.CancelFunc
	done                    chan struct{}
	closeOnce               sync.Once
	closed                  bool
	lastSuccess             time.Time
	epochInvalidated        bool
	requestAwareInput       runtimepredictive.RequestAwareInput
	requestAwareObservedAt  time.Time
	requestAwareGeneration  uint64
	observationSequence     uint64
	requestAwareRunning     int
	requestAwarePreemptions uint64
	requestAwareHasBaseline bool
}

type predictiveObserverSnapshot struct {
	ObservedAt          time.Time
	MetricsFresh        bool
	IdentityValid       bool
	ObservationSequence uint64
	CapacityTokens      int64
	UsedTokens          int64
	Running             int
	Waiting             int
	AggregateTPS        float64
	MeanActiveTPS       float64
	TPSValid            bool
	PreemptionObserved  bool
}

type predictiveSampleDisposition uint8

const (
	predictiveSampleTransient predictiveSampleDisposition = iota
	predictiveSampleUsable
	predictiveSampleCapabilityDrift
)

func newPredictiveVLLMObserver(config predictiveVLLMObserverConfig) (*predictiveVLLMObserver, error) {
	parsed, err := url.Parse(config.MetricsURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("predictive vLLM metrics URL is invalid")
	}
	identity := strings.ToLower(strings.TrimSpace(config.ModelIdentitySHA256))
	if identity == "" && strings.TrimSpace(config.ServedModel) != "" {
		identity = predictiveModelIdentitySHA256(config.ServedModel)
	}
	if !validPredictiveModelIdentitySHA256(identity) || config.MaximumKVTokens <= 0 || config.BlockSize <= 0 ||
		config.PollInterval <= 0 || config.MaximumAge < config.PollInterval || config.RequestTimeout <= 0 ||
		config.Coordinator == nil {
		return nil, fmt.Errorf("predictive vLLM observer configuration is invalid")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	observer := &predictiveVLLMObserver{
		metricsURL:          config.MetricsURL,
		modelIdentitySHA256: identity,
		maximumKVTokens:     config.MaximumKVTokens,
		blockSize:           config.BlockSize,
		pollInterval:        config.PollInterval,
		maximumAge:          config.MaximumAge,
		coordinator:         config.Coordinator,
		now:                 now,
		client:              &http.Client{Timeout: config.RequestTimeout, Transport: transport},
		done:                make(chan struct{}),
	}
	if !config.Initial.ObservedAt.IsZero() {
		if config.Initial.ModelIdentitySHA256 != identity || config.Initial.CapacityTokens != config.MaximumKVTokens || config.Initial.BlockSize != config.BlockSize {
			return nil, fmt.Errorf("predictive initial observation does not match immutable capability")
		}
		observer.lastSuccess = config.Initial.ObservedAt
		observer.requestAwareInput = runtimepredictive.RequestAwareInput{
			MetricsFresh:   true,
			IdentityValid:  true,
			CapacityTokens: config.Initial.CapacityTokens,
			UsedTokens:     config.Initial.UsedTokens,
			Running:        config.Initial.Running,
			Waiting:        config.Initial.Waiting,
		}
		observer.requestAwareObservedAt = config.Initial.ObservedAt
		observer.requestAwareGeneration = config.Initial.Generation
		observer.requestAwareRunning = config.Initial.Running
		observer.requestAwarePreemptions = config.Initial.Preemptions
		observer.requestAwareHasBaseline = true
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer.cancel = cancel
	go observer.loop(ctx)
	return observer, nil
}

func (o *predictiveVLLMObserver) loop(ctx context.Context) {
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

func (o *predictiveVLLMObserver) poll(ctx context.Context) {
	if o == nil {
		return
	}
	o.pollMu.Lock()
	defer o.pollMu.Unlock()
	o.mu.Lock()
	invalidated := o.epochInvalidated
	o.mu.Unlock()
	if invalidated {
		return
	}
	started := o.coordinator.StartSampleWindow()
	sample, err := prometheus.FetchSampleContext(ctx, o.client, o.metricsURL)
	finished := o.coordinator.EventSequence()
	if err != nil {
		return
	}
	observedAt := o.now()
	switch o.sampleDisposition(sample, observedAt) {
	case predictiveSampleTransient:
		return
	case predictiveSampleCapabilityDrift:
		o.invalidateEpoch()
		return
	}

	o.mu.Lock()
	reset := o.requestAwareHasBaseline && (sample.Generation < o.requestAwareGeneration || sample.Preemptions < o.requestAwarePreemptions)
	o.mu.Unlock()
	if reset {
		o.invalidateEpoch()
		return
	}
	observed := domainpredictive.VirtualState{
		PhysicalKVUpper:         sample.KVUsedTokens,
		ActiveKVUpper:           sample.KVUsedTokens,
		DecodeSequences:         sample.Running + sample.Waiting,
		PendingPrefillSequences: sample.Waiting,
		ActiveContextTokens:     sample.KVUsedTokens,
	}
	o.mu.Lock()
	if o.observationSequence == ^uint64(0) {
		o.mu.Unlock()
		o.invalidateEpoch()
		return
	}
	observationSequence := o.observationSequence + 1
	o.mu.Unlock()
	if err := o.coordinator.ReconcileSample(runtimepredictive.SampleWindow{
		Observed: observed, StartedSequence: started, FinishedSequence: finished,
		ObservationSequence: observationSequence,
	}); err != nil {
		return
	}
	o.publish(sample, observedAt, observationSequence)
}

func (o *predictiveVLLMObserver) sampleDisposition(sample telemetry.Sample, observedAt time.Time) predictiveSampleDisposition {
	if sample.BackendKind != "" && sample.BackendKind != "vllm" {
		return predictiveSampleCapabilityDrift
	}
	if sample.ModelNameValid && predictiveModelIdentitySHA256(sample.ModelName) != o.modelIdentitySHA256 {
		return predictiveSampleCapabilityDrift
	}
	if sample.KVTokenMetricsValid && sample.KVCapacityTokens != o.maximumKVTokens {
		return predictiveSampleCapabilityDrift
	}
	if sample.KVBlockSizeValid && sample.KVBlockSize != o.blockSize {
		return predictiveSampleCapabilityDrift
	}
	maximumInt := int(^uint(0) >> 1)
	usable := sample.BackendKind == "vllm" && sample.ModelNameValid &&
		predictiveModelIdentitySHA256(sample.ModelName) == o.modelIdentitySHA256 &&
		sample.KVTokenMetricsValid && sample.KVCapacityTokens == o.maximumKVTokens &&
		sample.KVBlockSizeValid && sample.KVBlockSize == o.blockSize &&
		sample.KVUsedTokens >= 0 && sample.KVUsedTokens <= o.maximumKVTokens &&
		sample.RunningValid && sample.WaitingValid && sample.PreemptionsValid && sample.GenerationValid &&
		sample.Running >= 0 && sample.Waiting >= 0 && sample.Running <= maximumInt-sample.Waiting && !observedAt.IsZero()
	if !usable {
		return predictiveSampleTransient
	}
	return predictiveSampleUsable
}

func (o *predictiveVLLMObserver) publish(sample telemetry.Sample, observedAt time.Time, observationSequence uint64) {
	o.mu.Lock()
	defer o.mu.Unlock()
	input := runtimepredictive.RequestAwareInput{
		MetricsFresh:        true,
		IdentityValid:       !o.epochInvalidated,
		ObservationSequence: observationSequence,
		CapacityTokens:      o.maximumKVTokens,
		UsedTokens:          sample.KVUsedTokens,
		Running:             sample.Running,
		Waiting:             sample.Waiting,
		PreemptionObserved:  o.requestAwareHasBaseline && sample.Preemptions > o.requestAwarePreemptions,
	}
	if o.requestAwareHasBaseline && sample.Preemptions == o.requestAwarePreemptions && sample.Generation > o.requestAwareGeneration {
		elapsed := observedAt.Sub(o.requestAwareObservedAt)
		if elapsed > 0 && elapsed <= o.maximumAge {
			aggregate := float64(sample.Generation-o.requestAwareGeneration) / elapsed.Seconds()
			denominator := sample.Running
			if o.requestAwareRunning > denominator {
				denominator = o.requestAwareRunning
			}
			if denominator < 1 {
				denominator = 1
			}
			input.AggregateTPSProxy = aggregate
			input.MeanActiveTPSProxy = aggregate / float64(denominator)
			input.TPSValid = aggregate > 0
		}
	}
	if o.requestAwareHasBaseline && sample.Preemptions > o.requestAwarePreemptions {
		input.TPSValid = false
		input.AggregateTPSProxy = 0
		input.MeanActiveTPSProxy = 0
	}
	o.requestAwareInput = input
	o.requestAwareObservedAt = observedAt
	o.requestAwareGeneration = sample.Generation
	o.observationSequence = observationSequence
	o.requestAwareRunning = sample.Running
	o.requestAwarePreemptions = sample.Preemptions
	o.requestAwareHasBaseline = true
	o.lastSuccess = observedAt
}

func (o *predictiveVLLMObserver) invalidateEpoch() {
	if invalidator, ok := o.coordinator.(predictiveEpochInvalidator); ok {
		invalidator.InvalidateEpoch()
	}
	o.mu.Lock()
	o.epochInvalidated = true
	o.requestAwareInput.MetricsFresh = false
	o.requestAwareInput.IdentityValid = false
	o.requestAwareInput.TPSValid = false
	o.mu.Unlock()
}

func (o *predictiveVLLMObserver) RequestAwareInput(now time.Time) runtimepredictive.RequestAwareInput {
	if o == nil {
		return runtimepredictive.RequestAwareInput{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.requestAwareInputLocked(now)
}

func (o *predictiveVLLMObserver) requestAwareInputLocked(now time.Time) runtimepredictive.RequestAwareInput {
	input := o.requestAwareInput
	fresh := !o.epochInvalidated && !o.lastSuccess.IsZero() && !now.Before(o.lastSuccess) && now.Sub(o.lastSuccess) <= o.maximumAge
	input.MetricsFresh = fresh
	input.IdentityValid = fresh
	if !fresh {
		input.PreemptionObserved = false
		input.TPSValid = false
		input.AggregateTPSProxy = 0
		input.MeanActiveTPSProxy = 0
	}
	return input
}

func (o *predictiveVLLMObserver) Snapshot(now time.Time) predictiveObserverSnapshot {
	if o == nil {
		return predictiveObserverSnapshot{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	input := o.requestAwareInputLocked(now)
	observedAt := o.requestAwareObservedAt
	return predictiveObserverSnapshot{
		ObservedAt: observedAt, MetricsFresh: input.MetricsFresh, IdentityValid: input.IdentityValid,
		ObservationSequence: input.ObservationSequence,
		CapacityTokens:      input.CapacityTokens, UsedTokens: input.UsedTokens, Running: input.Running,
		Waiting: input.Waiting, AggregateTPS: input.AggregateTPSProxy, MeanActiveTPS: input.MeanActiveTPSProxy,
		TPSValid: input.TPSValid, PreemptionObserved: input.PreemptionObserved,
	}
}

func (o *predictiveVLLMObserver) Close() error {
	if o == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		o.mu.Lock()
		o.closed = true
		o.mu.Unlock()
		o.cancel()
		<-o.done
	})
	return nil
}
