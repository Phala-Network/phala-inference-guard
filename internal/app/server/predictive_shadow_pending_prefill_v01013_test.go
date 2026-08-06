package server

import (
	"context"
	"math"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestPredictiveShadowPendingPrefillStoreAggregatesCompatibleRisks(t *testing.T) {
	store := newPredictiveShadowPendingPrefillStore(4)
	first := compatibleShadowPrefillObservation(7, 100, 100, 120, 110, 100, 50, time.Millisecond, false)
	second := compatibleShadowPrefillObservation(7, 300, 200, 350, 250, 400, 60, 2*time.Millisecond, false)
	firstHandle := store.Begin(first)
	secondHandle := store.Begin(second)
	if firstHandle == nil || secondHandle == nil {
		t.Fatal("compatible shadow prefills were not retained")
	}

	snapshot := store.Snapshot()
	wantFeatures := runtimepredictive.SchedulerFeatures{
		ExistingDecodeSequences:         4,
		DecodeSequences:                 5,
		ExistingPendingPrefillSequences: 2,
		PendingPrefillSequences:         3,
		ExistingActiveContextTokens:     1_100,
		ExistingUncachedPrefill:         300,
		ExistingPhysicalKVUpper:         1_320,
		ExistingActiveKVUpper:           1_210,
		RequestComplexityTokensUpper:    400,
		ActiveContextTokens:             1_300,
		UncachedPrefillTokens:           600,
		AccruedLocalAdmissionLatency:    2 * time.Millisecond,
		PhysicalKVUpper:                 1_670,
		ActiveKVUpper:                   1_460,
		DecodeHorizonUpper:              60,
	}
	if snapshot.Count != 2 || snapshot.Tokens != 400 || !snapshot.FeaturesValid ||
		snapshot.DecisionManagerSequence != 7 || !snapshot.Exploratory ||
		snapshot.AttributionState != predictiveShadowPrefillAttributionAggregate ||
		!reflect.DeepEqual(snapshot.Features, wantFeatures) {
		t.Fatalf("compatible aggregate snapshot = %+v, want features %+v", snapshot, wantFeatures)
	}

	if !secondHandle.End() {
		t.Fatal("latest compatible shadow prefill did not end")
	}
	snapshot = store.Snapshot()
	if snapshot.Count != 1 || snapshot.Tokens != first.Tokens || !snapshot.FeaturesValid ||
		snapshot.DecisionManagerSequence != first.DecisionManagerSequence || snapshot.Exploratory != first.Exploratory ||
		snapshot.AttributionState != predictiveShadowPrefillAttributionSingle ||
		!reflect.DeepEqual(snapshot.Features, first.Features) {
		t.Fatalf("single survivor did not recover its exact feature vector: %+v", snapshot)
	}
	if secondHandle.End() {
		t.Fatal("duplicate shadow prefill end succeeded")
	}
	if !firstHandle.End() {
		t.Fatal("first compatible shadow prefill did not end")
	}
	if snapshot := store.Snapshot(); snapshot.Count != 0 || snapshot.Tokens != 0 || snapshot.FeaturesValid ||
		snapshot.AttributionState != predictiveShadowPrefillAttributionEmpty {
		t.Fatalf("empty aggregate store snapshot = %+v", snapshot)
	}
}

func TestPredictiveShadowPendingPrefillStorePreservesSingleObservationContract(t *testing.T) {
	store := newPredictiveShadowPendingPrefillStore(2)
	observation := runtimepredictive.PendingPrefillObservation{
		Tokens:                  100,
		DecisionManagerSequence: 0,
		Exploratory:             true,
		Features: runtimepredictive.SchedulerFeatures{
			DecodeSequences:         1,
			PendingPrefillSequences: 1,
			UncachedPrefillTokens:   100,
		},
	}
	handle := store.Begin(observation)
	if handle == nil {
		t.Fatal("single shadow observation was not retained")
	}
	snapshot := store.Snapshot()
	if snapshot.Count != 1 || snapshot.Tokens != observation.Tokens || !snapshot.FeaturesValid ||
		snapshot.DecisionManagerSequence != 0 || !snapshot.Exploratory ||
		snapshot.AttributionState != predictiveShadowPrefillAttributionSingle ||
		!reflect.DeepEqual(snapshot.Features, observation.Features) {
		t.Fatalf("single shadow contract changed: %+v", snapshot)
	}
}

func TestPredictiveShadowPendingPrefillStoreAggregatesThreeAndRecomputesSurvivors(t *testing.T) {
	store := newPredictiveShadowPendingPrefillStore(4)
	first := compatibleShadowPrefillObservation(0, 100, 100, 120, 110, 100, 50, time.Millisecond, false)
	second := compatibleShadowPrefillObservation(0, 300, 200, 350, 250, 400, 60, 2*time.Millisecond, false)
	third := compatibleShadowPrefillObservation(0, 50, 80, 90, 85, 90, 70, 3*time.Millisecond, false)
	firstHandle := store.Begin(first)
	secondHandle := store.Begin(second)
	thirdHandle := store.Begin(third)
	if firstHandle == nil || secondHandle == nil || thirdHandle == nil {
		t.Fatal("three compatible shadow observations were not retained")
	}

	wantThree := third.Features
	wantThree.ExistingDecodeSequences = 5
	wantThree.DecodeSequences = 6
	wantThree.ExistingPendingPrefillSequences = 3
	wantThree.PendingPrefillSequences = 4
	wantThree.ExistingActiveContextTokens = 1_300
	wantThree.ActiveContextTokens = 1_380
	wantThree.ExistingUncachedPrefill = 600
	wantThree.UncachedPrefillTokens = 650
	wantThree.ExistingPhysicalKVUpper = 1_670
	wantThree.PhysicalKVUpper = 1_760
	wantThree.ExistingActiveKVUpper = 1_460
	wantThree.ActiveKVUpper = 1_545
	assertCompatibleShadowAggregate(t, store.Snapshot(), 3, 450, wantThree)

	if !secondHandle.End() {
		t.Fatal("earlier shadow observation did not end")
	}
	wantTwo := third.Features
	wantTwo.ExistingDecodeSequences = 4
	wantTwo.DecodeSequences = 5
	wantTwo.ExistingPendingPrefillSequences = 2
	wantTwo.PendingPrefillSequences = 3
	wantTwo.ExistingActiveContextTokens = 1_100
	wantTwo.ActiveContextTokens = 1_180
	wantTwo.ExistingUncachedPrefill = 300
	wantTwo.UncachedPrefillTokens = 350
	wantTwo.ExistingPhysicalKVUpper = 1_320
	wantTwo.PhysicalKVUpper = 1_410
	wantTwo.ExistingActiveKVUpper = 1_210
	wantTwo.ActiveKVUpper = 1_295
	assertCompatibleShadowAggregate(t, store.Snapshot(), 2, 150, wantTwo)

	if !thirdHandle.End() {
		t.Fatal("latest shadow observation did not end")
	}
	snapshot := store.Snapshot()
	if snapshot.Count != 1 || snapshot.Tokens != first.Tokens || !snapshot.FeaturesValid ||
		snapshot.AttributionState != predictiveShadowPrefillAttributionSingle || snapshot.Exploratory != first.Exploratory ||
		!reflect.DeepEqual(snapshot.Features, first.Features) {
		t.Fatalf("ending latest did not recover exact earlier survivor: %+v", snapshot)
	}
}

func TestPredictiveShadowPendingPrefillStoreCensorsIncompatibleAggregates(t *testing.T) {
	base := compatibleShadowPrefillObservation(7, 100, 100, 120, 110, 100, 50, time.Millisecond, false)
	for _, test := range []struct {
		name   string
		mutate func(*runtimepredictive.PendingPrefillObservation)
	}{
		{name: "manager_sequence", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.DecisionManagerSequence++
		}},
		{name: "decode_base", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.ExistingDecodeSequences++
			observation.Features.DecodeSequences++
		}},
		{name: "pending_base", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.ExistingPendingPrefillSequences = 0
			observation.Features.PendingPrefillSequences = 1
		}},
		{name: "context_base", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.ExistingActiveContextTokens++
			observation.Features.ActiveContextTokens++
		}},
		{name: "uncached_base", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.ExistingUncachedPrefill++
			observation.Features.UncachedPrefillTokens++
		}},
		{name: "physical_kv_base", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.ExistingPhysicalKVUpper++
			observation.Features.PhysicalKVUpper++
		}},
		{name: "active_kv_base", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.ExistingActiveKVUpper++
			observation.Features.ActiveKVUpper++
		}},
		{name: "uncached_delta", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.UncachedPrefillTokens++
		}},
		{name: "negative_latency", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.AccruedLocalAdmissionLatency = -time.Nanosecond
		}},
		{name: "request_complexity", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.RequestComplexityTokensUpper = observation.Tokens - 1
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := newPredictiveShadowPendingPrefillStore(2)
			other := base
			test.mutate(&other)
			if store.Begin(base) == nil || store.Begin(other) == nil {
				t.Fatal("incompatible observations were not retained for accounting")
			}
			snapshot := store.Snapshot()
			if snapshot.Count != 2 || snapshot.Tokens != base.Tokens+other.Tokens || snapshot.FeaturesValid ||
				snapshot.DecisionManagerSequence != 0 || snapshot.Exploratory ||
				snapshot.AttributionState != predictiveShadowPrefillAttributionIncompatible {
				t.Fatalf("incompatible aggregate was attributed: %+v", snapshot)
			}
		})
	}
}

