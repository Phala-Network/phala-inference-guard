package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type observerMetricsFixture struct {
	mu   sync.Mutex
	body string
}

type blockingSampleCoordinator struct {
	delegate predictiveSampleCoordinator
	mu       sync.Mutex
	block    bool
	entered  chan struct{}
	release  chan struct{}
}

type learningInvalidationCoordinator struct {
	delegate predictiveSampleCoordinator
	mu       sync.Mutex
	count    int
}

type stablePrefillTestCoordinator struct {
	mu            sync.Mutex
	sequence      uint64
	pending       int
	pendingTokens int64
	manager       runtimepredictive.Snapshot
}

type recordingExistingPrefillLearner struct {
	mu       sync.Mutex
	outcomes []runtimepredictive.ExistingPrefillOutcome
	err      error
}

func (*recordingExistingPrefillLearner) Identity() runtimepredictive.ModelIdentity {
	return adapterTestIdentity()
}

func (l *recordingExistingPrefillLearner) ObserveExistingPrefill(outcome runtimepredictive.ExistingPrefillOutcome) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.outcomes = append(l.outcomes, outcome)
	return l.err
}

func (l *recordingExistingPrefillLearner) Outcomes() []runtimepredictive.ExistingPrefillOutcome {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]runtimepredictive.ExistingPrefillOutcome(nil), l.outcomes...)
}

func (c *stablePrefillTestCoordinator) StartSampleWindow() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sequence
}

func (c *stablePrefillTestCoordinator) EventSequence() uint64 {
	return c.StartSampleWindow()
}

func (c *stablePrefillTestCoordinator) ReconcileSample(sample runtimepredictive.SampleWindow) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	raw := sample.Observed
	state := sample.Observed
	state.DecodeSequences += c.pending
	state.PendingPrefillSequences = c.pending
	state.UncachedPrefillTokens = c.pendingTokens
	state.ActiveContextTokens += c.pendingTokens
	state.PhysicalKVUpper += c.pendingTokens
	state.ActiveKVUpper += c.pendingTokens
	c.manager = runtimepredictive.Snapshot{
		IntakeOpen:                    true,
		EventSequence:                 c.sequence,
		ForwardedPendingPrefills:      c.pending,
		ForwardedPendingPrefillTokens: c.pendingTokens,
		Virtual:                       domainpredictive.VirtualStateInterval{Lower: state, Upper: state},
	}
	if c.pending == 1 && raw.DecodeSequences >= 1 && c.pendingTokens > 0 {
		existingContext := raw.ActiveContextTokens - c.pendingTokens
		if existingContext < 0 {
			existingContext = 0
		}
		existingPhysical := raw.PhysicalKVUpper - c.pendingTokens
		if existingPhysical < 0 {
			existingPhysical = 0
		}
		existingActive := raw.ActiveKVUpper - c.pendingTokens
		if existingActive < 0 {
			existingActive = 0
		}
		c.manager.ForwardedPendingPrefillFeatures = runtimepredictive.SchedulerFeatures{
			ExistingDecodeSequences:         raw.DecodeSequences - 1,
			DecodeSequences:                 raw.DecodeSequences,
			ExistingPendingPrefillSequences: 0,
			PendingPrefillSequences:         1,
			ExistingActiveContextTokens:     existingContext,
			ExistingPhysicalKVUpper:         existingPhysical,
			ExistingActiveKVUpper:           existingActive,
			RequestComplexityTokensUpper:    c.pendingTokens,
			ActiveContextTokens:             raw.ActiveContextTokens,
			UncachedPrefillTokens:           c.pendingTokens,
			PhysicalKVUpper:                 raw.PhysicalKVUpper,
			ActiveKVUpper:                   raw.ActiveKVUpper,
		}
		c.manager.ForwardedPendingPrefillFeaturesValid = true
	}
	return nil
}

func (c *stablePrefillTestCoordinator) Snapshot() runtimepredictive.CountCoordinatorSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return runtimepredictive.CountCoordinatorSnapshot{Manager: c.manager}
}

func (c *stablePrefillTestCoordinator) Set(sequence uint64, pending int, pendingTokens int64) {
	c.mu.Lock()
	c.sequence = sequence
	c.pending = pending
	c.pendingTokens = pendingTokens
	c.mu.Unlock()
}

func (c *learningInvalidationCoordinator) StartSampleWindow() uint64 {
	return c.delegate.StartSampleWindow()
}

func (c *learningInvalidationCoordinator) EventSequence() uint64 {
	return c.delegate.EventSequence()
}

func (c *learningInvalidationCoordinator) ReconcileSample(sample runtimepredictive.SampleWindow) error {
	return c.delegate.ReconcileSample(sample)
}

func (c *learningInvalidationCoordinator) InvalidateLearning() {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
}

func (c *learningInvalidationCoordinator) InvalidateEpoch() bool {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	if invalidator, ok := c.delegate.(predictiveEpochInvalidator); ok {
		return invalidator.InvalidateEpoch()
	}
	return false
}

