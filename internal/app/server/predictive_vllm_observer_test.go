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

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type observerMetricsFixture struct {
	mu   sync.Mutex
	body string
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

type stablePrefillTestCoordinator struct {
	mu              sync.Mutex
	sequence        uint64
	reconciliations int
	invalidations   int
	reconcileErr    error
	samples         []runtimepredictive.SampleWindow
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
	c.reconciliations++
	c.samples = append(c.samples, sample)
	return c.reconcileErr
}

func (c *stablePrefillTestCoordinator) InvalidateEpoch() bool {
	c.mu.Lock()
	c.invalidations++
	c.mu.Unlock()
	return true
}

func (c *stablePrefillTestCoordinator) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reconciliations, c.invalidations
}

func (c *stablePrefillTestCoordinator) setReconcileError(err error) {
	c.mu.Lock()
	c.reconcileErr = err
	c.mu.Unlock()
}

func (c *stablePrefillTestCoordinator) lastSample() (runtimepredictive.SampleWindow, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.samples) == 0 {
		return runtimepredictive.SampleWindow{}, false
	}
	return c.samples[len(c.samples)-1], true
}

func TestPredictiveVLLMObserverPublishesOnlyReconciledObservationSequence(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(3_500, 0)}
	coordinator := &stablePrefillTestCoordinator{}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 1, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

	observer.poll(context.Background())
	first := observer.RequestAwareInput(clock.Now())
	firstObserver := observer.Snapshot(clock.Now())
	firstSample, ok := coordinator.lastSample()
	if !ok || first.ObservationSequence != 1 || firstSample.ObservationSequence != first.ObservationSequence ||
		firstObserver.ObservationSequence != first.ObservationSequence {
		t.Fatalf("first observation input/snapshot/sample=%+v/%+v/%+v, want paired sequence 1",
			first, firstObserver, firstSample)
	}

	clock.Advance(500 * time.Millisecond)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.20, 1, 0, 0, 110, true))
	observer.poll(context.Background())
	second := observer.RequestAwareInput(clock.Now())
	secondObserver := observer.Snapshot(clock.Now())
	secondSample, _ := coordinator.lastSample()
	if second.ObservationSequence != 2 || secondSample.ObservationSequence != second.ObservationSequence ||
		secondObserver.ObservationSequence != second.ObservationSequence || second.UsedTokens != 200 {
		t.Fatalf("second observation input/snapshot/sample=%+v/%+v/%+v, want paired sequence 2",
			second, secondObserver, secondSample)
	}

	coordinator.setReconcileError(fmt.Errorf("fixture reconcile failure"))
	clock.Advance(500 * time.Millisecond)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.30, 1, 0, 0, 120, true))
	observer.poll(context.Background())
	failed := observer.RequestAwareInput(clock.Now())
	failedSample, _ := coordinator.lastSample()
	if failedSample.ObservationSequence != 3 || failed.ObservationSequence != 2 || failed.UsedTokens != 200 {
		t.Fatalf("failed reconciliation advanced publication: input/sample=%+v/%+v", failed, failedSample)
	}

	coordinator.setReconcileError(nil)
	observer.poll(context.Background())
	recovered := observer.RequestAwareInput(clock.Now())
	recoveredSample, _ := coordinator.lastSample()
	if recovered.ObservationSequence != 3 || recoveredSample.ObservationSequence != recovered.ObservationSequence || recovered.UsedTokens != 300 {
		t.Fatalf("recovered observation input/sample=%+v/%+v, want paired sequence 3", recovered, recoveredSample)
	}
}