func TestPredictiveShadowPendingPrefillStoreFailsClosedOnArithmeticOverflow(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*runtimepredictive.PendingPrefillObservation)
	}{
		{name: "sequence_count", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.ExistingDecodeSequences = math.MaxInt - 1
			observation.Features.DecodeSequences = math.MaxInt
		}},
		{name: "active_context", mutate: func(observation *runtimepredictive.PendingPrefillObservation) {
			observation.Features.ExistingActiveContextTokens = math.MaxInt64 - 10
			observation.Features.ActiveContextTokens = math.MaxInt64 - 2
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := compatibleShadowPrefillObservation(7, 1, 8, 8, 8, 1, 0, 0, false)
			second := first
			test.mutate(&first)
			test.mutate(&second)
			store := newPredictiveShadowPendingPrefillStore(2)
			if store.Begin(first) == nil || store.Begin(second) == nil {
				t.Fatal("overflow fixtures were not retained")
			}
			if snapshot := store.Snapshot(); snapshot.FeaturesValid || snapshot.AttributionState != predictiveShadowPrefillAttributionIncompatible {
				t.Fatalf("overflow aggregate did not fail closed: %+v", snapshot)
			}
		})
	}
}

func TestPredictiveShadowPendingPrefillStoreLifecycleBoundAndSequence(t *testing.T) {
	store := newPredictiveShadowPendingPrefillStore(2)
	observation := compatibleShadowPrefillObservation(1, 1, 1, 1, 1, 1, 0, 0, false)
	first := store.Begin(observation)
	second := store.Begin(observation)
	if first == nil || second == nil || store.Begin(observation) != nil {
		t.Fatal("shadow store capacity bound was not exact")
	}
	if snapshot := store.Snapshot(); snapshot.EventSequence != 2 {
		t.Fatalf("begin event sequence = %d, want 2", snapshot.EventSequence)
	}
	if !first.End() || first.End() {
		t.Fatal("shadow end was not idempotent")
	}
	if snapshot := store.Snapshot(); snapshot.EventSequence != 3 || snapshot.Count != 1 {
		t.Fatalf("end event state = %+v", snapshot)
	}
	if cleared := store.Clear(); cleared != 1 {
		t.Fatalf("clear count = %d, want 1", cleared)
	}
	if snapshot := store.Snapshot(); snapshot.EventSequence != 4 || snapshot.Count != 0 ||
		snapshot.AttributionState != predictiveShadowPrefillAttributionEmpty {
		t.Fatalf("clear event state = %+v", snapshot)
	}
	if store.Clear() != 0 || store.Snapshot().EventSequence != 4 || second.End() {
		t.Fatal("empty clear or stale handle changed lifecycle state")
	}
	if store.Begin(observation) == nil || store.Snapshot().EventSequence != 5 {
		t.Fatal("store did not recover after bounded clear")
	}
}