func (c *learningInvalidationCoordinator) Invalidations() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func newBlockingSampleCoordinator(delegate predictiveSampleCoordinator) *blockingSampleCoordinator {
	return &blockingSampleCoordinator{
		delegate: delegate,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (c *blockingSampleCoordinator) StartSampleWindow() uint64 {
	return c.delegate.StartSampleWindow()
}

func (c *blockingSampleCoordinator) EventSequence() uint64 {
	return c.delegate.EventSequence()
}

func (c *blockingSampleCoordinator) ReconcileSample(sample runtimepredictive.SampleWindow) error {
	c.mu.Lock()
	block := c.block
	if block {
		c.block = false
	}
	c.mu.Unlock()
	if block {
		close(c.entered)
		<-c.release
	}
	return c.delegate.ReconcileSample(sample)
}

func (c *blockingSampleCoordinator) InvalidateLearning() {
	if invalidator, ok := c.delegate.(predictiveLearningInvalidator); ok {
		invalidator.InvalidateLearning()
	}
}

func (c *blockingSampleCoordinator) blockNextReconciliation() {
	c.mu.Lock()
	c.block = true
	c.mu.Unlock()
}

func (f *observerMetricsFixture) ServeHTTP(w http.ResponseWriter, _ *http.Request) {
	f.mu.Lock()
	body := f.body
	f.mu.Unlock()
	_, _ = w.Write([]byte(body))
}

func (f *observerMetricsFixture) Set(body string) {
	f.mu.Lock()
	f.body = body
	f.mu.Unlock()
}

func TestPredictiveVLLMObserverRecoversTransientMetricsButQuarantinesCapacityDrift(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(4_000, 0)}
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	fixture := &observerMetricsFixture{body: observerMetrics(1_000, 0.25, 1, 0, 0, false)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) {
		t.Fatal("incomplete vLLM sample became healthy")
	}
	if snapshot := coordinator.Snapshot(); !snapshot.Manager.IntakeOpen || snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 0 {
		t.Fatalf("transient metrics failure mutated or quarantined predictive state: %+v", snapshot)
	}

	fixture.Set(observerMetrics(1_000, 0.25, 1, 0, 0, true))
	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) {
		t.Fatal("valid exact-capacity vLLM sample did not recover from transient metrics")
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 250 {
		t.Fatalf("valid sample physical KV = %+v, want 250 tokens", snapshot.Manager.Virtual)
	}

	clock.Advance(time.Second)
	fixture.Set(observerMetrics(2_000, 0.40, 2, 0, 0, true))
	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) {
		t.Fatal("observed runtime capacity identity drift retained old healthy authorization")
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.IntakeOpen || snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 250 {
		t.Fatalf("identity drift did not quarantine old coordinator: %+v", snapshot.Manager)
	}
	fixture.Set(observerMetrics(1_000, 0.25, 1, 0, 0, true))
	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) || coordinator.Snapshot().Manager.IntakeOpen {
		t.Fatal("old coordinator recovered after capacity drift without reconstruction")
	}
}

func TestPredictiveVLLMObserverAcceptsExactUnalignedGroupAwareCapacity(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(4_250, 0)}
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	fixture := &observerMetricsFixture{body: observerMetrics(1_003, 0, 0, 0, 0, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_003, coordinator, clock.Now)

	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) {
		t.Fatal("exact unaligned group-aware KV capacity did not become healthy")
	}
}

func TestPredictiveVLLMObserverFailsClosedOnBlockSizeOrServedModelMismatch(t *testing.T) {
	for name, body := range map[string]string{
		"block size": strings.Replace(
			observerMetricsWithModel(1_000, 0.10, 1, 0, 0, "google/gemma-4-fixture", true),
			`block_size="4"`, `block_size="8"`, 1,
		),
		"served model": observerMetricsWithModel(1_000, 0.10, 1, 0, 0, "other/model", true),
		"mixed models": strings.Replace(
			observerMetricsWithModel(1_000, 0.10, 1, 0, 0, "google/gemma-4-fixture", true),
			`vllm:num_requests_waiting{model_name="google/gemma-4-fixture",engine="0"}`,
			`vllm:num_requests_waiting{model_name="other/model",engine="0"}`,
			1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			clock := &adapterTestClock{now: time.Unix(4_500, 0)}
			coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
			fixture := &observerMetricsFixture{body: body}
			server := httptest.NewServer(fixture)
			defer server.Close()
			observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

			observer.poll(context.Background())
			if observer.Healthy(clock.Now()) {
				t.Fatal("runtime block size or served-model identity mismatch became healthy")
			}
			if snapshot := coordinator.Snapshot(); snapshot.Manager.Virtual.Upper != (domainpredictive.VirtualState{}) {
				t.Fatalf("identity-mismatched sample reconciled predictive state: %+v", snapshot.Manager.Virtual)
			}
		})
	}
}

func TestPredictiveVLLMObserverFreshnessRejectsFutureAndStaleClocks(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(5_000, 0)}
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	fixture := &observerMetricsFixture{body: observerMetrics(1_000, 0.10, 0, 0, 0, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.poll(context.Background())

	if !observer.Healthy(clock.Now()) {
		t.Fatal("fresh sample is unhealthy")
	}
	if observer.Healthy(clock.Now().Add(-time.Nanosecond)) {
		t.Fatal("future-dated sample became healthy")
	}
	if observer.Healthy(clock.Now().Add(observer.maximumAge + time.Nanosecond)) {
		t.Fatal("stale sample became healthy")
	}
}

func TestPredictiveVLLMObserverCounterResetInvalidatesOldFreshState(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(6_000, 0)}
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	fixture := &observerMetricsFixture{body: observerMetrics(1_000, 0.10, 1, 0, 7, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) {
		t.Fatal("initial counter baseline did not become healthy")
	}
	clock.Advance(time.Second)
	fixture.Set(observerMetrics(1_000, 0.20, 2, 0, 0, true))
	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) {
		t.Fatal("preemption counter reset left the old sample authorized as fresh")
	}

	clock.Advance(time.Second)
	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) {
		t.Fatal("old coordinator recovered after counter reset without reconstruction")
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.IntakeOpen || snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 100 {
		t.Fatalf("counter reset did not preserve and quarantine prior state: %+v", snapshot.Manager)
	}
}

func TestPredictiveVLLMObserverPreemptionIncrementStartsCooldown(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(7_000, 0)}
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	fixture := &observerMetricsFixture{body: observerMetrics(1_000, 0.10, 1, 0, 4, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.poll(context.Background())

	clock.Advance(time.Second)
	fixture.Set(observerMetrics(1_000, 0.11, 1, 0, 5, true))
	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) {
		t.Fatal("preemption increment did not start cooldown")
	}
	if !observer.Healthy(clock.Now().Add(observer.preemptionCooldown)) {
		t.Fatal("observer did not recover at the exact cooldown boundary")
	}
}

