package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

const testAdmissionRuntimeIdentity = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type admissionRuntimeTestConfig struct {
	Mode         string
	BackendKind  string
	Running      int64
	Waiting      int64
	MaximumAge   time.Duration
	Generation   uint64
	Preemptions  uint64
	RuntimeStart float64
	TPSReference float64
}

func newAdmissionRuntimeForTest(
	t testing.TB,
	config admissionRuntimeTestConfig,
) (*admissionRuntime, *coreadmission.AdmissionController, *manualTestClock) {
	t.Helper()
	if config.Mode == "" {
		config.Mode = "enforce"
	}
	if config.BackendKind == "" {
		config.BackendKind = "vllm"
	}
	if config.MaximumAge == 0 {
		config.MaximumAge = time.Hour
	}
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		RuntimeIdentity: testAdmissionRuntimeIdentity,
		TPS:             coreadmission.TPSPolicyConfig{Reference: config.TPSReference},
	})
	if err != nil {
		t.Fatalf("construct admission test Controller: %v", err)
	}
	clock := &manualTestClock{now: time.Unix(100, 0)}
	publishAdmissionObservationForTest(t, controller, coreadmission.BackendObservation{
		RuntimeIdentity:       testAdmissionRuntimeIdentity,
		ObservedAt:            clock.Now(),
		MaximumAge:            config.MaximumAge,
		Running:               config.Running,
		Waiting:               config.Waiting,
		GenerationTokensTotal: config.Generation,
		PreemptionsTotal:      config.Preemptions,
		RuntimeStartTime:      config.RuntimeStart,
	})
	runtime, err := newAdmissionRuntime(
		controller,
		newAdmissionReporter(time.Hour, nil),
		config.BackendKind,
		config.Mode,
		clock.Now,
	)
	if err != nil {
		controller.Close()
		t.Fatalf("construct admission test runtime: %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	return runtime, controller, clock
}

func publishAdmissionObservationForTest(
	t testing.TB,
	controller *coreadmission.AdmissionController,
	observation coreadmission.BackendObservation,
) coreadmission.PublicationResult {
	t.Helper()
	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("start admission test sample window")
	}
	result := controller.PublishObservation(window, observation)
	if !result.Accepted {
		t.Fatalf("publish admission test observation: %+v", result)
	}
	return result
}

func newProxyServerWithAdmissionForTest(
	t testing.TB,
	upstream string,
	mode string,
	service admissionService,
) *proxyServer {
	t.Helper()
	cfg := testProxyConfig(upstream)
	cfg.PredictiveAdmissionMode = mode
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewAdmission: func(config) (admissionService, error) { return service, nil },
	})
	if err != nil {
		t.Fatalf("construct admission test proxy: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func serveAdmissionRequest(t testing.TB, srv *proxyServer, content string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"model-agnostic","messages":[{"role":"user","content":` +
		strconv.Quote(content) + `}],"max_tokens":8}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	return response
}