func TestPredictiveShadowPendingPrefillStoreFailsClosedWhenEventSequenceExhausts(t *testing.T) {
	store := newPredictiveShadowPendingPrefillStore(2)
	store.eventSequence = ^uint64(0) - 1
	observation := compatibleShadowPrefillObservation(1, 1, 1, 1, 1, 1, 0, 0, false)
	handle := store.Begin(observation)
	if handle == nil || store.Snapshot().EventSequence != ^uint64(0) {
		t.Fatal("last unambiguous shadow episode was not retained")
	}
	if store.Begin(observation) != nil {
		t.Fatal("shadow episode sequence wrapped instead of failing closed")
	}
	if snapshot := store.Snapshot(); snapshot.FeaturesValid || snapshot.AttributionState != predictiveShadowPrefillAttributionIncompatible {
		t.Fatalf("exhausted non-empty store remained attributable: %+v", snapshot)
	}
	if !handle.End() {
		t.Fatal("sequence exhaustion leaked active lifecycle state")
	}
	if snapshot := store.Snapshot(); snapshot.Count != 0 || snapshot.AttributionState != predictiveShadowPrefillAttributionEmpty {
		t.Fatalf("exhausted store did not reach terminal zero: %+v", snapshot)
	}
	if store.Begin(observation) != nil {
		t.Fatal("exhausted store reused an ambiguous episode identity")
	}
}