func TestPredictiveVLLMObserverInvalidatesLearningOnPreemptionAndCounterReset(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(7_250, 0)}
	coordinator := &learningInvalidationCoordinator{delegate: newAdapterTestCoordinatorWithTPSTarget(t, 0)}
	fixture := &observerMetricsFixture{body: observerMetrics(1_000, 0.10, 1, 0, 4, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

	observer.poll(context.Background())
	if got := coordinator.Invalidations(); got != 0 {
		t.Fatalf("initial preemption baseline invalidations = %d, want 0", got)
	}
	clock.Advance(time.Second)
	fixture.Set(observerMetrics(1_000, 0.11, 1, 0, 5, true))
	observer.poll(context.Background())
	if got := coordinator.Invalidations(); got != 1 {
		t.Fatalf("preemption increment invalidations = %d, want 1", got)
	}

	clock.Advance(time.Second)
	fixture.Set(observerMetrics(1_000, 0.12, 1, 0, 0, true))
	observer.poll(context.Background())
	if got := coordinator.Invalidations(); got != 2 {
		t.Fatalf("counter reset invalidations = %d, want 2", got)
	}
}

func TestPredictiveVLLMObserverGenerationResetDetectsZeroToZeroPreemptionEpochChange(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(7_400, 0)}
	coordinator := &learningInvalidationCoordinator{delegate: newAdapterTestCoordinatorWithTPSTarget(t, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 1, 0, 0, 500, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) {
		t.Fatal("initial generation baseline did not become healthy")
	}
	clock.Advance(time.Second)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.20, 1, 0, 0, 0, true))
	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) {
		t.Fatal("generation reset with preemptions 0 to 0 retained old healthy authorization")
	}
	if got := coordinator.Invalidations(); got != 1 {
		t.Fatalf("generation reset invalidations = %d, want 1", got)
	}
	if snapshotter, ok := coordinator.delegate.(interface {
		Snapshot() runtimepredictive.CountCoordinatorSnapshot
	}); !ok {
		t.Fatal("test coordinator has no snapshot")
	} else if got := snapshotter.Snapshot().Manager.Virtual.Upper.PhysicalKVUpper; got != 100 {
		t.Fatalf("epoch-reset sample reconciled before stable baseline: KV=%d, want prior 100", got)
	}

	clock.Advance(time.Second)
	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) {
		t.Fatal("old coordinator recovered after generation reset without reconstruction")
	}
	if got := coordinator.Invalidations(); got != 1 {
		t.Fatalf("stable post-reset poll added invalidation: %d", got)
	}
}

func TestPredictiveVLLMObserverMissingGenerationSignalExpiresByFreshnessAndRecovers(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(7_450, 0)}
	coordinator := &learningInvalidationCoordinator{delegate: newAdapterTestCoordinatorWithTPSTarget(t, 0)}
	valid := observerMetrics(1_000, 0.10, 1, 0, 0, true)
	fixture := &observerMetricsFixture{body: valid}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) {
		t.Fatal("initial complete metric identity did not become healthy")
	}
	missingGeneration := strings.Replace(valid, `vllm:generation_tokens_total{model_name="google/gemma-4-fixture",engine="0"} 100`+"\n", "", 1)
	clock.Advance(time.Second)
	fixture.Set(missingGeneration)
	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) || coordinator.Invalidations() != 0 {
		t.Fatalf("single incomplete scrape health/invalidations = %t/%d, want prior fresh/0", observer.Healthy(clock.Now()), coordinator.Invalidations())
	}
	observer.poll(context.Background())
	if coordinator.Invalidations() != 0 {
		t.Fatalf("repeated incomplete scrape invalidations = %d, want 0", coordinator.Invalidations())
	}
	clock.Advance(observer.maximumAge)
	if observer.Healthy(clock.Now()) {
		t.Fatal("incomplete metrics never expired the last coherent sample")
	}
	fixture.Set(valid)
	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) || coordinator.Invalidations() != 0 {
		t.Fatalf("restored generation health/invalidations = %t/%d, want true/0", observer.Healthy(clock.Now()), coordinator.Invalidations())
	}
}

func TestPredictiveVLLMObserverFailsClosedOnMissingOrMalformedRequiredCounters(t *testing.T) {
	valid := observerMetrics(1_000, 0.10, 1, 0, 0, true)
	for name, body := range map[string]string{
		"missing running": strings.Replace(valid,
			`vllm:num_requests_running{model_name="google/gemma-4-fixture",engine="0"} 1`+"\n", "", 1),
		"missing waiting": strings.Replace(valid,
			`vllm:num_requests_waiting{model_name="google/gemma-4-fixture",engine="0"} 0`+"\n", "", 1),
		"missing preemptions": strings.Replace(valid,
			`vllm:num_preemptions_total{model_name="google/gemma-4-fixture",engine="0"} 0`+"\n", "", 1),
		"fractional running": strings.Replace(valid,
			`vllm:num_requests_running{model_name="google/gemma-4-fixture",engine="0"} 1`,
			`vllm:num_requests_running{model_name="google/gemma-4-fixture",engine="0"} 0.5`, 1),
		"infinite waiting": strings.Replace(valid,
			`vllm:num_requests_waiting{model_name="google/gemma-4-fixture",engine="0"} 0`,
			`vllm:num_requests_waiting{model_name="google/gemma-4-fixture",engine="0"} +Inf`, 1),
		"infinite preemption": strings.Replace(valid,
			`vllm:num_preemptions_total{model_name="google/gemma-4-fixture",engine="0"} 0`,
			`vllm:num_preemptions_total{model_name="google/gemma-4-fixture",engine="0"} +Inf`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			clock := &adapterTestClock{now: time.Unix(7_475, 0)}
			coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
			fixture := &observerMetricsFixture{body: body}
			server := httptest.NewServer(fixture)
			defer server.Close()
			observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

			observer.poll(context.Background())
			if observer.Healthy(clock.Now()) {
				t.Fatal("incomplete or malformed vLLM counter set became healthy")
			}
			if snapshot := coordinator.Snapshot(); snapshot.Manager.Virtual.Upper != (domainpredictive.VirtualState{}) {
				t.Fatalf("invalid counter sample reconciled predictive state: %+v", snapshot.Manager.Virtual)
			}
		})
	}
}

