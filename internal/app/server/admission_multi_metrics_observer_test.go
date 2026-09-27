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

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
)

func TestAdmissionObserverAggregatesAllDecodeEndpointsAndFailsClosed(t *testing.T) {
	const modelName = "meta/multi-decode-model"
	type endpointState struct {
		start       float64
		running     int
		waiting     int
		prealloc    int
		transfer    int
		generation  uint64
		preemptions uint64
		status      int
	}

	var mu sync.RWMutex
	states := []endpointState{
		{start: 1_000, running: 2, waiting: 0, prealloc: 3, transfer: 4, generation: 100, preemptions: 1, status: http.StatusOK},
		{start: 2_000, running: 5, waiting: 0, prealloc: 6, transfer: 7, generation: 200, preemptions: 2, status: http.StatusOK},
		{start: 3_000, running: 8, waiting: 0, prealloc: 9, transfer: 10, generation: 300, preemptions: 3, status: http.StatusOK},
	}
	servers := make([]*httptest.Server, len(states))
	metricsURLs := make([]string, len(states))
	for i := range states {
		index := i
		servers[i] = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			mu.RLock()
			state := states[index]
			mu.RUnlock()
			if state.status != http.StatusOK {
				http.Error(w, "unavailable", state.status)
				return
			}
			r := httptest.NewRecorder()
			writeAdmissionSGLangMetrics(r, modelName, state.generation, state.preemptions)
			text := strings.ReplaceAll(r.Body.String(), `engine_type="unified"`, `engine_type="decode"`)
			text += "# TYPE sglang:num_decode_prealloc_queue_reqs gauge\n# TYPE sglang:num_decode_transfer_queue_reqs gauge\n"
			text += fmt.Sprintf("process_start_time_seconds %g\n", state.start)
			text += fmt.Sprintf("sglang:num_running_reqs{engine_type=\"decode\",model_name=%q,tp_rank=\"1\",priority=\"\"} %d\n", modelName, state.running)
			text += fmt.Sprintf("sglang:num_queue_reqs{engine_type=\"decode\",model_name=%q,tp_rank=\"1\",priority=\"\"} %d\n", modelName, state.waiting)
			text += fmt.Sprintf("sglang:num_decode_prealloc_queue_reqs{engine_type=\"decode\",model_name=%q,tp_rank=\"0\"} %d\n", modelName, state.prealloc)
			text += fmt.Sprintf("sglang:num_decode_prealloc_queue_reqs{engine_type=\"decode\",model_name=%q,tp_rank=\"1\"} %d\n", modelName, state.prealloc)
			text += fmt.Sprintf("sglang:num_decode_transfer_queue_reqs{engine_type=\"decode\",model_name=%q,tp_rank=\"0\"} %d\n", modelName, state.transfer)
			text += fmt.Sprintf("sglang:num_decode_transfer_queue_reqs{engine_type=\"decode\",model_name=%q,tp_rank=\"1\"} %d\n", modelName, state.transfer)
			text += fmt.Sprintf("sglang:realtime_tokens_total{engine_type=\"decode\",mode=\"decode\",model_name=%q,tp_rank=\"1\",priority=\"\"} %d\n", modelName, state.generation)
			text += fmt.Sprintf("sglang:num_retracted_requests_total{engine_type=\"decode\",model_name=%q,tp_rank=\"1\",priority=\"\"} %d\n", modelName, state.preemptions)
			_, _ = fmt.Fprint(w, text)
		}))
		metricsURLs[i] = servers[i].URL
	}
	t.Cleanup(func() {
		for _, server := range servers {
			server.Close()
		}
	})

	clock := &manualTestClock{now: time.Unix(10_000, 0)}
	client := &http.Client{Timeout: time.Second}
	sample, err := prometheus.FetchAggregateSampleContext(context.Background(), client, metricsURLs)
	if err != nil {
		t.Fatalf("fetch initial aggregate: %v", err)
	}
	startup, err := predictiveBackendStartupFromSample(sample, clock.Now())
	if err != nil {
		t.Fatalf("validate initial aggregate: %v", err)
	}
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		RuntimeIdentity: startup.ModelIdentitySHA256,
		PDDecode:        true,
		Now:             clock.Now,
	})
	if err != nil {
		t.Fatalf("construct admission controller: %v", err)
	}
	defer controller.Close()
	window, ok := controller.StartSampleWindow()
	if !ok || !controller.PublishObservation(window, coreadmission.BackendObservation{
		RuntimeIdentity:       startup.ModelIdentitySHA256,
		ObservedAt:            startup.ObservedAt,
		MaximumAge:            time.Minute,
		Running:               int64(startup.Running),
		Waiting:               int64(startup.Waiting),
		DecodePending:         int64(startup.DecodePending),
		GenerationTokensTotal: startup.Generation,
		PreemptionsTotal:      startup.Preemptions,
		RuntimeStartTime:      startup.RuntimeStartTime,
		RuntimeEpochIdentity:  startup.RuntimeEpochIdentity,
	}).Accepted {
		t.Fatal("initial aggregate observation was not published")
	}

	observer := &admissionBackendObserver{
		backendKind: "sglang", metricsURLs: metricsURLs, runtimeIdentity: startup.ModelIdentitySHA256,
		maximumAge: time.Minute, controller: controller, client: client, now: clock.Now,
	}
	clock.Advance(time.Second)
	observer.poll(context.Background())
	first := controller.Snapshot(clock.Now())
	if first.RuntimeEpoch != 1 || first.State.RawRunning != 15 || first.State.RawWaiting != 0 || first.State.RawDecodePending != 39 {
		t.Fatalf("aggregate observer counts or initial epoch wrong: %#v", first)
	}
	healthyRequest := controller.Admit(clock.Now().Add(time.Millisecond), coreadmission.NewTPSRequestDemand(1))
	if !healthyRequest.Decision.Admitted() || !healthyRequest.Handle.MarkForwarded() || !healthyRequest.Handle.MarkFirstByte() {
		t.Fatalf("healthy Decode reservation did not enter the response lifecycle: %+v", healthyRequest.Decision)
	}
	pendingRequest := controller.Admit(clock.Now().Add(2*time.Millisecond), coreadmission.NewTPSRequestDemand(1))
	if !pendingRequest.Decision.Admitted() || !pendingRequest.Handle.MarkForwarded() {
		t.Fatalf("pending Decode reservation did not forward: %+v", pendingRequest.Decision)
	}

	mu.Lock()
	states[1].start = 2_500
	states[1].running = 0
	states[1].waiting = 0
	states[1].prealloc = 0
	states[1].transfer = 0
	states[1].generation = 0
	states[1].preemptions = 0
	mu.Unlock()
	clock.Advance(time.Second)
	observer.poll(context.Background())
	reset := controller.Snapshot(clock.Now())
	if reset.RuntimeEpoch != 2 || reset.Observation.RuntimeStartTime != 1_000 || reset.Observation.RuntimeEpochIdentity == first.Observation.RuntimeEpochIdentity {
		t.Fatalf("restart of Decode-1 did not advance aggregate runtime epoch: %#v", reset)
	}
	if reset.State.RawRunning != 10 || reset.State.UnobservedSequences != 1 || reset.State.LiveReservations != 2 {
		t.Fatalf("partial Decode restart lost in-flight reservation liability: %#v", reset.State)
	}
	if !pendingRequest.Handle.MarkFirstByte() || !healthyRequest.Handle.Terminate(coreadmission.TerminalSuccess) ||
		!pendingRequest.Handle.Terminate(coreadmission.TerminalSuccess) {
		t.Fatal("pre-reset Decode reservations did not remain operable after partial restart")
	}

	mu.Lock()
	states[0].running = 99
	states[2].status = http.StatusServiceUnavailable
	mu.Unlock()
	previousSequence := reset.ObservationSequence
	clock.Advance(time.Second)
	observer.poll(context.Background())
	failed := controller.Snapshot(clock.Now())
	if failed.ObservationSequence != previousSequence || failed.Observation.Running != reset.Observation.Running {
		t.Fatalf("failed endpoint published a partial aggregate: before=%#v after=%#v", reset, failed)
	}
}