func TestObservedPredictivePendingPrefillsAcceptsExactMixedManagerShadowAggregate(t *testing.T) {
	store := newPredictiveShadowPendingPrefillStore(2)
	if store.Begin(compatibleShadowPrefillObservation(7, 100, 100, 120, 110, 100, 50, time.Millisecond, false)) == nil ||
		store.Begin(compatibleShadowPrefillObservation(7, 300, 200, 350, 250, 400, 60, 2*time.Millisecond, false)) == nil {
		t.Fatal("mixed aggregate shadow observations were not retained")
	}
	manager := runtimepredictive.Snapshot{
		ForwardedPendingPrefills:      1,
		ForwardedPendingPrefillTokens: 200,
		EventSequence:                 7,
	}
	observed := observedPredictivePendingPrefills(manager, store.Snapshot())
	if observed.Count != 3 || observed.Tokens != 600 || !observed.FeaturesValid || !observed.FromShadow ||
		observed.DecisionManagerSequence != 7 || !observed.Exploratory || observed.Features.PendingPrefillSequences != 3 ||
		observed.Features.UncachedPrefillTokens != 600 || !observed.Episode.valid() {
		t.Fatalf("exact mixed Manager/shadow aggregate = %+v", observed)
	}
	manager.ForwardedPendingPrefillTokens++
	if mismatch := observedPredictivePendingPrefills(manager, store.Snapshot()); mismatch.FeaturesValid {
		t.Fatalf("mixed aggregate token mismatch was attributed: %+v", mismatch)
	}
	manager.ForwardedPendingPrefillTokens--
	manager.ForwardedPendingPrefills++
	if mismatch := observedPredictivePendingPrefills(manager, store.Snapshot()); mismatch.FeaturesValid {
		t.Fatalf("mixed aggregate count mismatch was attributed: %+v", mismatch)
	}
	manager.ForwardedPendingPrefills--
	manager.EventSequence++
	if changed := observedPredictivePendingPrefills(manager, store.Snapshot()); changed.FeaturesValid {
		t.Fatalf("mixed aggregate Manager-sequence change was attributed: %+v", changed)
	}
}