func TestPredictiveVLLMObserverPreemptionIncrementFailsClosedBeforeReconciliation(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(7_500, 0)}
	coordinator := newBlockingSampleCoordinator(newAdapterTestCoordinatorWithTPSTarget(t, 0))
	fixture := &observerMetricsFixture{body: observerMetrics(1_000, 0.10, 1, 0, 4, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) {
		t.Fatal("initial sample did not become healthy")
	}

	clock.Advance(time.Second)
	fixture.Set(observerMetrics(1_000, 0.11, 1, 0, 5, true))
	coordinator.blockNextReconciliation()
	done := make(chan struct{})
	go func() {
		observer.poll(context.Background())
		close(done)
	}()
	<-coordinator.entered

	if observer.Healthy(clock.Now()) {
		close(coordinator.release)
		<-done
		t.Fatal("preemption increment left the old healthy sample authorized while reconciliation was in progress")
	}
	close(coordinator.release)
	<-done
	if observer.Healthy(clock.Now()) {
		t.Fatal("preemption increment did not retain cooldown after reconciliation")
	}
}

func TestPredictiveVLLMObserverSampleWindowDoesNotAbsorbUnforwardedConcurrentReservation(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(8_000, 0)}
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(observerMetrics(1_000, 0, 0, 0, 0, true)))
	}))
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	done := make(chan struct{})
	go func() {
		observer.poll(context.Background())
		close(done)
	}()
	<-started
	result := coordinator.DecideAndReserve(clock.Now(), runtimepredictive.CountAdmissionProposal{
		RequestID: "during-scrape",
		Analysis: runtimepredictive.TokenCountAnalysis{
			ManifestID:       "adapter-test-manifest",
			BackendEpoch:     "adapter-test-epoch",
			ExactInputTokens: 4,
		},
		DecodeHorizonUpper: 16,
		Confidence:         1,
	})
	if !result.Reserved {
		close(release)
		<-done
		t.Fatalf("concurrent reservation was not created: %+v", result)
	}
	close(release)
	<-done
	snapshot := coordinator.Snapshot()
	if snapshot.Manager.Virtual.Lower.PhysicalKVUpper != 20 || snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 20 {
		t.Fatalf("unforwarded scrape-window reservation interval = %+v, want exact full KV reservation [20,20]", snapshot.Manager.Virtual)
	}
	if snapshot.Manager.Virtual.Lower.UncachedPrefillTokens != 4 || snapshot.Manager.Virtual.Upper.UncachedPrefillTokens != 4 {
		t.Fatalf("unforwarded scrape-window prefill interval = %+v, want exact full prefill reservation [4,4]", snapshot.Manager.Virtual)
	}
	if !coordinator.Terminate("during-scrape", runtimepredictive.TerminalCompleted) {
		t.Fatal("concurrent reservation did not terminate")
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 0 || snapshot.Manager.Reservations != 0 {
		t.Fatalf("post-completion concurrent state leaked: %+v", snapshot.Manager)
	}
}

func TestPredictiveVLLMObserverCloseCancelsInflightFetch(t *testing.T) {
	requestStarted := make(chan struct{})
	requestCancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(requestStarted)
		<-request.Context().Done()
		close(requestCancelled)
	}))
	defer server.Close()
	observer, err := newPredictiveVLLMObserver(predictiveVLLMObserverConfig{
		MetricsURL:         server.URL,
		ServedModel:        "google/gemma-4-fixture",
		MaximumKVTokens:    1_000,
		BlockSize:          4,
		PollInterval:       time.Hour,
		MaximumAge:         2 * time.Hour,
		RequestTimeout:     time.Hour,
		PreemptionCooldown: time.Minute,
		Coordinator:        newAdapterTestCoordinatorWithTPSTarget(t, 0),
	})
	if err != nil {
		t.Fatalf("new observer: %v", err)
	}
	<-requestStarted
	started := time.Now()
	if err := observer.Close(); err != nil {
		t.Fatalf("close observer: %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("observer close took %s, want <= 1s", elapsed)
	}
	select {
	case <-requestCancelled:
	case <-time.After(time.Second):
		t.Fatal("in-flight metrics request was not cancelled")
	}
	if err := observer.Close(); err != nil {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestPredictiveVLLMObserverLearnsZeroTPSOnlyFromStablePendingPrefillWindow(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(90_000, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	coordinator := &stablePrefillTestCoordinator{}
	coordinator.Set(7, 1, 100)
	learner := &recordingExistingPrefillLearner{}
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.coordinatorSnapshot = coordinator
	observer.existingPrefillLearner = learner

	observer.poll(context.Background())
	clock.Advance(time.Second)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 100, true))
	observer.poll(context.Background())

	outcomes := learner.Outcomes()
	if len(outcomes) != 1 {
		t.Fatalf("stable prefill outcomes = %d, want 1", len(outcomes))
	}
	if outcomes[0].ExistingUserTPS != 0 || outcomes[0].ExistingDecodeSequences != 1 || outcomes[0].PendingPrefillSequences != 1 || outcomes[0].PendingPrefillTokens != 100 {
		t.Fatalf("stable zero-generation outcome = %+v", outcomes[0])
	}
	stats := observer.ExistingPrefillTelemetry()
	if stats.Accepted != 1 || stats.Rejected != 0 || stats.Censored != 0 || !stats.LastExistingUserTPSValid || stats.LastExistingUserTPS != 0 {
		t.Fatalf("stable prefill telemetry = %+v", stats)
	}
}

func TestPredictiveVLLMObserverLearnsFromOneStableAnonymousShadowPrefill(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(90_125, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	coordinator := &stablePrefillTestCoordinator{}
	coordinator.Set(8, 0, 0)
	learner := &recordingExistingPrefillLearner{}
	shadowPrefills := newPredictiveShadowPendingPrefillStore(4)
	handle := shadowPrefills.Begin(runtimepredictive.PendingPrefillObservation{
		Tokens:                  100,
		DecisionManagerSequence: 8,
		Features: runtimepredictive.SchedulerFeatures{
			ExistingDecodeSequences: 1,
			DecodeSequences:         2,
			PendingPrefillSequences: 1,
			UncachedPrefillTokens:   100,
		},
	})
	if handle == nil {
		t.Fatal("one anonymous shadow prefill was not retained")
	}
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.coordinatorSnapshot = coordinator
	observer.existingPrefillLearner = learner
	observer.shadowPendingPrefills = shadowPrefills

	observer.poll(context.Background())
	clock.Advance(time.Second)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 160, true))
	observer.poll(context.Background())

	outcomes := learner.Outcomes()
	if len(outcomes) != 1 || outcomes[0].ExistingUserTPS != 60 ||
		outcomes[0].ExistingDecodeSequences != 1 || outcomes[0].PendingPrefillSequences != 1 ||
		outcomes[0].PendingPrefillTokens != 100 {
		t.Fatalf("stable anonymous shadow-prefill outcomes = %+v", outcomes)
	}
	if stats := observer.ExistingPrefillTelemetry(); stats.Accepted != 1 || stats.Censored != 0 ||
		!stats.LastExistingUserTPSValid || stats.LastExistingUserTPS != 60 {
		t.Fatalf("stable anonymous shadow-prefill telemetry = %+v", stats)
	}
	if !handle.End() {
		t.Fatal("anonymous shadow-prefill handle did not end")
	}
}

