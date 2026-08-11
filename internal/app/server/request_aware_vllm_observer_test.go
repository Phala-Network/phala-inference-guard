package server

import (
	"context"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestRequestAwareObserverStartupProbeIsTheFirstTPSBaseline(t *testing.T) {
	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(requestStarted)
		<-releaseRequest
		_, _ = w.Write([]byte(observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 120, true)))
	}))
	defer server.Close()
	observedAt := time.Unix(99_000, 0)
	observer, err := newPredictiveVLLMObserver(predictiveVLLMObserverConfig{
		MetricsURL:      server.URL,
		ServedModel:     "google/gemma-4-fixture",
		MaximumKVTokens: 1_000,
		BlockSize:       4,
		PollInterval:    time.Hour,
		MaximumAge:      2 * time.Hour,
		RequestTimeout:  time.Hour,
		Coordinator:     &stablePrefillTestCoordinator{},
		Initial: predictiveVLLMStartup{
			ModelIdentitySHA256: predictiveModelIdentitySHA256("google/gemma-4-fixture"),
			CapacityTokens:      1_000,
			BlockSize:           4,
			UsedTokens:          100,
			Running:             2,
			Generation:          100,
			ObservedAt:          observedAt,
		},
	})
	if err != nil {
		close(releaseRequest)
		t.Fatalf("new observer: %v", err)
	}
	<-requestStarted
	observer.mu.Lock()
	hasBaseline := observer.requestAwareHasBaseline
	baselineObservedAt := observer.requestAwareObservedAt
	baselineGeneration := observer.requestAwareGeneration
	initialObservationSequence := observer.requestAwareInput.ObservationSequence
	observer.mu.Unlock()
	close(releaseRequest)
	defer observer.Close()
	if !hasBaseline || baselineObservedAt != observedAt || baselineGeneration != 100 || initialObservationSequence != 0 {
		t.Fatalf("startup request-aware baseline=%t/%s/%d sequence=%d, want true/%s/100/0",
			hasBaseline, baselineObservedAt, baselineGeneration, initialObservationSequence, observedAt)
	}
}

func TestRequestAwareObserverPublishesFreshTPSWithoutWaitingBlindSpot(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(100_000, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 4, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, &stablePrefillTestCoordinator{}, clock.Now)
	observer.poll(context.Background())
	first := observer.RequestAwareInput(clock.Now())
	if !first.MetricsFresh || !first.IdentityValid || first.CapacityTokens != 1_000 || first.Running != 4 || first.TPSValid {
		t.Fatalf("first request-aware snapshot=%+v", first)
	}

	clock.Advance(500 * time.Millisecond)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.20, 4, 1, 0, 120, true))
	observer.poll(context.Background())
	second := observer.RequestAwareInput(clock.Now())
	if !second.MetricsFresh || !second.IdentityValid || second.Running != 4 || second.Waiting != 1 ||
		!second.TPSValid || math.Abs(second.AggregateTPSProxy-40) > 1e-9 || math.Abs(second.MeanActiveTPSProxy-10) > 1e-9 {
		t.Fatalf("second request-aware snapshot=%+v, want waiting=1 aggregate TPS=40 mean TPS=10", second)
	}
}

func TestRequestAwareObserverZeroGenerationIsUnknownNotTPSFloor(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(101_000, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 1, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, &stablePrefillTestCoordinator{}, clock.Now)
	observer.poll(context.Background())
	clock.Advance(500 * time.Millisecond)
	observer.poll(context.Background())
	input := observer.RequestAwareInput(clock.Now())
	if !input.MetricsFresh || input.TPSValid || input.AggregateTPSProxy != 0 || input.MeanActiveTPSProxy != 0 {
		t.Fatalf("zero-generation request-aware snapshot=%+v, want fresh TPS unknown", input)
	}
}

func TestRequestAwareObserverPublishesFreshCompletionWindowTPSWithNoCurrentRunning(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(101_500, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 1, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, &stablePrefillTestCoordinator{}, clock.Now)
	observer.poll(context.Background())

	clock.Advance(500 * time.Millisecond)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 0, 0, 0, 110, true))
	observer.poll(context.Background())
	input := observer.RequestAwareInput(clock.Now())
	if !input.MetricsFresh || !input.IdentityValid || input.Running != 0 || !input.TPSValid ||
		math.Abs(input.AggregateTPSProxy-20) > 1e-9 || math.Abs(input.MeanActiveTPSProxy-20) > 1e-9 {
		t.Fatalf("fresh completion-window request-aware snapshot=%+v, want current running=0 and TPS proxy=20", input)
	}
}

