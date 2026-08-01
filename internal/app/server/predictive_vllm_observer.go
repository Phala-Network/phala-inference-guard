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
)

type predictiveVLLMObserverConfig struct {
	MetricsURL          string
	ModelIdentitySHA256 string
	// ServedModel is accepted only for compatibility with tests and is hashed
	// during construction. The observer never retains the raw model name.
	ServedModel          string
	MaximumKVTokens      int64
	BlockSize            int
	PollInterval         time.Duration
	MaximumAge           time.Duration
	RequestTimeout       time.Duration
	PreemptionCooldown   time.Duration
	Coordinator          predictiveSampleCoordinator
	LearningInvalidators []predictiveLearningInvalidator
	Initial              predictiveVLLMStartup
	Now                  func() time.Time
}

type predictiveSampleCoordinator interface {
	StartSampleWindow() uint64
	EventSequence() uint64
	ReconcileSample(runtimepredictive.SampleWindow) error
}

type predictiveLearningInvalidator interface {
	InvalidateLearning()
}

type predictiveEpochInvalidator interface {
	InvalidateEpoch() bool
}

type predictiveVLLMObserver struct {
	mu                   sync.Mutex
	metricsURL           string
	modelIdentitySHA256  string
	maximumKVTokens      int64
	blockSize            int
	pollInterval         time.Duration
	maximumAge           time.Duration
	preemptionCooldown   time.Duration
	coordinator          predictiveSampleCoordinator
	learningInvalidators []predictiveLearningInvalidator
	now                  func() time.Time
	client               *http.Client
	cancel               context.CancelFunc
	done                 chan struct{}
	closeOnce            sync.Once
	closed               bool
	lastSuccess          time.Time
	lastPreemption       time.Time
	lastWaiting          int
	epochInvalidated     bool
	preemptions          uint64
	hasPreemptions       bool
	generation           uint64
	hasGeneration        bool
}