func TestPredictiveVLLMObserverCensorsAmbiguousOrChangedShadowPrefillAttribution(t *testing.T) {
	features := runtimepredictive.SchedulerFeatures{ExistingDecodeSequences: 1, DecodeSequences: 2, PendingPrefillSequences: 1, UncachedPrefillTokens: 100}
	for _, test := range []struct {
		name   string
		mutate func(*predictiveShadowPendingPrefillStore, *predictiveShadowPendingPrefillHandle)
	}{
		{
			name: "multiple_shadow_prefills",
			mutate: func(store *predictiveShadowPendingPrefillStore, _ *predictiveShadowPendingPrefillHandle) {
				if store.Begin(runtimepredictive.PendingPrefillObservation{Tokens: 100, Features: features, DecisionManagerSequence: 9}) == nil {
					t.Fatal("second shadow prefill was not retained")
				}
			},
		},
		{
			name: "same_features_new_attribution",
			mutate: func(store *predictiveShadowPendingPrefillStore, first *predictiveShadowPendingPrefillHandle) {
				if !first.End() || store.Begin(runtimepredictive.PendingPrefillObservation{Tokens: 100, Features: features, DecisionManagerSequence: 9}) == nil {
					t.Fatal("shadow prefill attribution did not change")
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &adapterTestClock{now: time.Unix(90_200, 0)}
			fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 100, true)}
			server := httptest.NewServer(fixture)
			defer server.Close()
			coordinator := &stablePrefillTestCoordinator{}
			coordinator.Set(9, 0, 0)
			learner := &recordingExistingPrefillLearner{}
			store := newPredictiveShadowPendingPrefillStore(4)
			first := store.Begin(runtimepredictive.PendingPrefillObservation{Tokens: 100, Features: features, DecisionManagerSequence: 9})
			if first == nil {
				t.Fatal("first shadow prefill was not retained")
			}
			observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
			observer.coordinatorSnapshot = coordinator
			observer.existingPrefillLearner = learner
			observer.shadowPendingPrefills = store
			observer.poll(context.Background())

			test.mutate(store, first)
			clock.Advance(time.Second)
			fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 160, true))
			observer.poll(context.Background())

			if outcomes := learner.Outcomes(); len(outcomes) != 0 {
				t.Fatalf("ambiguous shadow attribution trained outcomes: %+v", outcomes)
			}
			if stats := observer.ExistingPrefillTelemetry(); stats.Accepted != 0 || stats.Censored != 1 {
				t.Fatalf("ambiguous shadow-prefill telemetry = %+v", stats)
			}
		})
	}
}

func TestPredictiveVLLMObserverCensorsShadowPrefillWhenManagerChangedBeforeFirstPoll(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(90_225, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	coordinator := &stablePrefillTestCoordinator{}
	coordinator.Set(9, 0, 0)
	learner := &recordingExistingPrefillLearner{}
	store := newPredictiveShadowPendingPrefillStore(4)
	handle := store.Begin(runtimepredictive.PendingPrefillObservation{
		Tokens:                  100,
		DecisionManagerSequence: 8,
		Features: runtimepredictive.SchedulerFeatures{
			ExistingDecodeSequences: 1,
			DecodeSequences:         2,
			PendingPrefillSequences: 1,
			UncachedPrefillTokens:   100,
		},
	})
	if handle == nil {
		t.Fatal("shadow prefill was not retained")
	}
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.coordinatorSnapshot = coordinator
	observer.existingPrefillLearner = learner
	observer.shadowPendingPrefills = store

	observer.poll(context.Background())
	clock.Advance(time.Second)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 160, true))
	observer.poll(context.Background())

	if outcomes := learner.Outcomes(); len(outcomes) != 0 {
		t.Fatalf("stale decision features trained outcomes: %+v", outcomes)
	}
	if stats := observer.ExistingPrefillTelemetry(); stats.Accepted != 0 || stats.Censored != 1 {
		t.Fatalf("stale decision-sequence telemetry = %+v", stats)
	}
}

