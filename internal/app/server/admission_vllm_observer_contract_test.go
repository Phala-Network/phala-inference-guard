package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestAdmissionVLLMObserverTransientFailureStalesThenRecoversAndDriftCloses(t *testing.T) {
	const modelName = "vendor/observer-model"
	var mode atomic.Int32
	var calls atomic.Int64
	var generation atomic.Uint64
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		switch mode.Load() {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		case 3:
			writeAdmissionVLLMMetrics(w, "vendor/drifted-model", generation.Load())
			return
		default:
			writeAdmissionVLLMMetrics(w, modelName, generation.Load())
		}
	}))
	defer metrics.Close()

	fingerprint := predictiveModelIdentitySHA256(modelName)
	profile, err := runtimepredictive.NewBackendCapabilityProfile(runtimepredictive.CapabilityProfileInput{
		ModelIdentitySHA256: fingerprint,
		KVCapacityTokens:    1_000_000, KVBlockSize: 64, KVHardRatio: 0.9,
		MaxModelLen: 650 * 1024, Source: runtimepredictive.CapabilityProfileAutomatic,
	})
	if err != nil {
		t.Fatalf("construct observer capability: %v", err)
	}
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		Capability:  admissionCapabilityFromProfile(profile),
		WorkProfile: mustPredictiveRequestWorkProfile(t, "vllm"),
	})
	if err != nil {
		t.Fatalf("construct observer Controller: %v", err)
	}
	defer controller.Close()
	clock := &manualTestClock{now: time.Unix(200, 0)}
	observer, err := newAdmissionBackendObserver(admissionBackendObserverConfig{
		BackendKind: "vllm", MetricsURL: metrics.URL, CapabilityFingerprint: fingerprint,
		MaxModelLenTokens: profile.MaxModelLenTokens, KVCapacityTokens: profile.KVCapacityTokens,
		KVBlockSize: profile.KVBlockSize, PollInterval: 20 * time.Millisecond,
		MaximumAge: 60 * time.Millisecond, RequestTimeout: 100 * time.Millisecond,
		Controller: controller, Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("construct admission observer: %v", err)
	}
	defer observer.Close()

	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return snapshot.HasObservation && snapshot.Available && snapshot.ObservationSequence >= 1
	})
	initialSequence := controller.Snapshot(clock.Now()).ObservationSequence

	mode.Store(1)
	failureCalls := calls.Load()
	clock.Advance(100 * time.Millisecond)
	waitAdmissionCondition(t, func() bool { return calls.Load() > failureCalls })
	stale := controller.Snapshot(clock.Now())
	if stale.IntakeOpen == false || stale.Available || stale.MinimumDecision.Reason != coreadmission.ReasonObservationStale ||
		stale.ObservationSequence != initialSequence {
		t.Fatalf("transient scrape failure did not produce recoverable staleness: %+v", stale)
	}

	mode.Store(2)
	generation.Store(10)
	clock.Advance(time.Millisecond)
	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return snapshot.Available && snapshot.ObservationSequence > initialSequence
	})
	recovered := controller.Snapshot(clock.Now())
	if !recovered.IntakeOpen || !recovered.Available || recovered.State.RawRunning != 0 || recovered.State.RawWaiting != 0 {
		t.Fatalf("low-flow recovery self-locked admission: %+v", recovered)
	}

	mode.Store(3)
	clock.Advance(time.Millisecond)
	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return !snapshot.IntakeOpen && snapshot.MinimumDecision.Reason == coreadmission.ReasonCapabilityDrift
	})
	drifted := controller.Snapshot(clock.Now())
	if drifted.IntakeOpen || drifted.MinimumDecision.Scope != coreadmission.ProtectionAvailability {
		t.Fatalf("identity drift did not permanently close admission: %+v", drifted)
	}
}

