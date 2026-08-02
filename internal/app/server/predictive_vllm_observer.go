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
	ServedModel            string
	MaximumKVTokens        int64
	BlockSize              int
	PollInterval           time.Duration
	MaximumAge             time.Duration
	RequestTimeout         time.Duration
	PreemptionCooldown     time.Duration
	Coordinator            predictiveSampleCoordinator
	ExistingPrefillLearner predictiveExistingPrefillLearner
	ShadowPendingPrefills  predictiveShadowPendingPrefillSnapshotter
	LearningInvalidators   []predictiveLearningInvalidator
	Initial                predictiveVLLMStartup
	Now                    func() time.Time
}

type predictiveSampleCoordinator interface {
	StartSampleWindow() uint64
	EventSequence() uint64
	ReconcileSample(runtimepredictive.SampleWindow) error
}

type predictiveLearningInvalidator interface {
	InvalidateLearning()
}

type predictiveExistingPrefillLearner interface {
	Identity() runtimepredictive.ModelIdentity
	ObserveExistingPrefill(runtimepredictive.ExistingPrefillOutcome) error
}

type predictiveExistingPrefillTelemetryProvider interface {
	ExistingPrefillTelemetry() predictiveExistingPrefillObservationSnapshot
}

type predictiveShadowPendingPrefillSnapshotter interface {
	Snapshot() predictiveShadowPendingPrefillSnapshot
}

type predictiveEpochInvalidator interface {
	InvalidateEpoch() bool
}

type predictiveVLLMObserver struct {
	mu                     sync.Mutex
	metricsURL             string
	modelIdentitySHA256    string
	maximumKVTokens        int64
	blockSize              int
	pollInterval           time.Duration
	maximumAge             time.Duration
	preemptionCooldown     time.Duration
	coordinator            predictiveSampleCoordinator
	coordinatorSnapshot    predictiveCoordinatorSnapshotter
	existingPrefillLearner predictiveExistingPrefillLearner
	shadowPendingPrefills  predictiveShadowPendingPrefillSnapshotter
	learningInvalidators   []predictiveLearningInvalidator
	now                    func() time.Time
	client                 *http.Client
	cancel                 context.CancelFunc
	done                   chan struct{}
	closeOnce              sync.Once
	closed                 bool
	lastSuccess            time.Time
	lastPreemption         time.Time
	lastWaiting            int
	epochInvalidated       bool
	preemptions            uint64
	hasPreemptions         bool
	generation             uint64
	hasGeneration          bool
	prefillWindow          *predictiveStablePrefillWindow
	prefillStats           predictiveExistingPrefillObservationSnapshot
}

type predictiveStablePrefillWindow struct {
	ObservedAt    time.Time
	Generation    uint64
	Preemptions   uint64
	Running       int
	Waiting       int
	EventSequence uint64
	Manager       runtimepredictive.Snapshot
	ShadowPending predictiveShadowPendingPrefillSnapshot
}