func TestStableZeroGenerationPrefillWindowTightensNextRealAdmission(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(90_250, 0)}
	identity := adapterTestIdentity()
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity: identity, BaseCompletionTPS: 100, PrefillTPSPenaltyPerKToken: 0,
		BaseTTFT: time.Millisecond, BaseTPOT: time.Millisecond, Confidence: 1,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity: identity, MinimumSamples: 3, MaximumSamplesPerCell: 8, MaximumCells: 32, MaxAge: time.Minute,
		LowerQuantile: 0.1, UpperQuantile: 0.9, MinimumTPSMultiplier: 0.2, MaximumTPSMultiplier: 1,
		MinimumLatencyMultiplier: 1, MaximumLatencyMultiplier: 2, CalibratedConfidence: 1,
		DecodeSequenceBucket: 1, ContextTokenBucket: 128, PrefillTokenBucket: 128, KVTokenBucket: 128,
	})
	if err != nil {
		t.Fatalf("new real prefill learner: %v", err)
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID: "adapter-test-manifest", BackendEpoch: identity.BackendEpoch, Scheduler: identity, BlockSize: 4,
		},
		ModelMaximumLength: 10_000,
		Initial: domainpredictive.VirtualState{
			PhysicalKVUpper: 1_000, ActiveKVUpper: 1_000, DecodeSequences: 1, ActiveContextTokens: 1_000,
		},
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard: 9_000, ActiveKVHard: 9_000, UserTPSTarget: 25, TPOTSLO: time.Second,
			WorkspaceRiskBudget: 1, PreemptionRiskBudget: 1, MinimumConfidence: 1,
		},
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatalf("new real prefill coordinator: %v", err)
	}
	proposal := runtimepredictive.UpperBoundAdmissionProposal{
		RequestID: "stable-prefill", InputTokensUpper: 100, RawInputTokensHigh: 100, DecodeHorizonUpper: 16, Confidence: 1,
	}
	first := coordinator.DecideUpperBoundAndReserve(clock.Now(), proposal)
	if !first.Reserved || first.Decision.Reason != domainpredictive.ReasonFit || !coordinator.MarkForwarded(proposal.RequestID) {
		t.Fatalf("initial real prefill admission/forward = %+v", first)
	}

	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(10_000, 0.11, 2, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 10_000, coordinator, clock.Now)
	observer.coordinatorSnapshot = coordinator
	observer.existingPrefillLearner = scheduler
	observer.poll(context.Background())
	clock.Advance(time.Second)
	observer.poll(context.Background())
	if stats := observer.ExistingPrefillTelemetry(); stats.Accepted != 1 || stats.Rejected != 0 || !stats.LastExistingUserTPSValid || stats.LastExistingUserTPS != 0 {
		t.Fatalf("real stable prefill learner telemetry = %+v", stats)
	}

	if !coordinator.Terminate(proposal.RequestID, runtimepredictive.TerminalExpired) {
		t.Fatal("stable prefill reservation did not terminate")
	}
	clock.Advance(time.Second)
	fixture.Set(observerMetricsWithGeneration(10_000, 0.10, 1, 0, 0, 100, true))
	observer.poll(context.Background())
	nextProposal := proposal
	nextProposal.RequestID = "after-zero-generation"
	next := coordinator.DecideUpperBoundAndReserve(clock.Now(), nextProposal)
	if next.Reserved || next.Decision.Reason != domainpredictive.ReasonExistingTPSAtRisk ||
		next.Prediction.Source != runtimepredictive.PredictionSourceCalibrated || next.Prediction.Samples != 1 {
		t.Fatalf("zero-generation evidence did not tighten the next real pre-forward admission: %+v", next)
	}
}

func TestShadowPrefillEvidenceProgressivelyUnlocksOnlyTheNextSafeAdmission(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(90_375, 0)}
	identity := adapterTestIdentity()
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity: identity, BaseCompletionTPS: 100, PrefillTPSPenaltyPerKToken: 40,
		BaseTTFT: time.Millisecond, BaseTPOT: time.Millisecond, Confidence: 1,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity: identity, MinimumSamples: 3, MaximumSamplesPerCell: 8, MaximumCells: 32, MaxAge: time.Minute,
		LowerQuantile: 0.1, UpperQuantile: 0.9, MinimumTPSMultiplier: 0.2, MaximumTPSMultiplier: 10,
		MinimumLatencyMultiplier: 1, MaximumLatencyMultiplier: 2, CalibratedConfidence: 1,
		DecodeSequenceBucket: 1, ContextTokenBucket: 128, PrefillTokenBucket: 128, KVTokenBucket: 128,
	})
	if err != nil {
		t.Fatalf("new progressive shadow-prefill learner: %v", err)
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID: "adapter-test-manifest", BackendEpoch: identity.BackendEpoch, Scheduler: identity, BlockSize: 4,
		},
		ModelMaximumLength: 10_000,
		Initial: domainpredictive.VirtualState{
			PhysicalKVUpper: 1_000, ActiveKVUpper: 1_000, DecodeSequences: 1, ActiveContextTokens: 1_000,
		},
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard: 9_000, ActiveKVHard: 9_000, UserTPSTarget: 25, TPOTSLO: time.Second,
			WorkspaceRiskBudget: 1, PreemptionRiskBudget: 1, MinimumConfidence: 1,
		},
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatalf("new progressive shadow-prefill coordinator: %v", err)
	}
	shadowPrefills := newPredictiveShadowPendingPrefillStore(4)
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 3, 1, 1),
		Coordinator:            coordinator,
		Learner:                scheduler,
		Mode:                   "shadow",
		ShadowObservationLimit: 4,
		ShadowPendingPrefills:  shadowPrefills,
		Now:                    clock.Now,
	})
	if err != nil {
		t.Fatalf("new progressive shadow-prefill adapter: %v", err)
	}
	fixture := &observerMetricsFixture{}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 10_000, coordinator, clock.Now)
	observer.coordinatorSnapshot = coordinator
	observer.existingPrefillLearner = scheduler
	observer.shadowPendingPrefills = shadowPrefills

	input := approximateAdapterTestInput()
	input.Cost.EstimatedInputLow = 2_000
	input.Cost.EstimatedInputHigh = 2_000
	generation := uint64(100)
	for sample := 0; sample < 3; sample++ {
		requestID := fmt.Sprintf("progressive-shadow-%d", sample)
		decision := adapter.Decide(context.Background(), requestID, input)
		if decision.Outcome != predictiveAdmissionOutcomeForward || decision.Reservation == nil {
			t.Fatalf("shadow exploration %d did not forward: %+v", sample, decision)
		}
		if manager := coordinator.Snapshot().Manager; manager.Reservations != 0 {
			t.Fatalf("shadow exploration %d entered resource accounting before learning: %+v", sample, manager)
		}
		if !decision.Reservation.MarkForwarded() || shadowPrefills.Snapshot().Count != 1 {
			t.Fatalf("shadow exploration %d did not publish one pending prefill", sample)
		}
		fixture.Set(observerMetricsWithGeneration(10_000, 0.10, 2, 0, 0, generation, true))
		observer.poll(context.Background())
		clock.Advance(time.Second)
		generation += 60
		fixture.Set(observerMetricsWithGeneration(10_000, 0.10, 2, 0, 0, generation, true))
		observer.poll(context.Background())
		if !decision.Reservation.MarkPrefillComplete() || !decision.Reservation.Terminate(runtimepredictive.TerminalCompleted) {
			t.Fatalf("shadow exploration %d did not complete and release", sample)
		}
		clock.Advance(time.Second)
		fixture.Set(observerMetricsWithGeneration(10_000, 0.10, 1, 0, 0, generation, true))
		observer.poll(context.Background())
		if pending := shadowPrefills.Snapshot(); pending.Count != 0 || pending.Tokens != 0 {
			t.Fatalf("shadow exploration %d left pending state: %+v", sample, pending)
		}
	}

	learned := adapter.Decide(context.Background(), "progressive-learned-fit", input)
	if learned.Outcome != predictiveAdmissionOutcomeForward || learned.Reservation == nil {
		t.Fatalf("mature shadow-prefill evidence did not unlock the next request: %+v", learned)
	}
	manager := coordinator.Snapshot().Manager
	if manager.Reservations != 1 {
		t.Fatalf("mature next request was still an observation-only forward: %+v", manager)
	}
	if stats := observer.ExistingPrefillTelemetry(); stats.Accepted != 3 || stats.Rejected != 0 {
		t.Fatalf("progressive shadow-prefill learning telemetry = %+v", stats)
	}
	if !learned.Reservation.Terminate(runtimepredictive.TerminalExpired) {
		t.Fatal("mature learned reservation did not release")
	}
}

