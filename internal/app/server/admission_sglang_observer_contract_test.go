package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestAdmissionBackendObserverPublishesSGLangCountersAndRejectsIdentityDrift(t *testing.T) {
	const modelName = "meta/sglang-observer-model"
	var generation atomic.Uint64
	var retractions atomic.Uint64
	var drift atomic.Bool
	generation.Store(100)
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		observedModel := modelName
		if drift.Load() {
			observedModel = "meta/drifted-model"
		}
		writeAdmissionSGLangMetrics(w, observedModel, generation.Load(), retractions.Load())
	}))
	defer metrics.Close()

	fingerprint := predictiveModelIdentitySHA256(modelName)
	profile, err := runtimepredictive.NewBackendCapabilityProfile(runtimepredictive.CapabilityProfileInput{
		ModelIdentitySHA256: fingerprint,
		KVCapacityTokens:    1_000_000,
		KVBlockSize:         16,
		KVHardRatio:         0.9,
		MaxModelLen:         650 * 1024,
		Source:              runtimepredictive.CapabilityProfileAutomatic,
	})
	if err != nil {
		t.Fatalf("construct SGLang observer capability: %v", err)
	}
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		Capability: admissionCapabilityFromProfile(profile),
	})
	if err != nil {
		t.Fatalf("construct SGLang observer Controller: %v", err)
	}
	defer controller.Close()
	clock := &manualTestClock{now: time.Unix(400, 0)}
	observer, err := newAdmissionBackendObserver(admissionBackendObserverConfig{
		BackendKind: "sglang", MetricsURL: metrics.URL,
		CapabilityFingerprint: fingerprint, MaxModelLenTokens: profile.MaxModelLenTokens,
		KVCapacityTokens: profile.KVCapacityTokens, KVBlockSize: profile.KVBlockSize,
		PollInterval: 20 * time.Millisecond, MaximumAge: 60 * time.Millisecond,
		RequestTimeout: 100 * time.Millisecond, Controller: controller, Now: clock.Now,
	})
	if err != nil {
		t.Fatalf("construct SGLang admission observer: %v", err)
	}
	defer observer.Close()

	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return snapshot.Available && snapshot.Observation.GenerationTokensTotal == 100 &&
			snapshot.Observation.PreemptionsTotal == 0
	})

	generation.Store(125)
	retractions.Store(1)
	clock.Advance(500 * time.Millisecond)
	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return snapshot.Available && snapshot.Observation.GenerationTokensTotal == 125 &&
			snapshot.Observation.PreemptionsTotal == 1
	})

	drift.Store(true)
	clock.Advance(time.Millisecond)
	waitAdmissionCondition(t, func() bool {
		snapshot := controller.Snapshot(clock.Now())
		return !snapshot.IntakeOpen && snapshot.MinimumDecision.Reason == coreadmission.ReasonCapabilityDrift
	})
}

func writeAdmissionSGLangMetrics(w http.ResponseWriter, modelName string, generation, retractions uint64) {
	_, _ = fmt.Fprintf(w, `
# TYPE sglang:max_total_num_tokens gauge
# TYPE sglang:page_size gauge
# TYPE sglang:num_pages gauge
# TYPE sglang:kv_available_tokens gauge
# TYPE sglang:kv_evictable_tokens gauge
# TYPE sglang:kv_used_tokens gauge
# TYPE sglang:num_running_reqs gauge
# TYPE sglang:num_queue_reqs gauge
# TYPE sglang:realtime_tokens_total counter
# TYPE sglang:num_retracted_requests_total counter
sglang:max_total_num_tokens{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority=""} 1000000
sglang:page_size{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority=""} 16
sglang:num_pages{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority=""} 62500
sglang:kv_available_tokens{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority=""} 900000
sglang:kv_evictable_tokens{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority=""} 50000
sglang:kv_used_tokens{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority=""} 50000
sglang:num_running_reqs{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority=""} 0
sglang:num_running_reqs{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority="9223372036854775807"} 0
sglang:num_queue_reqs{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority=""} 0
sglang:num_queue_reqs{engine_type="unified",model_name=%q,tp_rank="0",pp_rank="0",priority="9223372036854775807"} 0
sglang:realtime_tokens_total{engine_type="unified",mode="decode",model_name=%q,tp_rank="0",pp_rank="0",priority=""} %d
`, modelName, modelName, modelName, modelName, modelName, modelName, modelName, modelName,
		modelName, modelName, modelName, generation)
	if retractions > 0 {
		_, _ = fmt.Fprintf(w,
			"sglang:num_retracted_requests_total{engine_type=%q,model_name=%q,tp_rank=%q,pp_rank=%q,priority=%q} %d\n",
			"unified", modelName, "0", "0", "", retractions,
		)
	}
}
