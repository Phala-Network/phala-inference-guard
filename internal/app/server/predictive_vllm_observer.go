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
	ServedModel                string
	MaximumKVTokens            int64
	BlockSize                  int
	PollInterval               time.Duration
	MaximumAge                 time.Duration
	RequestTimeout             time.Duration
	PreemptionCooldown         time.Duration
	Coordinator                predictiveSampleCoordinator
	ExistingPrefillLearner     predictiveExistingPrefillLearner
	AggregateThroughputLearner predictiveAggregateThroughputLearner
	LoadPressureObserver       predictiveLoadPressureObserver
	ShadowPendingPrefills      predictiveShadowPendingPrefillSnapshotter
	LearningInvalidators       []predictiveLearningInvalidator
	Initial                    predictiveVLLMStartup
	Now                        func() time.Time
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

type predictiveAggregateThroughputLearner interface {
	Identity() runtimepredictive.ModelIdentity
	ObserveAggregateThroughput(runtimepredictive.AggregateThroughputOutcome) error
}

type predictiveLearnerIdentityProvider interface {
	Identity() runtimepredictive.ModelIdentity
}

type predictiveLoadPressureObserver interface {
	ObserveLoadPressure(time.Time, runtimepredictive.LoadPressureKind) bool
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

type predictiveEpochRebaser interface {
	RebaseEpoch(domainpredictive.VirtualState) error
}

type predictiveVLLMObserver struct {
	mu                          sync.Mutex
	metricsURL                  string
	modelIdentitySHA256         string
	maximumKVTokens             int64
	blockSize                   int
	pollInterval                time.Duration
	maximumAge                  time.Duration
	preemptionCooldown          time.Duration
	coordinator                 predictiveSampleCoordinator
	coordinatorSnapshot         predictiveCoordinatorSnapshotter
	existingPrefillLearner      predictiveExistingPrefillLearner
	existingPrefillIdentity     runtimepredictive.ModelIdentity
	aggregateThroughputLearner  predictiveAggregateThroughputLearner
	aggregateThroughputIdentity runtimepredictive.ModelIdentity
	loadPressureObserver        predictiveLoadPressureObserver
	shadowPendingPrefills       predictiveShadowPendingPrefillSnapshotter
	learningInvalidators        []predictiveLearningInvalidator
	now                         func() time.Time
	client                      *http.Client
	cancel                      context.CancelFunc
	done                        chan struct{}
	closeOnce                   sync.Once
	closed                      bool
	lastSuccess                 time.Time
	lastPreemption              time.Time
	lastWaiting                 int
	epochInvalidated            bool
	preemptions                 uint64
	hasPreemptions              bool
	generation                  uint64
	hasGeneration               bool
	requestAwareInput           runtimepredictive.RequestAwareInput
	requestAwareObservedAt      time.Time
	requestAwareGeneration      uint64
	requestAwareRunning         int
	requestAwarePreemptions     uint64
	requestAwareHasBaseline     bool
	prefillWindow               *predictiveStablePrefillWindow
	prefillStats                predictiveExistingPrefillObservationSnapshot
	prefillEpisodes             predictivePrefillEpisodeTracker
	completionQoSWindow         *predictiveStablePrefillWindow
	completionQoS               predictiveCompletionQoSEvidence
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
	Exploratory             bool
	Episode                 predictivePrefillEpisodeIdentity
}

type predictivePrefillEpisodeOrigin uint8

const (
	predictivePrefillEpisodeUnknown predictivePrefillEpisodeOrigin = iota
	predictivePrefillEpisodeManager
	predictivePrefillEpisodeShadow
)

type predictivePrefillEpisodeIdentity struct {
	Origin   predictivePrefillEpisodeOrigin
	Sequence uint64
}

func (i predictivePrefillEpisodeIdentity) valid() bool {
	return i.Origin != predictivePrefillEpisodeUnknown && i.Sequence > 0
}

// predictivePrefillEpisodeTracker is the fixed-size one-shot attribution
// component. It retains no request identity or payload and cannot grow with
// traffic cardinality.
type predictivePrefillEpisodeTracker struct {
	last    predictivePrefillEpisodeIdentity
	hasLast bool
}

func (t *predictivePrefillEpisodeTracker) MarkEmitted(identity predictivePrefillEpisodeIdentity) bool {
	if t == nil || !identity.valid() {
		return false
	}
	if t.hasLast && t.last == identity {
		return false
	}
	t.last = identity
	t.hasLast = true
	return true
}

func validatedPredictiveLearnerIdentity(provider predictiveLearnerIdentityProvider) (identity runtimepredictive.ModelIdentity, valid bool) {
	if provider == nil {
		return runtimepredictive.ModelIdentity{}, false
	}
	defer func() {
		if recover() != nil {
			identity = runtimepredictive.ModelIdentity{}
			valid = false
		}
	}()
	identity = provider.Identity()
	return identity, identity.Validate() == nil
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
	if config.ExistingPrefillLearner != nil || config.AggregateThroughputLearner != nil {
		var ok bool
		coordinatorSnapshot, ok = config.Coordinator.(predictiveCoordinatorSnapshotter)
		if !ok {
			return nil, fmt.Errorf("predictive stable-window learner configuration is invalid")
		}
	}
	var existingPrefillIdentity runtimepredictive.ModelIdentity
	if config.ExistingPrefillLearner != nil {
		var valid bool
		existingPrefillIdentity, valid = validatedPredictiveLearnerIdentity(config.ExistingPrefillLearner)
		if !valid {
			return nil, fmt.Errorf("predictive existing-prefill learner configuration is invalid")
		}
	}
	var aggregateThroughputIdentity runtimepredictive.ModelIdentity
	if config.AggregateThroughputLearner != nil {
		var valid bool
		aggregateThroughputIdentity, valid = validatedPredictiveLearnerIdentity(config.AggregateThroughputLearner)
		if !valid {
			return nil, fmt.Errorf("predictive aggregate-throughput learner configuration is invalid")
		}
	}
	if config.ExistingPrefillLearner != nil && config.AggregateThroughputLearner != nil &&
		existingPrefillIdentity != aggregateThroughputIdentity {
		return nil, fmt.Errorf("predictive stable-window learner identities differ")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	ctx, cancel := context.WithCancel(context.Background())
	observer := &predictiveVLLMObserver{
		metricsURL:                  config.MetricsURL,
		modelIdentitySHA256:         modelIdentitySHA256,
		maximumKVTokens:             config.MaximumKVTokens,
		blockSize:                   config.BlockSize,
		pollInterval:                config.PollInterval,
		maximumAge:                  config.MaximumAge,
		preemptionCooldown:          config.PreemptionCooldown,
		coordinator:                 config.Coordinator,
		coordinatorSnapshot:         coordinatorSnapshot,
		existingPrefillLearner:      config.ExistingPrefillLearner,
		existingPrefillIdentity:     existingPrefillIdentity,
		aggregateThroughputLearner:  config.AggregateThroughputLearner,
		aggregateThroughputIdentity: aggregateThroughputIdentity,
		loadPressureObserver:        config.LoadPressureObserver,
		shadowPendingPrefills:       config.ShadowPendingPrefills,
		learningInvalidators:        append([]predictiveLearningInvalidator(nil), config.LearningInvalidators...),
		now:                         config.Now,
		client:                      &http.Client{Timeout: config.RequestTimeout},
		cancel:                      cancel,
		done:                        make(chan struct{}),
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

func (o *predictiveVLLMObserver) RequestAwareInput(now time.Time) runtimepredictive.RequestAwareInput {
	if o == nil {
		return runtimepredictive.RequestAwareInput{}
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	input := o.requestAwareInput
	input.MetricsFresh = false
	input.PreemptionCooldown = false
	if o.closed || o.epochInvalidated || now.IsZero() || o.requestAwareObservedAt.IsZero() || !input.IdentityValid {
		input.TPSValid = false
		input.AggregateTPSProxy = 0
		input.MeanActiveTPSProxy = 0
		return input
	}
	age := now.Sub(o.requestAwareObservedAt)
	if age < 0 || age > o.maximumAge {
		input.TPSValid = false
		input.AggregateTPSProxy = 0
		input.MeanActiveTPSProxy = 0
		return input
	}
	input.MetricsFresh = true
	if !o.lastPreemption.IsZero() {
		preemptionAge := now.Sub(o.lastPreemption)
		if preemptionAge < 0 || preemptionAge < o.preemptionCooldown {
			input.PreemptionCooldown = true
			input.TPSValid = false
			input.AggregateTPSProxy = 0
			input.MeanActiveTPSProxy = 0
		}
	}
	return input
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

func (o *predictiveVLLMObserver) CompletionQoSEvidence(now, requestStarted, requestObservedAt time.Time) (predictiveCompletionQoSEvidence, bool) {
	if o == nil || now.IsZero() || requestStarted.IsZero() || requestObservedAt.IsZero() || !requestObservedAt.After(requestStarted) {
		return predictiveCompletionQoSEvidence{}, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || o.epochInvalidated || o.lastWaiting > 0 || o.completionQoS.ObservedAt.IsZero() {
		return predictiveCompletionQoSEvidence{}, false
	}
	age := now.Sub(o.completionQoS.ObservedAt)
	if age < 0 || age > o.maximumAge {
		return predictiveCompletionQoSEvidence{}, false
	}
	if !o.lastPreemption.IsZero() {
		preemptionAge := now.Sub(o.lastPreemption)
		if preemptionAge < 0 || preemptionAge < o.preemptionCooldown {
			return predictiveCompletionQoSEvidence{}, false
		}
	}
	// Require a real overlap with the request. A fresh window that completed
	// entirely before this request cannot qualify its downstream wall clock.
	if !o.completionQoS.ObservedAt.After(requestStarted) || !requestObservedAt.After(o.completionQoS.StartedAt) {
		return predictiveCompletionQoSEvidence{}, false
	}
	return o.completionQoS, true
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
		observed := domainpredictive.VirtualState{
			PhysicalKVUpper:       sample.KVUsedTokens,
			ActiveKVUpper:         sample.KVUsedTokens,
			DecodeSequences:       sample.Running + sample.Waiting,
			ActiveContextTokens:   sample.KVUsedTokens,
			UncachedPrefillTokens: 0,
		}
		o.censorStablePrefillWindowLocked()
		o.resetRequestAwareInputLocked()
		o.epochInvalidated = true
		o.lastSuccess = time.Time{}
		o.lastPreemption = time.Time{}
		o.lastWaiting = 0
		o.mu.Unlock()
		rebaser, canRebase := o.coordinator.(predictiveEpochRebaser)
		if !canRebase || rebaser.RebaseEpoch(observed) != nil {
			o.invalidateEpoch()
			return
		}
		o.invalidateLearning()
		o.mu.Lock()
		if o.closed {
			o.mu.Unlock()
			o.invalidateEpoch()
			return
		}
		o.epochInvalidated = false
		o.preemptions = sample.Preemptions
		o.hasPreemptions = true
		o.generation = sample.Generation
		o.hasGeneration = true
		o.lastSuccess = now
		o.lastPreemption = time.Time{}
		o.lastWaiting = sample.Waiting
		o.requestAwareInput = runtimepredictive.RequestAwareInput{
			MetricsFresh:   true,
			IdentityValid:  true,
			CapacityTokens: o.maximumKVTokens,
			UsedTokens:     sample.KVUsedTokens,
			Running:        sample.Running,
			Waiting:        sample.Waiting,
		}
		o.requestAwareObservedAt = now
		o.requestAwareGeneration = sample.Generation
		o.requestAwareRunning = sample.Running
		o.requestAwarePreemptions = sample.Preemptions
		o.requestAwareHasBaseline = true
		o.mu.Unlock()
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
		o.resetRequestAwareInputLocked()
		o.lastSuccess = time.Time{}
		o.lastPreemption = now
	}
	o.mu.Unlock()
	if preempted {
		o.invalidateLearning()
		observePredictiveLoadPressure(o.loadPressureObserver, now, runtimepredictive.LoadPressurePreemption)
	}
	if sample.Waiting > 0 {
		observePredictiveLoadPressure(o.loadPressureObserver, now, runtimepredictive.LoadPressureWaiting)
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
	o.publishRequestAwareInputLocked(current, sample.KVUsedTokens)
	prefillOutcome, prefillCandidate := o.qualifyStablePrefillOutcomeLocked(current, started, finished, shadowStarted.EventSequence, shadowFinished.EventSequence)
	aggregateOutcome, aggregateCandidate := o.qualifyStableAggregateThroughputOutcomeLocked(current, started, finished, shadowStarted.EventSequence, shadowFinished.EventSequence)
	completionQoS, completionQoSValid := o.qualifyCompletionQoSEvidenceLocked(current)
	if sample.Waiting == 0 {
		o.prefillWindow = &current
	} else {
		o.prefillWindow = nil
	}
	if preempted || sample.Waiting > 0 {
		o.completionQoSWindow = nil
		o.completionQoS = predictiveCompletionQoSEvidence{}
	} else {
		o.completionQoSWindow = &current
		if completionQoSValid {
			o.completionQoS = completionQoS
		} else {
			// Evidence is valid only for the immediately preceding stable
			// interval. Retaining an older still-fresh value here would let a
			// running-set transition or a no-progress interval qualify a later
			// request that merely overlaps that older window.
			o.completionQoS = predictiveCompletionQoSEvidence{}
		}
	}
	o.lastSuccess = now
	o.lastWaiting = sample.Waiting
	o.mu.Unlock()
	if prefillCandidate {
		o.observeStablePrefillOutcome(prefillOutcome)
	}
	if aggregateCandidate {
		o.observeStableAggregateThroughputOutcome(aggregateOutcome)
	}
}

func (o *predictiveVLLMObserver) publishRequestAwareInputLocked(current predictiveStablePrefillWindow, usedTokens int64) {
	input := runtimepredictive.RequestAwareInput{
		MetricsFresh:   true,
		IdentityValid:  true,
		CapacityTokens: o.maximumKVTokens,
		UsedTokens:     usedTokens,
		Running:        current.Running,
		Waiting:        current.Waiting,
	}
	if o.requestAwareHasBaseline && current.Preemptions == o.requestAwarePreemptions &&
		current.Generation > o.requestAwareGeneration {
		elapsed := current.ObservedAt.Sub(o.requestAwareObservedAt)
		runningDenominator := o.requestAwareRunning
		if current.Running > runningDenominator {
			runningDenominator = current.Running
		}
		if elapsed > 0 && elapsed <= o.maximumAge && runningDenominator > 0 {
			aggregateTPS := float64(current.Generation-o.requestAwareGeneration) / elapsed.Seconds()
			input.AggregateTPSProxy = aggregateTPS
			input.MeanActiveTPSProxy = aggregateTPS / float64(runningDenominator)
			input.TPSValid = input.MeanActiveTPSProxy > 0
		}
	}
	o.requestAwareInput = input
	o.requestAwareObservedAt = current.ObservedAt
	o.requestAwareGeneration = current.Generation
	o.requestAwareRunning = current.Running
	o.requestAwarePreemptions = current.Preemptions
	o.requestAwareHasBaseline = true
}

func (o *predictiveVLLMObserver) resetRequestAwareInputLocked() {
	o.requestAwareInput = runtimepredictive.RequestAwareInput{}
	o.requestAwareObservedAt = time.Time{}
	o.requestAwareGeneration = 0
	o.requestAwareRunning = 0
	o.requestAwarePreemptions = 0
	o.requestAwareHasBaseline = false
}

func (o *predictiveVLLMObserver) qualifyCompletionQoSEvidenceLocked(current predictiveStablePrefillWindow) (predictiveCompletionQoSEvidence, bool) {
	previous := o.completionQoSWindow
	if previous == nil {
		return predictiveCompletionQoSEvidence{}, false
	}
	elapsed := current.ObservedAt.Sub(previous.ObservedAt)
	qualified := elapsed > 0 && elapsed <= o.maximumAge &&
		previous.Preemptions == current.Preemptions && previous.Generation < current.Generation &&
		previous.Waiting == 0 && current.Waiting == 0 &&
		previous.Running > 0 && previous.Running == current.Running
	if !qualified {
		return predictiveCompletionQoSEvidence{}, false
	}
	aggregateTPS := float64(current.Generation-previous.Generation) / elapsed.Seconds()
	perDecodeTPS := aggregateTPS / float64(previous.Running)
	if aggregateTPS <= 0 || perDecodeTPS <= 0 {
		return predictiveCompletionQoSEvidence{}, false
	}
	tpot := time.Duration(float64(time.Second) / perDecodeTPS)
	if tpot <= 0 {
		return predictiveCompletionQoSEvidence{}, false
	}
	return predictiveCompletionQoSEvidence{
		StartedAt:              previous.ObservedAt,
		ObservedAt:             current.ObservedAt,
		AggregateCompletionTPS: aggregateTPS,
		DecodeSequences:        previous.Running,
		PerDecodeTPS:           perDecodeTPS,
		TPOT:                   tpot,
	}, true
}

func observePredictiveLoadPressure(observer predictiveLoadPressureObserver, now time.Time, kind runtimepredictive.LoadPressureKind) (observed bool) {
	if observer == nil {
		return false
	}
	defer func() {
		if recover() != nil {
			observed = false
		}
	}()
	return observer.ObserveLoadPressure(now, kind)
}

func (o *predictiveVLLMObserver) qualifyStablePrefillOutcomeLocked(current predictiveStablePrefillWindow, started, finished, shadowStarted, shadowFinished uint64) (runtimepredictive.ExistingPrefillOutcome, bool) {
	previous := o.prefillWindow
	if previous == nil || o.existingPrefillLearner == nil {
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
	qualified := o.existingPrefillLearner != nil && o.existingPrefillIdentity.Validate() == nil && o.coordinatorSnapshot != nil && previousPending.Episode.valid() &&
		elapsed > 0 && elapsed <= o.maximumAge && previous.EventSequence == started && started == finished && current.Manager.EventSequence == finished &&
		previous.ShadowPending.EventSequence == shadowStarted && shadowStarted == shadowFinished && current.ShadowPending.EventSequence == shadowFinished &&
		previous.Preemptions == current.Preemptions && previous.Generation <= current.Generation &&
		previous.Waiting == 0 && current.Waiting == 0 && previous.Running == current.Running &&
		pending > 0 && pendingTokens > 0 && previousPending.FeaturesValid &&
		(!previousPending.FromShadow || previousPending.DecisionManagerSequence == previous.Manager.EventSequence) &&
		currentPending.Count == pending && currentPending.Tokens == pendingTokens &&
		currentPending.FeaturesValid && currentPending.Features == features &&
		currentPending.Exploratory == previousPending.Exploratory &&
		currentPending.Episode == previousPending.Episode &&
		features.ExistingPendingPrefillSequences == pending-1 && features.PendingPrefillSequences == pending &&
		features.ExistingUncachedPrefill >= 0 && features.ExistingUncachedPrefill < features.UncachedPrefillTokens &&
		features.UncachedPrefillTokens == pendingTokens &&
		previous.Running == features.DecodeSequences
	existingDecoders := features.ExistingDecodeSequences - features.ExistingPendingPrefillSequences
	if !qualified || existingDecoders <= 0 {
		o.prefillStats.Censored++
		return runtimepredictive.ExistingPrefillOutcome{}, false
	}
	if !o.prefillEpisodes.MarkEmitted(previousPending.Episode) {
		o.prefillStats.Deduplicated++
		return runtimepredictive.ExistingPrefillOutcome{}, false
	}
	generationTPS := float64(current.Generation-previous.Generation) / elapsed.Seconds()
	perUserTPS := generationTPS / float64(existingDecoders)
	return runtimepredictive.ExistingPrefillOutcome{
		Identity:                o.existingPrefillIdentity,
		StartedAt:               previous.ObservedAt,
		ObservedAt:              current.ObservedAt,
		Features:                features,
		ExistingDecodeSequences: existingDecoders,
		PendingPrefillSequences: pending,
		PendingPrefillTokens:    pendingTokens,
		ExistingUserTPS:         perUserTPS,
		Exploratory:             previousPending.Exploratory,
	}, true
}

func (o *predictiveVLLMObserver) observeStablePrefillOutcome(outcome runtimepredictive.ExistingPrefillOutcome) {
	err := observePredictiveExistingPrefill(o.existingPrefillLearner, outcome)
	o.mu.Lock()
	defer o.mu.Unlock()
	if err != nil {
		o.prefillStats.Rejected++
		return
	}
	o.prefillStats.Accepted++
	o.prefillStats.LastExistingUserTPS = outcome.ExistingUserTPS
	o.prefillStats.LastExistingUserTPSValid = true
	o.prefillStats.LastExploratory = outcome.Exploratory
}

func observePredictiveExistingPrefill(learner predictiveExistingPrefillLearner, outcome runtimepredictive.ExistingPrefillOutcome) (err error) {
	if learner == nil {
		return fmt.Errorf("predictive existing-prefill learner is nil")
	}
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("predictive existing-prefill learner panicked")
		}
	}()
	return learner.ObserveExistingPrefill(outcome)
}

func (o *predictiveVLLMObserver) qualifyStableAggregateThroughputOutcomeLocked(current predictiveStablePrefillWindow, started, finished, shadowStarted, shadowFinished uint64) (runtimepredictive.AggregateThroughputOutcome, bool) {
	previous := o.prefillWindow
	if previous == nil || o.aggregateThroughputLearner == nil || o.aggregateThroughputIdentity.Validate() != nil || o.coordinatorSnapshot == nil {
		return runtimepredictive.AggregateThroughputOutcome{}, false
	}
	elapsed := current.ObservedAt.Sub(previous.ObservedAt)
	previousPending := observedPredictivePendingPrefills(previous.Manager, previous.ShadowPending)
	currentPending := observedPredictivePendingPrefills(current.Manager, current.ShadowPending)
	qualified := elapsed > 0 && elapsed <= o.maximumAge &&
		previous.EventSequence == started && started == finished && current.Manager.EventSequence == finished &&
		previous.ShadowPending.EventSequence == shadowStarted && shadowStarted == shadowFinished && current.ShadowPending.EventSequence == shadowFinished &&
		previous.Preemptions == current.Preemptions && previous.Generation < current.Generation &&
		previous.Waiting == 0 && current.Waiting == 0 && previous.Running > 0 && previous.Running == current.Running &&
		previousPending.Count == 0 && currentPending.Count == 0
	if !qualified {
		return runtimepredictive.AggregateThroughputOutcome{}, false
	}
	completionTPS := float64(current.Generation-previous.Generation) / elapsed.Seconds()
	if completionTPS <= 0 {
		return runtimepredictive.AggregateThroughputOutcome{}, false
	}
	return runtimepredictive.AggregateThroughputOutcome{
		Identity:               o.aggregateThroughputIdentity,
		StartedAt:              previous.ObservedAt,
		ObservedAt:             current.ObservedAt,
		DecodeSequences:        previous.Running,
		AggregateCompletionTPS: completionTPS,
	}, true
}

func (o *predictiveVLLMObserver) observeStableAggregateThroughputOutcome(outcome runtimepredictive.AggregateThroughputOutcome) {
	_ = observePredictiveAggregateThroughput(o.aggregateThroughputLearner, outcome)
}

func observePredictiveAggregateThroughput(learner predictiveAggregateThroughputLearner, outcome runtimepredictive.AggregateThroughputOutcome) (err error) {
	if learner == nil {
		return fmt.Errorf("predictive aggregate-throughput learner is nil")
	}
	defer func() {
		if recover() != nil {
			err = fmt.Errorf("predictive aggregate-throughput learner panicked")
		}
	}()
	return learner.ObserveAggregateThroughput(outcome)
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
	o.completionQoSWindow = nil
	o.completionQoS = predictiveCompletionQoSEvidence{}
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
	case shadow.Count > 0 && shadow.FeaturesValid &&
		shadow.DecisionManagerSequence == manager.EventSequence &&
		shadow.Features.PendingPrefillSequences == result.Count &&
		shadow.Features.UncachedPrefillTokens == result.Tokens:
		// A compatible shadow feature vector is the latest counterfactual
		// marginal request. It may already include Manager pending pressure and
		// therefore owns mixed Manager-plus-shadow attribution as well.
		result.Features = shadow.Features
		result.FeaturesValid = true
		result.FromShadow = true
		result.DecisionManagerSequence = shadow.DecisionManagerSequence
		result.Exploratory = shadow.Exploratory
		result.Episode = predictivePrefillEpisodeIdentity{Origin: predictivePrefillEpisodeShadow, Sequence: shadow.EventSequence}
	case manager.ForwardedPendingPrefills > 0 && shadow.Count == 0 && manager.ForwardedPendingPrefillFeaturesValid:
		result.Features = manager.ForwardedPendingPrefillFeatures
		result.FeaturesValid = true
		result.Exploratory = manager.ForwardedPendingPrefillExploratory
		result.Episode = predictivePrefillEpisodeIdentity{Origin: predictivePrefillEpisodeManager, Sequence: manager.ForwardedPendingPrefillSequence}
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
	o.resetRequestAwareInputLocked()
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