func TestPredictiveVLLMObserverCensorsPendingPrefillBeforeVLLMRunningMaterializesIt(t *testing.T) {
	now := time.Unix(90_500, 0)
	features := runtimepredictive.SchedulerFeatures{
		ExistingDecodeSequences:      1,
		DecodeSequences:              2,
		PendingPrefillSequences:      1,
		RequestComplexityTokensUpper: 100,
		UncachedPrefillTokens:        100,
		ActiveContextTokens:          100,
		PhysicalKVUpper:              100,
		ActiveKVUpper:                100,
	}
	manager := runtimepredictive.Snapshot{
		EventSequence:                        7,
		ForwardedPendingPrefills:             1,
		ForwardedPendingPrefillTokens:        100,
		ForwardedPendingPrefillFeatures:      features,
		ForwardedPendingPrefillFeaturesValid: true,
	}
	observer := &predictiveVLLMObserver{
		maximumAge:             time.Minute,
		existingPrefillLearner: &recordingExistingPrefillLearner{},
		coordinatorSnapshot:    &stablePrefillTestCoordinator{},
		prefillWindow: &predictiveStablePrefillWindow{
			ObservedAt: now, Generation: 100, Running: 1, EventSequence: 7, Manager: manager,
		},
	}
	current := predictiveStablePrefillWindow{
		ObservedAt: now.Add(time.Second), Generation: 101, Running: 1, EventSequence: 7, Manager: manager,
	}
	if outcome, candidate := observer.qualifyStablePrefillOutcomeLocked(current, 7, 7, 0, 0); candidate {
		t.Fatalf("unmaterialized pending prefill produced optimistic training outcome: %+v", outcome)
	}
	if observer.prefillStats.Censored != 1 {
		t.Fatalf("unmaterialized prefill censor telemetry = %+v", observer.prefillStats)
	}
}

func TestPredictiveVLLMObserverCountsLearnerRejectionWithoutRetainingFalseSuccess(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(90_750, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	coordinator := &stablePrefillTestCoordinator{}
	coordinator.Set(9, 1, 100)
	learner := &recordingExistingPrefillLearner{err: fmt.Errorf("injected learner rejection")}
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.coordinatorSnapshot = coordinator
	observer.existingPrefillLearner = learner
	observer.poll(context.Background())
	clock.Advance(time.Second)
	observer.poll(context.Background())

	stats := observer.ExistingPrefillTelemetry()
	if stats.Accepted != 0 || stats.Rejected != 1 || stats.LastExistingUserTPSValid {
		t.Fatalf("rejected learner outcome telemetry = %+v", stats)
	}
}

func TestPredictiveVLLMObserverFetchFailureClearsPendingPrefillBaseline(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(90_875, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	coordinator := &stablePrefillTestCoordinator{}
	coordinator.Set(11, 1, 100)
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.coordinatorSnapshot = coordinator
	observer.existingPrefillLearner = &recordingExistingPrefillLearner{}
	observer.poll(context.Background())
	if observer.prefillWindow == nil {
		t.Fatal("first coherent pending-prefill poll did not establish a baseline")
	}

	clock.Advance(time.Second)
	fixture.Set("invalid metrics")
	observer.poll(context.Background())
	if observer.prefillWindow != nil || observer.ExistingPrefillTelemetry().Censored != 1 {
		t.Fatalf("failed fetch retained a trainable pending-prefill baseline: window=%+v telemetry=%+v", observer.prefillWindow, observer.ExistingPrefillTelemetry())
	}
}

func TestPredictiveVLLMObserverCensorsAmbiguousPrefillWindows(t *testing.T) {
	for _, test := range []struct {
		name              string
		initialRunning    int
		secondRunning     int
		initialPending    int
		secondPending     int
		initialTokens     int64
		secondTokens      int64
		initialSequence   uint64
		secondSequence    uint64
		initialGeneration uint64
		secondGeneration  uint64
		initialPreemption uint64
		secondPreemption  uint64
		secondWaiting     int
		wantCensored      uint64
	}{
		{name: "event_sequence_changed", initialRunning: 2, secondRunning: 2, initialPending: 1, secondPending: 1, initialTokens: 100, secondTokens: 100, initialSequence: 3, secondSequence: 4, initialGeneration: 100, secondGeneration: 110, wantCensored: 1},
		{name: "pending_count_changed", initialRunning: 3, secondRunning: 3, initialPending: 1, secondPending: 2, initialTokens: 100, secondTokens: 200, initialGeneration: 100, secondGeneration: 110, wantCensored: 1},
		{name: "pending_tokens_changed", initialRunning: 2, secondRunning: 2, initialPending: 1, secondPending: 1, initialTokens: 100, secondTokens: 200, initialGeneration: 100, secondGeneration: 110, wantCensored: 1},
		{name: "generation_reset", initialRunning: 2, secondRunning: 2, initialPending: 1, secondPending: 1, initialTokens: 100, secondTokens: 100, initialGeneration: 100, secondGeneration: 0, wantCensored: 1},
		{name: "preemption", initialRunning: 2, secondRunning: 2, initialPending: 1, secondPending: 1, initialTokens: 100, secondTokens: 100, initialGeneration: 100, secondGeneration: 110, secondPreemption: 1, wantCensored: 1},
		{name: "waiting", initialRunning: 2, secondRunning: 2, initialPending: 1, secondPending: 1, initialTokens: 100, secondTokens: 100, initialGeneration: 100, secondGeneration: 110, secondWaiting: 1, wantCensored: 1},
		{name: "running_changed", initialRunning: 2, secondRunning: 3, initialPending: 1, secondPending: 1, initialTokens: 100, secondTokens: 100, initialGeneration: 100, secondGeneration: 110, wantCensored: 1},
		{name: "no_existing_decoder", initialRunning: 1, secondRunning: 1, initialPending: 1, secondPending: 1, initialTokens: 100, secondTokens: 100, initialGeneration: 100, secondGeneration: 110, wantCensored: 1},
		{name: "no_pending_prefill", initialRunning: 1, secondRunning: 1, initialPending: 0, secondPending: 0, initialTokens: 0, secondTokens: 0, initialGeneration: 100, secondGeneration: 110, wantCensored: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := &adapterTestClock{now: time.Unix(91_000, 0)}
			fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, test.initialRunning, 0, test.initialPreemption, test.initialGeneration, true)}
			server := httptest.NewServer(fixture)
			defer server.Close()
			coordinator := &stablePrefillTestCoordinator{}
			coordinator.Set(test.initialSequence, test.initialPending, test.initialTokens)
			learner := &recordingExistingPrefillLearner{}
			observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
			observer.coordinatorSnapshot = coordinator
			observer.existingPrefillLearner = learner
			observer.poll(context.Background())

			clock.Advance(time.Second)
			coordinator.Set(test.secondSequence, test.secondPending, test.secondTokens)
			fixture.Set(observerMetricsWithGeneration(1_000, 0.10, test.secondRunning, test.secondWaiting, test.secondPreemption, test.secondGeneration, true))
			observer.poll(context.Background())

			if outcomes := learner.Outcomes(); len(outcomes) != 0 {
				t.Fatalf("ambiguous window trained %d outcomes: %+v", len(outcomes), outcomes)
			}
			stats := observer.ExistingPrefillTelemetry()
			if stats.Accepted != 0 || stats.Censored != test.wantCensored {
				t.Fatalf("ambiguous prefill telemetry = %+v, want censored=%d", stats, test.wantCensored)
			}
		})
	}
}

