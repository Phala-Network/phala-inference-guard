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
			writeAdmissionVLLMTPSMetrics(w, "vendor/drifted-model", generation.Load(), 0)
		default:
			writeAdmissionVLLMTPSMetrics(w, modelName, generation.Load(), 0)
		}
	}))
	defer metrics.Close()

	fingerprint := predictiveModelIdentitySHA256(modelName)
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		RuntimeIdentity: fingerprint,
	})
	if err != nil {
		t.Fatalf("construct observer Controller: %v", err)
	}
	defer controller.Close()
	clock := &manualTestClock{now: time.Unix(200, 0)}
	observer, err := newAdmissionBackendObserver(admissionBackendObserverConfig{
		BackendKind: "vllm", MetricsURL: metrics.URL, RuntimeIdentity: fingerprint,
		PollInterval: 20 * time.Millisecond, MaximumAge: 60 * time.Millisecond,
		RequestTimeout: 100 * time.Millisecond, Controller: controller, Now: clock.Now,
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
	if !stale.IntakeOpen || stale.Available || stale.MinimumDecision.Reason != coreadmission.ReasonObservationStale ||
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
		return !snapshot.IntakeOpen && snapshot.MinimumDecision.Reason == coreadmission.ReasonRuntimeIdentityDrift
	})
	drifted := controller.Snapshot(clock.Now())
	if drifted.IntakeOpen || drifted.MinimumDecision.Scope != coreadmission.ProtectionAvailability {
		t.Fatalf("identity drift did not permanently close admission: %+v", drifted)
	}
}

func TestAdmissionVLLMObserverResetsEpochImmediatelyWithoutMetadata(t *testing.T) {
	const modelName = "vendor/reset-model"
	var generation atomic.Uint64
	var runtimeStart atomic.Int64
	generation.Store(10)
	runtimeStart.Store(100)
	metrics := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		writeAdmissionVLLMTPSMetrics(response, modelName, generation.Load(), runtimeStart.Load())
	}))
	defer metrics.Close()

	fingerprint := predictiveModelIdentitySHA256(modelName)
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		RuntimeIdentity: fingerprint,
	})
	if err != nil {
		t.Fatalf("construct reset observer Controller: %v", err)
	}
	defer controller.Close()
	clock := &manualTestClock{now: time.Unix(300, 0)}
	observer, err := newAdmissionBackendObserver(admissionBackendObserverConfig{
		BackendKind: "vllm", MetricsURL: metrics.URL, RuntimeIdentity: fingerprint,
		PollInterval: time.Hour, MaximumAge: 2 * time.Hour,
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
	initial := controller.Snapshot(clock.Now())

	runtimeStart.Store(200)
	generation.Store(1)
	clock.Advance(500 * time.Millisecond)
	observer.poll(context.Background())
	restarted := controller.Snapshot(clock.Now())
	if !restarted.Available || !restarted.IntakeOpen || restarted.RuntimeEpoch != initial.RuntimeEpoch+1 ||
		restarted.Observation.RuntimeStartTime != 200 || restarted.Observation.GenerationTokensTotal != 1 {
		t.Fatalf("runtime restart did not reset the TPS epoch immediately: %+v", restarted)
	}

	generation.Store(0)
	clock.Advance(500 * time.Millisecond)
	observer.poll(context.Background())
	rolledBack := controller.Snapshot(clock.Now())
	if !rolledBack.Available || !rolledBack.IntakeOpen || rolledBack.RuntimeEpoch != restarted.RuntimeEpoch+1 ||
		rolledBack.Observation.GenerationTokensTotal != 0 {
		t.Fatalf("counter rollback did not reset the TPS epoch immediately: %+v", rolledBack)
	}
}

func writeAdmissionVLLMTPSMetrics(w http.ResponseWriter, modelName string, generation uint64, runtimeStart int64) {
	_, _ = fmt.Fprintf(w, `
# TYPE vllm:num_requests_running gauge
# TYPE vllm:num_requests_waiting gauge
# TYPE vllm:num_preemptions_total counter
# TYPE vllm:generation_tokens_total counter
vllm:num_requests_running{model_name=%q,engine="0"} 0
vllm:num_requests_waiting{model_name=%q,engine="0"} 0
vllm:num_preemptions_total{model_name=%q,engine="0"} 0
vllm:generation_tokens_total{model_name=%q,engine="0"} %d
`, modelName, modelName, modelName, modelName, generation)
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
