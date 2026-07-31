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

func newBlockingSampleCoordinator(delegate predictiveSampleCoordinator) *blockingSampleCoordinator {
	return &blockingSampleCoordinator{
		delegate: delegate,
		entered:  make(chan struct{}),
		release:  make(chan struct{}),
	}
}

func (c *blockingSampleCoordinator) StartSampleWindow() (uint64, domainpredictive.VirtualState) {
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

func TestPredictiveVLLMObserverFailsClosedOnCapacityAndTokenMetricMismatch(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(4_000, 0)}
	coordinator := newAdapterTestCoordinatorWithTPSTarget(t, 0)
	fixture := &observerMetricsFixture{body: observerMetrics(2_000, 0.25, 1, 0, 0, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) {
		t.Fatal("capacity-mismatched vLLM sample became healthy")
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 0 {
		t.Fatalf("capacity mismatch mutated predictive state: %+v", snapshot)
	}

	fixture.Set(observerMetrics(1_000, 0.25, 1, 0, 0, false))
	observer.poll(context.Background())
	if observer.Healthy(clock.Now()) {
		t.Fatal("sample without KV token capacity became healthy")
	}

	fixture.Set(observerMetrics(1_000, 0.25, 1, 0, 0, true))
	observer.poll(context.Background())
	if !observer.Healthy(clock.Now()) {
		t.Fatal("valid exact-capacity vLLM sample did not become healthy")
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 250 {
		t.Fatalf("valid sample physical KV = %+v, want 250 tokens", snapshot.Manager.Virtual)
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
	if !observer.Healthy(clock.Now()) {
		t.Fatal("new stable post-reset baseline did not restore health")
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 200 {
		t.Fatalf("post-reset stable sample did not reconcile: %+v", snapshot.Manager.Virtual)
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

func TestPredictiveVLLMObserverSampleWindowDoesNotDoubleCountConcurrentReservation(t *testing.T) {
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
	if snapshot.Manager.Virtual.Lower.PhysicalKVUpper != 0 || snapshot.Manager.Virtual.Upper.PhysicalKVUpper != 20 {
		t.Fatalf("scrape-window reservation interval = %+v, want [0,20] without double count", snapshot.Manager.Virtual)
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
		MaximumKVTokens:    1_000,
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
		metricsURL:         metricsURL,
		maximumKVTokens:    maximumKV,
		pollInterval:       time.Second,
		maximumAge:         10 * time.Second,
		preemptionCooldown: 5 * time.Second,
		coordinator:        coordinator,
		now:                now,
		client:             &http.Client{Timeout: time.Second},
	}
}

func observerMetrics(capacity int64, usage float64, running, waiting int, preemptions uint64, includeCapacity bool) string {
	var builder strings.Builder
	if includeCapacity {
		fmt.Fprintf(&builder, "vllm:cache_config_info{block_size=\"4\",kv_cache_size_tokens=\"%d\",num_gpu_blocks=\"250\"} 1\n", capacity)
	}
	fmt.Fprintf(&builder, "vllm:kv_cache_usage_perc %.6f\n", usage)
	fmt.Fprintf(&builder, "vllm:num_requests_running %d\n", running)
	fmt.Fprintf(&builder, "vllm:num_requests_waiting %d\n", waiting)
	fmt.Fprintf(&builder, "vllm:num_preemptions_total %d\n", preemptions)
	return builder.String()
}