func TestPredictiveVLLMMetricsURLRequiresExactlyOneUpstream(t *testing.T) {
	for name, cfg := range map[string]config{
		"none":    {},
		"two":     {Backends: []backendConfig{{MetricsURL: "http://one/metrics"}, {MetricsURL: "http://two/metrics"}}},
		"missing": {Backends: []backendConfig{{Upstream: "http://one"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := predictiveVLLMMetricsURL(cfg); err == nil {
				t.Fatalf("configuration %+v unexpectedly produced a metrics URL", cfg.Backends)
			}
		})
	}
	url, err := predictiveVLLMMetricsURL(config{Backends: []backendConfig{{MetricsURL: " http://one/metrics "}}})
	if err != nil || url != "http://one/metrics" {
		t.Fatalf("single metrics URL = %q/%v", url, err)
	}
	url, err = predictiveVLLMMetricsURL(config{Backends: []backendConfig{{}}, DynamicMetricsURLs: []string{" http://fallback/metrics "}})
	if err != nil || url != "http://fallback/metrics" {
		t.Fatalf("single dynamic fallback URL = %q/%v", url, err)
	}
}

func newManualPredictiveVLLMObserver(metricsURL string, maximumKV int64, coordinator predictiveSampleCoordinator, now func() time.Time) *predictiveVLLMObserver {
	return &predictiveVLLMObserver{
		metricsURL:          metricsURL,
		modelIdentitySHA256: predictiveModelIdentitySHA256("google/gemma-4-fixture"),
		maximumKVTokens:     maximumKV,
		blockSize:           4,
		pollInterval:        time.Second,
		maximumAge:          10 * time.Second,
		preemptionCooldown:  5 * time.Second,
		coordinator:         coordinator,
		now:                 now,
		client:              &http.Client{Timeout: time.Second},
	}
}

func observerMetrics(capacity int64, usage float64, running, waiting int, preemptions uint64, includeCapacity bool) string {
	return observerMetricsWithGeneration(capacity, usage, running, waiting, preemptions, 100, includeCapacity)
}

func observerMetricsWithGeneration(capacity int64, usage float64, running, waiting int, preemptions, generation uint64, includeCapacity bool) string {
	const model = "google/gemma-4-fixture"
	var builder strings.Builder
	if includeCapacity {
		fmt.Fprintf(&builder, "vllm:cache_config_info{block_size=\"4\",kv_cache_size_tokens=\"%d\",num_gpu_blocks=\"250\"} 1\n", capacity)
	}
	fmt.Fprintf(&builder, "vllm:kv_cache_usage_perc %.6f\n", usage)
	fmt.Fprintf(&builder, "vllm:num_requests_running{model_name=%q,engine=\"0\"} %d\n", model, running)
	fmt.Fprintf(&builder, "vllm:num_requests_waiting{model_name=%q,engine=\"0\"} %d\n", model, waiting)
	fmt.Fprintf(&builder, "vllm:num_preemptions_total{model_name=%q,engine=\"0\"} %d\n", model, preemptions)
	fmt.Fprintf(&builder, "vllm:generation_tokens_total{model_name=%q,engine=\"0\"} %d\n", model, generation)
	return builder.String()
}

func observerMetricsWithModel(capacity int64, usage float64, running, waiting int, preemptions uint64, model string, includeCapacity bool) string {
	metrics := observerMetrics(capacity, usage, running, waiting, preemptions, includeCapacity)
	return strings.ReplaceAll(metrics, `model_name="google/gemma-4-fixture"`, fmt.Sprintf("model_name=%q", model))
}