func TestRequestAwareObserverPreemptionProtectionClearsOnNextFreshObservation(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(102_000, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 2, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, &stablePrefillTestCoordinator{}, clock.Now)
	observer.poll(context.Background())
	clock.Advance(500 * time.Millisecond)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 2, 0, 1, 110, true))
	observer.poll(context.Background())
	if input := observer.RequestAwareInput(clock.Now()); !input.MetricsFresh || !input.PreemptionObserved || input.TPSValid {
		t.Fatalf("preemption request-aware snapshot=%+v", input)
	}

	clock.Advance(500 * time.Millisecond)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 2, 0, 1, 120, true))
	observer.poll(context.Background())
	if input := observer.RequestAwareInput(clock.Now()); !input.MetricsFresh || input.PreemptionObserved {
		t.Fatalf("next fresh observation retained preemption protection=%+v", input)
	}
	clock.Advance(11 * time.Second)
	if input := observer.RequestAwareInput(clock.Now()); input.MetricsFresh {
		t.Fatalf("stale request-aware snapshot=%+v", input)
	}
}

func TestRequestAwareObserverSameIdentityCounterResetInvalidatesCapabilityEpoch(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(103_000, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 1, 0, 0, 500, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	manager := runtimepredictive.NewManager(
		"request-aware-reset-test",
		domainpredictive.VirtualState{
			PhysicalKVUpper:     100,
			ActiveKVUpper:       100,
			DecodeSequences:     1,
			ActiveContextTokens: 100,
		},
	)
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, manager, clock.Now)
	observer.poll(context.Background())
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		HardKVLimitTokens:            900,
		BlockSize:                    4,
		MaximumAdmissibleInputTokens: 644,
		PrefillRegularTokens:         runtimepredictive.DefaultRequestAwarePrefillRegularTokens,
		PrefillExclusiveTokens:       runtimepredictive.DefaultRequestAwarePrefillExclusiveTokens,
		PrefillQuiescentTokens:       runtimepredictive.DefaultRequestAwarePrefillQuiescentTokens,
		PrefillContendedBudgetTokens: 644,
		PrefillAggregateBudgetTokens: runtimepredictive.DefaultRequestAwarePrefillAggregateBudgetTokens,
	})
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	result := manager.DecideRequestAwareAndReserve(
		clock.Now(),
		"old-epoch-request",
		domainpredictive.RequestCost{
			ManifestID:               "request-aware-reset-test",
			InputTokens:              40,
			KV:                       domainpredictive.KVIncrement{PhysicalKVUpper: 48, ActiveKVUpper: 48},
			FutureKV:                 domainpredictive.KVIncrement{PhysicalKVUpper: 8, ActiveKVUpper: 8},
			UncachedPrefillUpper:     40,
			DecodeHorizonUpper:       8,
			DecodeSequencesUpper:     1,
			ActiveContextTokensUpper: 48,
			FutureContextTokensUpper: 8,
			Confidence:               1,
		},
		40,
		policy,
		runtimepredictive.RequestAwareInput{
			MetricsFresh:       true,
			IdentityValid:      true,
			CapacityTokens:     1_000,
			Running:            1,
			EffectiveSequences: 1,
		},
	)
	if !result.Reserved {
		t.Fatalf("old-epoch setup reservation=%+v", result)
	}

	clock.Advance(500 * time.Millisecond)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.20, 0, 0, 0, 0, true))
	observer.poll(context.Background())
	input := observer.RequestAwareInput(clock.Now())
	snapshot := manager.Snapshot()
	if input.MetricsFresh || input.IdentityValid || input.TPSValid {
		t.Fatalf("counter-reset request-aware input=%+v, want invalid capability epoch", input)
	}
	if snapshot.IntakeOpen || snapshot.Reservations != 1 {
		t.Fatalf("counter-reset manager snapshot=%+v, want closed intake with owned old reservation", snapshot)
	}
	if manager.MarkForwarded("old-epoch-request") {
		t.Fatal("old-epoch reservation forwarded after capability invalidation")
	}
}
