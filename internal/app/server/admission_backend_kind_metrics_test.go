package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
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

func TestV01215AdmissionMetricsExposeFirstByteWorkLiabilities(t *testing.T) {
	input := observabilitymetrics.PredictiveAdmissionInput{}
	applyAdmissionDecisionMetrics(&input, coreadmission.DecisionRecord{
		Work: domainpredictive.RequestWork{
			PrefillInputTokens: 800,
			FirstBytePendingPrefillInputTokens: 300,
			FirstBytePendingPrefillComputeTokens: 200,
			FirstBytePendingPrefillSequences: 3,
			InputKVTokens: 1_600,
			FirstByteCoverableInputKVTokens: 400,
			FirstBytePendingInputKVTokens: 1_200,
			FutureKVTokens: 2_000,
		},
	})
	if input.AdmissionFirstBytePendingPrefillInput != 300 ||
		input.AdmissionFirstBytePendingPrefillCompute != 200 ||
		input.AdmissionFirstBytePendingPrefillSequences != 3 ||
		input.AdmissionInputKVTokens != 1_600 ||
		input.AdmissionFirstByteCoverableInputKV != 400 ||
		input.AdmissionFirstBytePendingInputKV != 1_200 ||
		input.AdmissionFutureKVTokens != 2_000 {
		t.Fatalf("first-byte work metrics=%+v", input)
	}
}