func newPredictiveVLLMObserver(config predictiveVLLMObserverConfig) (*predictiveVLLMObserver, error) {
	parsed, err := url.Parse(config.MetricsURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("predictive vLLM metrics URL is invalid")
	}
	modelIdentitySHA256 := strings.ToLower(strings.TrimSpace(config.ModelIdentitySHA256))
	if modelIdentitySHA256 == "" && strings.TrimSpace(config.ServedModel) != "" {
		modelIdentitySHA256 = predictiveModelIdentitySHA256(config.ServedModel)
	}
	if !validPredictiveModelIdentitySHA256(modelIdentitySHA256) || config.MaximumKVTokens <= 0 || config.BlockSize <= 0 || config.PollInterval <= 0 || config.MaximumAge < config.PollInterval || config.RequestTimeout <= 0 || config.PreemptionCooldown < 0 || config.Coordinator == nil {
		return nil, fmt.Errorf("predictive vLLM observer configuration is invalid")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer := &predictiveVLLMObserver{
		metricsURL:           config.MetricsURL,
		modelIdentitySHA256:  modelIdentitySHA256,
		maximumKVTokens:      config.MaximumKVTokens,
		blockSize:            config.BlockSize,
		pollInterval:         config.PollInterval,
		maximumAge:           config.MaximumAge,
		preemptionCooldown:   config.PreemptionCooldown,
		coordinator:          config.Coordinator,
		learningInvalidators: append([]predictiveLearningInvalidator(nil), config.LearningInvalidators...),
		now:                  config.Now,
		client:               &http.Client{Timeout: config.RequestTimeout},
		cancel:               cancel,
		done:                 make(chan struct{}),
	}
	if !config.Initial.ObservedAt.IsZero() {
		if config.Initial.ModelIdentitySHA256 != modelIdentitySHA256 || config.Initial.CapacityTokens != config.MaximumKVTokens || config.Initial.BlockSize != config.BlockSize {
			cancel()
			return nil, fmt.Errorf("predictive vLLM initial observation identity mismatch")
		}
		observer.lastSuccess = config.Initial.ObservedAt
		observer.lastWaiting = config.Initial.Waiting
		observer.preemptions = config.Initial.Preemptions
		observer.hasPreemptions = true
		observer.generation = config.Initial.Generation
		observer.hasGeneration = true
	}
	go observer.run(ctx)
	return observer, nil
}

func (o *predictiveVLLMObserver) Healthy(now time.Time) bool {
	if o == nil {
		return false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.epochInvalidated || o.lastSuccess.IsZero() || o.lastWaiting > 0 {
		return false
	}
	age := now.Sub(o.lastSuccess)
	if age < 0 || age > o.maximumAge {
		return false
	}
	if !o.lastPreemption.IsZero() {
		preemptionAge := now.Sub(o.lastPreemption)
		if preemptionAge < 0 || preemptionAge < o.preemptionCooldown {
			return false
		}
	}
	return true
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

func (o *predictiveVLLMObserver) run(ctx context.Context) {
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
	started := o.coordinator.StartSampleWindow()
	sample, err := prometheus.FetchSampleContext(ctx, o.client, o.metricsURL)
	finished := o.coordinator.EventSequence()
	now := o.now()
	maximumInt := int(^uint(0) >> 1)
	if err != nil {
		return
	}
	if sample.BackendKind != "vllm" || !sample.ModelNameValid || !sample.KVTokenMetricsValid || !sample.KVBlockSizeValid {
		return
	}
	if predictiveModelIdentitySHA256(sample.ModelName) != o.modelIdentitySHA256 || sample.KVCapacityTokens != o.maximumKVTokens || sample.KVBlockSize != o.blockSize {
		o.rejectObservedIdentity()
		return
	}
	if sample.KVUsedTokens < 0 || sample.KVUsedTokens > o.maximumKVTokens || !sample.RunningValid || !sample.WaitingValid || !sample.PreemptionsValid || !sample.GenerationValid || sample.Running < 0 || sample.Waiting < 0 || sample.Running > maximumInt-sample.Waiting {
		return
	}
	o.mu.Lock()
	if o.epochInvalidated {
		o.mu.Unlock()
		return
	}
	epochReset := (o.hasPreemptions && sample.Preemptions < o.preemptions) ||
		(o.hasGeneration && sample.Generation < o.generation)
	if epochReset {
		o.epochInvalidated = true
		o.lastSuccess = time.Time{}
		o.lastPreemption = time.Time{}
		o.lastWaiting = 0
		o.mu.Unlock()
		o.invalidateEpoch()
		return
	}
	preempted := o.hasPreemptions && sample.Preemptions > o.preemptions
	o.preemptions = sample.Preemptions
	o.hasPreemptions = true
	o.generation = sample.Generation
	o.hasGeneration = true
	o.lastWaiting = sample.Waiting
	if preempted {
		o.lastSuccess = time.Time{}
		o.lastPreemption = now
	}
	o.mu.Unlock()
	if preempted {
		o.invalidateLearning()
	}
	err = o.coordinator.ReconcileSample(runtimepredictive.SampleWindow{
		Observed: domainpredictive.VirtualState{
			PhysicalKVUpper:       sample.KVUsedTokens,
			ActiveKVUpper:         sample.KVUsedTokens,
			DecodeSequences:       sample.Running + sample.Waiting,
			ActiveContextTokens:   sample.KVUsedTokens,
			UncachedPrefillTokens: 0,
		},
		StartedSequence:  started,
		FinishedSequence: finished,
	})
	if err != nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.lastSuccess = now
	o.lastWaiting = sample.Waiting
}

func (o *predictiveVLLMObserver) rejectObservedIdentity() {
	o.mu.Lock()
	alreadyInvalidated := o.epochInvalidated
	o.epochInvalidated = true
	o.hasGeneration = false
	o.generation = 0
	o.hasPreemptions = false
	o.preemptions = 0
	o.lastSuccess = time.Time{}
	o.lastPreemption = time.Time{}
	o.lastWaiting = 0
	o.mu.Unlock()
	if !alreadyInvalidated {
		o.invalidateEpoch()
	}
}

func (o *predictiveVLLMObserver) invalidateLearning() {
	if invalidator, ok := o.coordinator.(predictiveLearningInvalidator); ok {
		invalidator.InvalidateLearning()
	}
	for _, invalidator := range o.learningInvalidators {
		if invalidator != nil {
			invalidator.InvalidateLearning()
		}
	}
}

func (o *predictiveVLLMObserver) invalidateEpoch() {
	if invalidator, ok := o.coordinator.(predictiveEpochInvalidator); ok {
		invalidator.InvalidateEpoch()
	} else {
		o.invalidateLearning()
	}
	for _, invalidator := range o.learningInvalidators {
		if invalidator != nil {
			invalidator.InvalidateLearning()
		}
	}
}

func predictiveVLLMMetricsURL(cfg config) (string, error) {
	if len(cfg.Backends) != 1 {
		return "", fmt.Errorf("predictive admission requires exactly one configured upstream")
	}
	metricsURL := strings.TrimSpace(cfg.Backends[0].MetricsURL)
	if metricsURL == "" && len(cfg.DynamicMetricsURLs) == 1 {
		metricsURL = strings.TrimSpace(cfg.DynamicMetricsURLs[0])
	}
	if metricsURL == "" {
		return "", fmt.Errorf("predictive admission requires one vLLM metrics URL")
	}
	return metricsURL, nil
}
