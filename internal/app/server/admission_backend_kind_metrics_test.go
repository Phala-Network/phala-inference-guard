package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	observabilitymetrics "github.com/Phala-Network/phala-inference-guard/internal/observability/metrics"
)

func TestBackendMetricsExposeFrozenSGLangKind(t *testing.T) {
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{BackendKind: "sglang"})
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	server := newProxyServerWithAdmissionForTest(t, upstream.URL, "enforce", runtime)

	backends := server.backendMetricsInput(runtime.Snapshot(clock.Now()), clock.Now())
	if len(backends) != 1 || backends[0].Status.BackendKind != "sglang" {
		t.Fatalf("backend metrics kind=%#v, want sglang", backends)
	}
}

func TestAdmissionMetricsExposeTPSDecisionAndSequenceLiabilities(t *testing.T) {
	input := observabilitymetrics.PredictiveAdmissionInput{}
	applyAdmissionDecisionMetrics(&input, coreadmission.DecisionRecord{
		Demand: coreadmission.TPSRequestDemand{
			DecodeSequences: 3,
			Source:          coreadmission.TPSDemandSourceRequest,
		},
		State: coreadmission.ProjectedState{
			RawRunning:          4,
			RawWaiting:          1,
			UnobservedSequences: 2,
			SequenceLiabilities: 2,
			GenerationDelta:     50,
			ObservationInterval: time.Second,
		},
		TPSDecisionResult:    coreadmission.TPSDecisionResultProtect,
		TPSDecisionSubreason: coreadmission.TPSDecisionSubreasonWaiting,
	})
	if input.AdmissionDecodeSequences != 3 ||
		input.AdmissionEffectiveSequences != 7 ||
		input.TPSDecisionResult != "protect" ||
		input.TPSDecisionSubreason != "waiting" {
		t.Fatalf("TPS decision metrics=%+v", input)
	}
}