func TestPredictiveVLLMObserverTransientScrapeExpiresButDoesNotInvalidateEpoch(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(4_000, 0)}
	coordinator := &stablePrefillTestCoordinator{}
	valid := observerMetricsWithGeneration(1_000, 0.25, 1, 0, 0, 100, true)
	fixture := &observerMetricsFixture{body: valid}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

	observer.poll(context.Background())
	if input := observer.RequestAwareInput(clock.Now()); !input.MetricsFresh || !input.IdentityValid || input.UsedTokens != 250 {
		t.Fatalf("initial observer input=%+v, want a fresh coherent sample", input)
	}
	fixture.Set(strings.Replace(valid, `vllm:generation_tokens_total{model_name="google/gemma-4-fixture",engine="0"} 100`+"\n", "", 1))
	clock.Advance(500 * time.Millisecond)
	observer.poll(context.Background())
	if input := observer.RequestAwareInput(clock.Now()); !input.MetricsFresh || !input.IdentityValid {
		t.Fatalf("one incomplete scrape discarded the last coherent sample: %+v", input)
	}
	if reconciliations, invalidations := coordinator.counts(); reconciliations != 1 || invalidations != 0 {
		t.Fatalf("transient scrape coordinator calls=%d/%d, want 1/0", reconciliations, invalidations)
	}

	clock.Advance(observer.maximumAge)
	if input := observer.RequestAwareInput(clock.Now()); input.MetricsFresh || input.IdentityValid {
		t.Fatalf("incomplete metrics did not expire by freshness: %+v", input)
	}
	fixture.Set(valid)
	observer.poll(context.Background())
	if input := observer.RequestAwareInput(clock.Now()); !input.MetricsFresh || !input.IdentityValid {
		t.Fatalf("observer did not recover after coherent metrics returned: %+v", input)
	}
}

func TestPredictiveVLLMObserverCapabilityDriftPermanentlyInvalidatesEpoch(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(5_000, 0)}
	coordinator := &stablePrefillTestCoordinator{}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 1, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

	observer.poll(context.Background())
	clock.Advance(500 * time.Millisecond)
	fixture.Set(observerMetricsWithGeneration(2_000, 0.10, 1, 0, 0, 110, true))
	observer.poll(context.Background())
	if input := observer.RequestAwareInput(clock.Now()); input.MetricsFresh || input.IdentityValid {
		t.Fatalf("capacity drift retained an authorized epoch: %+v", input)
	}
	if reconciliations, invalidations := coordinator.counts(); reconciliations != 1 || invalidations != 1 {
		t.Fatalf("capacity drift coordinator calls=%d/%d, want 1/1", reconciliations, invalidations)
	}

	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 1, 0, 0, 120, true))
	observer.poll(context.Background())
	if input := observer.RequestAwareInput(clock.Now()); input.MetricsFresh || input.IdentityValid {
		t.Fatalf("invalidated epoch recovered without adapter reconstruction: %+v", input)
	}
	if reconciliations, invalidations := coordinator.counts(); reconciliations != 1 || invalidations != 1 {
		t.Fatalf("invalidated observer continued reconciling: %d/%d", reconciliations, invalidations)
	}
}

func TestPredictiveVLLMObserverExplicitIdentityAndBlockDriftArePermanent(t *testing.T) {
	for name, mutate := range map[string]func(string) string{
		"model": func(metrics string) string {
			return strings.ReplaceAll(metrics, `model_name="google/gemma-4-fixture"`, `model_name="other/model"`)
		},
		"block": func(metrics string) string {
			return strings.Replace(metrics, `block_size="4"`, `block_size="8"`, 1)
		},
	} {
		t.Run(name, func(t *testing.T) {
			clock := &adapterTestClock{now: time.Unix(6_000, 0)}
			coordinator := &stablePrefillTestCoordinator{}
			metrics := mutate(observerMetricsWithGeneration(1_000, 0.10, 1, 0, 0, 100, true))
			fixture := &observerMetricsFixture{body: metrics}
			server := httptest.NewServer(fixture)
			defer server.Close()
			observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)

			observer.poll(context.Background())
			if input := observer.RequestAwareInput(clock.Now()); input.MetricsFresh || input.IdentityValid {
				t.Fatalf("explicit %s drift became authorized: %+v", name, input)
			}
			if reconciliations, invalidations := coordinator.counts(); reconciliations != 0 || invalidations != 1 {
				t.Fatalf("explicit %s drift calls=%d/%d, want 0/1", name, reconciliations, invalidations)
			}
		})
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