func TestObservedPredictivePendingPrefillsPreservesExactMixedManagerSingleShadow(t *testing.T) {
	store := newPredictiveShadowPendingPrefillStore(1)
	observation := compatibleShadowPrefillObservation(7, 100, 100, 120, 110, 100, 50, time.Millisecond, false)
	if store.Begin(observation) == nil {
		t.Fatal("mixed single-shadow observation was not retained")
	}
	manager := runtimepredictive.Snapshot{
		ForwardedPendingPrefills:      1,
		ForwardedPendingPrefillTokens: 200,
		EventSequence:                 7,
	}
	observed := observedPredictivePendingPrefills(manager, store.Snapshot())
	if observed.Count != 2 || observed.Tokens != 300 || !observed.FeaturesValid || !observed.FromShadow ||
		observed.Exploratory != observation.Exploratory || !reflect.DeepEqual(observed.Features, observation.Features) {
		t.Fatalf("exact mixed Manager/single-shadow attribution = %+v", observed)
	}
}

func TestPredictiveVLLMObserverLearnsOncePerCompatibleShadowAggregateEpisode(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(90_210, 0)}
	fixture := &observerMetricsFixture{body: observerMetricsWithGeneration(1_000, 0.10, 3, 0, 0, 100, true)}
	server := httptest.NewServer(fixture)
	defer server.Close()
	coordinator := &stablePrefillTestCoordinator{}
	coordinator.Set(9, 0, 0)
	learner := &recordingExistingPrefillLearner{}
	store := newPredictiveShadowPendingPrefillStore(4)
	beginEpisode := func() (*predictiveShadowPendingPrefillHandle, *predictiveShadowPendingPrefillHandle) {
		first := store.Begin(observerShadowPrefillObservation(9, 100, 120, false))
		second := store.Begin(observerShadowPrefillObservation(9, 200, 230, false))
		if first == nil || second == nil {
			t.Fatal("compatible observer episode was not retained")
		}
		return first, second
	}
	first, second := beginEpisode()
	observer := newManualPredictiveVLLMObserver(server.URL, 1_000, coordinator, clock.Now)
	observer.coordinatorSnapshot = coordinator
	observer.existingPrefillLearner = learner
	observer.shadowPendingPrefills = store

	observer.poll(context.Background())
	clock.Advance(time.Second)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 3, 0, 0, 160, true))
	observer.poll(context.Background())
	if outcomes := learner.Outcomes(); len(outcomes) != 1 || outcomes[0].PendingPrefillSequences != 2 ||
		outcomes[0].PendingPrefillTokens != 300 || outcomes[0].ExistingDecodeSequences != 1 ||
		outcomes[0].ExistingUserTPS != 60 || !outcomes[0].Exploratory {
		t.Fatalf("first compatible aggregate outcomes = %+v", outcomes)
	}
	if stats := observer.ExistingPrefillTelemetry(); stats.Accepted != 1 || stats.Deduplicated != 0 || !stats.LastExploratory {
		t.Fatalf("first compatible aggregate telemetry = %+v", stats)
	}

	clock.Advance(time.Second)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 3, 0, 0, 220, true))
	observer.poll(context.Background())
	if stats := observer.ExistingPrefillTelemetry(); stats.Accepted != 1 || stats.Deduplicated != 1 {
		t.Fatalf("stable repeat was not deduplicated: %+v", stats)
	}

	if !first.End() || !second.End() {
		t.Fatal("first aggregate episode did not terminate")
	}
	first, second = beginEpisode()
	clock.Advance(time.Second)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 3, 0, 0, 280, true))
	observer.poll(context.Background())
	clock.Advance(time.Second)
	fixture.Set(observerMetricsWithGeneration(1_000, 0.10, 3, 0, 0, 340, true))
	observer.poll(context.Background())
	if outcomes := learner.Outcomes(); len(outcomes) != 2 {
		t.Fatalf("two distinct aggregate episodes outcomes = %+v", outcomes)
	}
	if stats := observer.ExistingPrefillTelemetry(); stats.Accepted != 2 || stats.Deduplicated != 1 {
		t.Fatalf("two distinct aggregate episodes telemetry = %+v", stats)
	}
	if !first.End() || !second.End() || store.Snapshot().Count != 0 {
		t.Fatal("second aggregate episode did not reach terminal zero")
	}
}