func TestAdmissionVLLMObserverRevalidatesMetadataBeforeCounterResetRecovery(t *testing.T) {
	const modelName = "vendor/reset-model"
	const originalMaxModelLen int64 = 650 * 1024
	var generation atomic.Uint64
	var metadataMode atomic.Int32
	var metadataMaxModelLen atomic.Int64
	var metadataCalls atomic.Int64
	var runtimeStart atomic.Int64
	generation.Store(10)
	metadataMaxModelLen.Store(originalMaxModelLen)
	runtimeStart.Store(100)
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/metrics":
			writeAdmissionVLLMMetrics(response, modelName, generation.Load())
			_, _ = fmt.Fprintf(response, "process_start_time_seconds %d\n", runtimeStart.Load())
		case "/v1/models":
			metadataCalls.Add(1)
			if metadataMode.Load() == 1 {
				response.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprintf(
				response,
				`{"object":"list","data":[{"id":%q,"max_model_len":%d}]}`,
				modelName,
				metadataMaxModelLen.Load(),
			)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	fingerprint := predictiveModelIdentitySHA256(modelName)
	profile, err := runtimepredictive.NewBackendCapabilityProfile(runtimepredictive.CapabilityProfileInput{
		ModelIdentitySHA256: fingerprint,
		KVCapacityTokens:    1_000_000,
		KVBlockSize:         64,
		KVHardRatio:         0.9,
		MaxModelLen:         originalMaxModelLen,
		Source:              runtimepredictive.CapabilityProfileAutomatic,
	})
	if err != nil {
		t.Fatalf("construct reset observer capability: %v", err)
	}
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		Capability:  admissionCapabilityFromProfile(profile),
		WorkProfile: mustPredictiveRequestWorkProfile(t, "vllm"),
	})
	if err != nil {
		t.Fatalf("construct reset observer Controller: %v", err)
	}
	defer controller.Close()
	clock := &manualTestClock{now: time.Unix(300, 0)}
	observer, err := newAdmissionBackendObserver(admissionBackendObserverConfig{
		BackendKind: "vllm", MetricsURL: upstream.URL + "/metrics", UpstreamURL: upstream.URL,
		ModelName: modelName, RevalidateMetadata: true,
		CapabilityFingerprint: fingerprint, MaxModelLenTokens: profile.MaxModelLenTokens,
		KVCapacityTokens: profile.KVCapacityTokens, KVBlockSize: profile.KVBlockSize,
		PollInterval: 20 * time.Millisecond, MaximumAge: 60 * time.Millisecond,
		RequestTimeout: 100 * time.Millisecond, Controller: controller, Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("construct reset admission observer: %v", err)
	}
	defer observer.Close()

	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return snapshot.Available && snapshot.Observation.GenerationTokensTotal == 10
	})
	if metadataCalls.Load() != 0 {
		t.Fatalf("ordinary observation unexpectedly fetched metadata calls=%d", metadataCalls.Load())
	}

	metadataMode.Store(1)
	generation.Store(1)
	clock.Advance(100 * time.Millisecond)
	waitAdmissionCondition(t, func() bool { return metadataCalls.Load() >= 1 })
	failedRevalidation := controller.Snapshot(clock.Now())
	if !failedRevalidation.IntakeOpen || failedRevalidation.Available ||
		failedRevalidation.MinimumDecision.Reason != coreadmission.ReasonObservationStale ||
		failedRevalidation.Observation.GenerationTokensTotal != 10 {
		t.Fatalf("failed reset metadata revalidation published or misclassified state: %+v", failedRevalidation)
	}

	metadataMode.Store(0)
	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return snapshot.Available && snapshot.RuntimeEpoch == 2 &&
			snapshot.Observation.GenerationTokensTotal == 1
	})
	recovered := controller.Snapshot(clock.Now())
	if !recovered.IntakeOpen || !recovered.Available {
		t.Fatalf("same-capability reset did not recover after metadata validation: %+v", recovered)
	}

	metadataMaxModelLen.Store(256 * 1024)
	runtimeStart.Store(200)
	generation.Store(2)
	clock.Advance(time.Millisecond)
	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return !snapshot.IntakeOpen && snapshot.MinimumDecision.Reason == coreadmission.ReasonCapabilityDrift
	})
	drifted := controller.Snapshot(clock.Now())
	if drifted.IntakeOpen || drifted.MinimumDecision.Scope != coreadmission.ProtectionAvailability {
		t.Fatalf("reset max_model_len drift did not close admission: %+v", drifted)
	}
}