type predictiveObservedPendingPrefill struct {
	Count                   int
	Tokens                  int64
	Features                runtimepredictive.SchedulerFeatures
	FeaturesValid           bool
	FromShadow              bool
	DecisionManagerSequence uint64
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
	var coordinatorSnapshot predictiveCoordinatorSnapshotter
	if config.ExistingPrefillLearner != nil {
		var ok bool
		coordinatorSnapshot, ok = config.Coordinator.(predictiveCoordinatorSnapshotter)
		if !ok || config.ExistingPrefillLearner.Identity().Validate() != nil {
			return nil, fmt.Errorf("predictive existing-prefill learner configuration is invalid")
		}
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer := &predictiveVLLMObserver{
		metricsURL:             config.MetricsURL,
		modelIdentitySHA256:    modelIdentitySHA256,
		maximumKVTokens:        config.MaximumKVTokens,
		blockSize:              config.BlockSize,
		pollInterval:           config.PollInterval,
		maximumAge:             config.MaximumAge,
		preemptionCooldown:     config.PreemptionCooldown,
		coordinator:            config.Coordinator,
		coordinatorSnapshot:    coordinatorSnapshot,
		existingPrefillLearner: config.ExistingPrefillLearner,
		shadowPendingPrefills:  config.ShadowPendingPrefills,
		learningInvalidators:   append([]predictiveLearningInvalidator(nil), config.LearningInvalidators...),
		now:                    config.Now,
		client:                 &http.Client{Timeout: config.RequestTimeout},
		cancel:                 cancel,
		done:                   make(chan struct{}),
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

func (o *predictiveVLLMObserver) ExistingPrefillTelemetry() predictiveExistingPrefillObservationSnapshot {
	if o == nil {
		return predictiveExistingPrefillObservationSnapshot{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.prefillStats
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
	shadowStarted := snapshotPredictiveShadowPendingPrefills(o.shadowPendingPrefills)
	sample, err := prometheus.FetchSampleContext(ctx, o.client, o.metricsURL)
	finished := o.coordinator.EventSequence()
	shadowFinished := snapshotPredictiveShadowPendingPrefills(o.shadowPendingPrefills)
	now := o.now()
	maximumInt := int(^uint(0) >> 1)
	if err != nil {
		o.censorStablePrefillWindow()
		return
	}
	if sample.BackendKind != "vllm" || !sample.ModelNameValid || !sample.KVTokenMetricsValid || !sample.KVBlockSizeValid {
		o.censorStablePrefillWindow()
		return
	}
	if predictiveModelIdentitySHA256(sample.ModelName) != o.modelIdentitySHA256 || sample.KVCapacityTokens != o.maximumKVTokens || sample.KVBlockSize != o.blockSize {
		o.rejectObservedIdentity()
		return
	}
	if sample.KVUsedTokens < 0 || sample.KVUsedTokens > o.maximumKVTokens || !sample.RunningValid || !sample.WaitingValid || !sample.PreemptionsValid || !sample.GenerationValid || sample.Running < 0 || sample.Waiting < 0 || sample.Running > maximumInt-sample.Waiting {
		o.censorStablePrefillWindow()
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
		o.censorStablePrefillWindowLocked()
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
		o.censorStablePrefillWindowLocked()
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
		o.censorStablePrefillWindow()
		return
	}
	manager := runtimepredictive.Snapshot{}
	if o.coordinatorSnapshot != nil {
		manager = o.coordinatorSnapshot.Snapshot().Manager
	}
	current := predictiveStablePrefillWindow{
		ObservedAt:    now,
		Generation:    sample.Generation,
		Preemptions:   sample.Preemptions,
		Running:       sample.Running,
		Waiting:       sample.Waiting,
		EventSequence: finished,
		Manager:       manager,
		ShadowPending: snapshotPredictiveShadowPendingPrefills(o.shadowPendingPrefills),
	}
	o.mu.Lock()
	if o.closed {
		o.censorStablePrefillWindowLocked()
		o.mu.Unlock()
		return
	}
	outcome, candidate := o.qualifyStablePrefillOutcomeLocked(current, started, finished, shadowStarted.EventSequence, shadowFinished.EventSequence)
	if sample.Waiting == 0 {
		o.prefillWindow = &current
	} else {
		o.prefillWindow = nil
	}
	o.lastSuccess = now
	o.lastWaiting = sample.Waiting
	o.mu.Unlock()
	if candidate {
		o.observeStablePrefillOutcome(outcome)
	}
}

func (o *predictiveVLLMObserver) qualifyStablePrefillOutcomeLocked(current predictiveStablePrefillWindow, started, finished, shadowStarted, shadowFinished uint64) (runtimepredictive.ExistingPrefillOutcome, bool) {
	previous := o.prefillWindow
	if previous == nil {
		return runtimepredictive.ExistingPrefillOutcome{}, false
	}
	previousPending := observedPredictivePendingPrefills(previous.Manager, previous.ShadowPending)
	if previousPending.Count <= 0 {
		return runtimepredictive.ExistingPrefillOutcome{}, false
	}
	currentPending := observedPredictivePendingPrefills(current.Manager, current.ShadowPending)
	pending := previousPending.Count
	pendingTokens := previousPending.Tokens
	features := previousPending.Features
	elapsed := current.ObservedAt.Sub(previous.ObservedAt)
	qualified := o.existingPrefillLearner != nil && o.coordinatorSnapshot != nil &&
		elapsed > 0 && elapsed <= o.maximumAge && previous.EventSequence == started && started == finished && current.Manager.EventSequence == finished &&
		previous.ShadowPending.EventSequence == shadowStarted && shadowStarted == shadowFinished && current.ShadowPending.EventSequence == shadowFinished &&
		previous.Preemptions == current.Preemptions && previous.Generation <= current.Generation &&
		previous.Waiting == 0 && current.Waiting == 0 && previous.Running == current.Running &&
		pending == 1 && pendingTokens > 0 && previousPending.FeaturesValid &&
		(!previousPending.FromShadow || previousPending.DecisionManagerSequence == previous.Manager.EventSequence) &&
		currentPending.Count == pending && currentPending.Tokens == pendingTokens &&
		currentPending.FeaturesValid && currentPending.Features == features &&
		features.ExistingPendingPrefillSequences == 0 && features.PendingPrefillSequences == 1 &&
		features.ExistingUncachedPrefill == 0 && features.UncachedPrefillTokens == pendingTokens &&
		previous.Running == features.DecodeSequences
	existingDecoders := features.ExistingDecodeSequences
	if !qualified || existingDecoders <= 0 {
		o.prefillStats.Censored++
		return runtimepredictive.ExistingPrefillOutcome{}, false
	}
	generationTPS := float64(current.Generation-previous.Generation) / elapsed.Seconds()
	perUserTPS := generationTPS / float64(existingDecoders)
	return runtimepredictive.ExistingPrefillOutcome{
		Identity:                o.existingPrefillLearner.Identity(),
		StartedAt:               previous.ObservedAt,
		ObservedAt:              current.ObservedAt,
		Features:                features,
		ExistingDecodeSequences: existingDecoders,
		PendingPrefillSequences: pending,
		PendingPrefillTokens:    pendingTokens,
		ExistingUserTPS:         perUserTPS,
	}, true
}

func (o *predictiveVLLMObserver) observeStablePrefillOutcome(outcome runtimepredictive.ExistingPrefillOutcome) {
	err := o.existingPrefillLearner.ObserveExistingPrefill(outcome)
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		o.prefillStats.Rejected++
		return
	}
	o.prefillStats.Accepted++
	o.prefillStats.LastExistingUserTPS = outcome.ExistingUserTPS
	o.prefillStats.LastExistingUserTPSValid = true
}

func (o *predictiveVLLMObserver) censorStablePrefillWindow() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.censorStablePrefillWindowLocked()
	o.mu.Unlock()
}

func (o *predictiveVLLMObserver) censorStablePrefillWindowLocked() {
	if o.prefillWindow != nil && observedPredictivePendingPrefills(o.prefillWindow.Manager, o.prefillWindow.ShadowPending).Count > 0 {
		o.prefillStats.Censored++
	}
	o.prefillWindow = nil
}

func snapshotPredictiveShadowPendingPrefills(provider predictiveShadowPendingPrefillSnapshotter) predictiveShadowPendingPrefillSnapshot {
	if provider == nil {
		return predictiveShadowPendingPrefillSnapshot{}
	}
	return provider.Snapshot()
}

func observedPredictivePendingPrefills(manager runtimepredictive.Snapshot, shadow predictiveShadowPendingPrefillSnapshot) predictiveObservedPendingPrefill {
	count := manager.ForwardedPendingPrefills
	if shadow.Count > 0 {
		maximumInt := int(^uint(0) >> 1)
		if count > maximumInt-shadow.Count {
			count = maximumInt
		} else {
			count += shadow.Count
		}
	}
	result := predictiveObservedPendingPrefill{
		Count:  count,
		Tokens: addPredictivePendingPrefillTokens(manager.ForwardedPendingPrefillTokens, shadow.Tokens),
	}
	switch {
	case manager.ForwardedPendingPrefills == 1 && shadow.Count == 0 && manager.ForwardedPendingPrefillFeaturesValid:
		result.Features = manager.ForwardedPendingPrefillFeatures
		result.FeaturesValid = true
	case manager.ForwardedPendingPrefills == 0 && shadow.Count == 1 && shadow.FeaturesValid:
		result.Features = shadow.Features
		result.FeaturesValid = true
		result.FromShadow = true
		result.DecisionManagerSequence = shadow.DecisionManagerSequence
	}
	return result
}

func (o *predictiveVLLMObserver) rejectObservedIdentity() {
	o.mu.Lock()
	o.censorStablePrefillWindowLocked()
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