func assertCompatibleShadowAggregate(t *testing.T, snapshot predictiveShadowPendingPrefillSnapshot, count int, tokens int64, features runtimepredictive.SchedulerFeatures) {
	t.Helper()
	if snapshot.Count != count || snapshot.Tokens != tokens || !snapshot.FeaturesValid ||
		snapshot.AttributionState != predictiveShadowPrefillAttributionAggregate || !snapshot.Exploratory ||
		!reflect.DeepEqual(snapshot.Features, features) {
		t.Fatalf("compatible shadow aggregate = %+v, want features %+v", snapshot, features)
	}
}

func observerShadowPrefillObservation(managerSequence uint64, tokens, activeContextDelta int64, exploratory bool) runtimepredictive.PendingPrefillObservation {
	return runtimepredictive.PendingPrefillObservation{
		Tokens: tokens,
		Features: runtimepredictive.SchedulerFeatures{
			ExistingDecodeSequences:         1,
			DecodeSequences:                 2,
			ExistingPendingPrefillSequences: 0,
			PendingPrefillSequences:         1,
			ExistingActiveContextTokens:     1_000,
			ExistingUncachedPrefill:         0,
			ExistingPhysicalKVUpper:         1_000,
			ExistingActiveKVUpper:           1_000,
			RequestComplexityTokensUpper:    tokens,
			ActiveContextTokens:             1_000 + activeContextDelta,
			UncachedPrefillTokens:           tokens,
			PhysicalKVUpper:                 1_000 + activeContextDelta,
			ActiveKVUpper:                   1_000 + activeContextDelta,
			DecodeHorizonUpper:              activeContextDelta - tokens,
		},
		DecisionManagerSequence: managerSequence,
		Exploratory:             exploratory,
	}
}

func compatibleShadowPrefillObservation(
	managerSequence uint64,
	tokens int64,
	activeContextDelta int64,
	physicalKVDelta int64,
	activeKVDelta int64,
	requestComplexity int64,
	decodeHorizon int64,
	latency time.Duration,
	exploratory bool,
) runtimepredictive.PendingPrefillObservation {
	return runtimepredictive.PendingPrefillObservation{
		Tokens: tokens,
		Features: runtimepredictive.SchedulerFeatures{
			ExistingDecodeSequences:         3,
			DecodeSequences:                 4,
			ExistingPendingPrefillSequences: 1,
			PendingPrefillSequences:         2,
			ExistingActiveContextTokens:     1_000,
			ExistingUncachedPrefill:         200,
			ExistingPhysicalKVUpper:         1_200,
			ExistingActiveKVUpper:           1_100,
			RequestComplexityTokensUpper:    requestComplexity,
			ActiveContextTokens:             1_000 + activeContextDelta,
			UncachedPrefillTokens:           200 + tokens,
			AccruedLocalAdmissionLatency:    latency,
			PhysicalKVUpper:                 1_200 + physicalKVDelta,
			ActiveKVUpper:                   1_100 + activeKVDelta,
			DecodeHorizonUpper:              decodeHorizon,
		},
		DecisionManagerSequence: managerSequence,
		Exploratory:             exploratory,
	}
}