func TestAdmissionVLLMObserverConfirmsRuntimeRestartBeforeKVCapacityRebind(t *testing.T) {
	const modelName = "vendor/rebind-model"
	const maxModelLen int64 = 650 * 1024
	var generation atomic.Uint64
	var metricsCalls atomic.Int64
	var metadataCalls atomic.Int64
	var metadataFailure atomic.Bool
	var runtimeStart atomic.Int64
	var kvCapacity atomic.Int64
	generation.Store(10)
	runtimeStart.Store(100)
	kvCapacity.Store(1_000_000)
	upstream := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/metrics":
			metricsCalls.Add(1)
			writeAdmissionVLLMMetricsWithCapability(
				response,
				modelName,
				generation.Load(),
				kvCapacity.Load(),
				runtimeStart.Load(),
			)
		case "/v1/models":
			metadataCalls.Add(1)
			if metadataFailure.Load() {
				response.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = fmt.Fprintf(
				response,
				`{"object":"list","data":[{"id":%q,"max_model_len":%d}]}`,
				modelName,
				maxModelLen,
			)
		default:
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()

	fingerprint := predictiveModelIdentitySHA256(modelName)
	profile, err := runtimepredictive.NewBackendCapabilityProfile(runtimepredictive.CapabilityProfileInput{
		ModelIdentitySHA256: fingerprint,
		KVCapacityTokens:    kvCapacity.Load(),
		KVBlockSize:         64,
		KVHardRatio:         0.9,
		MaxModelLen:         maxModelLen,
		Source:              runtimepredictive.CapabilityProfileAutomatic,
	})
	if err != nil {
		t.Fatalf("construct rebind observer capability: %v", err)
	}
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		Capability:  admissionCapabilityFromProfile(profile),
		WorkProfile: mustPredictiveRequestWorkProfile(t, "vllm"),
	})
	if err != nil {
		t.Fatalf("construct rebind observer Controller: %v", err)
	}
	defer controller.Close()
	clock := &manualTestClock{now: time.Unix(350, 0)}
	observer, err := newAdmissionBackendObserver(admissionBackendObserverConfig{
		BackendKind: "vllm", MetricsURL: upstream.URL + "/metrics", UpstreamURL: upstream.URL,
		ModelName: modelName, RevalidateMetadata: true,
		CapabilityFingerprint: fingerprint, MaxModelLenTokens: profile.MaxModelLenTokens,
		KVCapacityTokens: profile.KVCapacityTokens, KVBlockSize: profile.KVBlockSize,
		PollInterval: time.Hour, MaximumAge: 2 * time.Hour,
		RequestTimeout: 100 * time.Millisecond, Controller: controller, Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("construct rebind admission observer: %v", err)
	}
	defer observer.Close()

	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return snapshot.Available && snapshot.Observation.RuntimeStartTime == 100
	})
	initial := controller.Snapshot(clock.Now())

	runtimeStart.Store(200)
	kvCapacity.Store(999_616)
	generation.Store(1)
	clock.Advance(100 * time.Millisecond)
	metadataFailure.Store(true)
	observer.poll(context.Background())
	if metadataCalls.Load() != 1 {
		t.Fatalf("failed restart candidate metadata calls=%d, want 1", metadataCalls.Load())
	}
	afterFailure := controller.Snapshot(clock.Now())
	if afterFailure.RuntimeEpoch != initial.RuntimeEpoch ||
		afterFailure.Observation.RuntimeStartTime != initial.Observation.RuntimeStartTime ||
		!afterFailure.RuntimeRebindPending || afterFailure.Available ||
		afterFailure.MinimumDecision.Reason != coreadmission.ReasonObservationStale {
		t.Fatalf("failed metadata candidate changed availability contract: %+v", afterFailure)
	}

	metadataFailure.Store(false)
	observer.poll(context.Background())
	if metadataCalls.Load() != 2 {
		t.Fatalf("first coherent restart candidate metadata calls=%d, want 2", metadataCalls.Load())
	}
	afterFirst := controller.Snapshot(clock.Now())
	if afterFirst.RuntimeEpoch != initial.RuntimeEpoch || !afterFirst.RuntimeRebindPending || afterFirst.Available {
		t.Fatalf("first coherent capability candidate was published: %+v", afterFirst)
	}

	observer.poll(context.Background())
	if metadataCalls.Load() != 3 {
		t.Fatalf("confirmed restart candidate metadata calls=%d, want 3", metadataCalls.Load())
	}
	rebound := controller.Snapshot(clock.Now())
	if !rebound.Available || rebound.RuntimeEpoch != initial.RuntimeEpoch+1 ||
		rebound.CapabilityRebinds != 1 || rebound.RuntimeRebindPending ||
		rebound.Observation.RuntimeStartTime != 200 ||
		rebound.Observation.KVCapacityTokens != 999_616 {
		t.Fatalf("confirmed runtime capability was not rebound: %+v", rebound)
	}
	if !rebound.IntakeOpen || rebound.MinimumDecision.Reason == coreadmission.ReasonCapabilityDrift {
		t.Fatalf("confirmed runtime capability did not reopen: %+v", rebound)
	}

	sequence := rebound.ObservationSequence
	generation.Store(2)
	observer.poll(context.Background())
	followup := controller.Snapshot(clock.Now())
	if !followup.Available || followup.ObservationSequence <= sequence ||
		followup.RuntimeEpoch != rebound.RuntimeEpoch || !followup.IntakeOpen {
		t.Fatalf("rebound observer baseline was not retained: %+v", followup)
	}
}

func writeAdmissionVLLMMetrics(w http.ResponseWriter, modelName string, generation uint64) {
	writeAdmissionVLLMMetricsWithCapability(w, modelName, generation, 1_000_000, 0)
}

func writeAdmissionVLLMMetricsWithCapability(
	w http.ResponseWriter,
	modelName string,
	generation uint64,
	kvCapacity int64,
	runtimeStart int64,
) {
	_, _ = fmt.Fprintf(w, `
# TYPE vllm:cache_config_info gauge
# TYPE vllm:kv_cache_usage_perc gauge
# TYPE vllm:num_requests_running gauge
# TYPE vllm:num_requests_waiting gauge
# TYPE vllm:num_preemptions_total counter
# TYPE vllm:generation_tokens_total counter
vllm:cache_config_info{block_size="64",kv_cache_size_tokens="%d",num_gpu_blocks="15625"} 1
vllm:kv_cache_usage_perc 0
vllm:num_requests_running{model_name=%q,engine="0"} 0
vllm:num_requests_waiting{model_name=%q,engine="0"} 0
vllm:num_preemptions_total{model_name=%q,engine="0"} 0
vllm:generation_tokens_total{model_name=%q,engine="0"} %d
`, kvCapacity, modelName, modelName, modelName, modelName, generation)
	if runtimeStart > 0 {
		_, _ = fmt.Fprintf(w, "process_start_time_seconds %d\n", runtimeStart)
	}
}

func waitAdmissionCondition(t testing.TB, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timed out waiting for admission condition")
}
