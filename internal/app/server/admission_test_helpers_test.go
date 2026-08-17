package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const testAdmissionFingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type admissionRuntimeTestConfig struct {
	Mode         string
	BackendKind  string
	KVCapacity   int64
	MaxModelLen  int64
	KVHardRatio  float64
	UsedKVTokens int64
	Running      int64
	Waiting      int64
	MaximumAge   time.Duration
	Generation   uint64
	Preemptions  uint64
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
	if config.KVCapacity == 0 {
		config.KVCapacity = 1_000_000
	}
	if config.MaxModelLen == 0 {
		config.MaxModelLen = 650 * 1024
	}
	if config.KVHardRatio == 0 {
		config.KVHardRatio = 0.9
	}
	if config.MaximumAge == 0 {
		config.MaximumAge = time.Hour
	}
	profile, err := runtimepredictive.NewBackendCapabilityProfile(runtimepredictive.CapabilityProfileInput{
		ModelIdentitySHA256: testAdmissionFingerprint,
		KVCapacityTokens:    config.KVCapacity,
		KVBlockSize:         64,
		KVHardRatio:         config.KVHardRatio,
		MaxModelLen:         config.MaxModelLen,
		Source:              runtimepredictive.CapabilityProfileAutomatic,
	})
	if err != nil {
		t.Fatalf("construct admission test capability: %v", err)
	}
	workProfile, err := predictiveRequestWorkProfile(config.BackendKind)
	if err != nil {
		t.Fatalf("construct admission test work profile: %v", err)
	}
	controller, err := coreadmission.NewAdmissionController(coreadmission.ControllerConfig{
		Capability:  admissionCapabilityFromProfile(profile),
		WorkProfile: workProfile,
		TPS:         coreadmission.TPSPolicyConfig{Reference: config.TPSReference},
	})
	if err != nil {
		t.Fatalf("construct admission test Controller: %v", err)
	}
	clock := &manualTestClock{now: time.Unix(100, 0)}
	publishAdmissionObservationForTest(t, controller, profile, coreadmission.BackendObservation{
		CapabilityFingerprint: profile.ModelIdentitySHA256,
		MaxModelLenTokens:     profile.MaxModelLenTokens,
		KVCapacityTokens:      profile.KVCapacityTokens,
		KVBlockSize:           profile.KVBlockSize,
		ObservedAt:            clock.Now(),
		MaximumAge:            config.MaximumAge,
		UsedKVTokens:          config.UsedKVTokens,
		Running:               config.Running,
		Waiting:               config.Waiting,
		GenerationTokensTotal: config.Generation,
		PreemptionsTotal:      config.Preemptions,
	})
	runtime, err := newAdmissionRuntime(
		controller,
		newAdmissionReporter(time.Hour, nil),
		profile,
		"test",
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

func admissionCapabilityFromProfile(profile runtimepredictive.BackendCapabilityProfile) coreadmission.Capability {
	return coreadmission.Capability{
		Fingerprint:                  profile.ModelIdentitySHA256,
		MaxModelLenTokens:            profile.MaxModelLenTokens,
		KVCapacityTokens:             profile.KVCapacityTokens,
		KVBlockSize:                  profile.KVBlockSize,
		KVHardLimitTokens:            profile.KVHardLimitTokens,
		MaximumInputTokens:           profile.MaximumAdmissibleInputTokens,
		MinimumDecodeHorizonTokens:   runtimepredictive.DefaultCapabilityDecodeHorizonTokens,
		PrefillRegularTokens:         profile.PrefillRegularTokens,
		PrefillExclusiveTokens:       profile.PrefillExclusiveTokens,
		PrefillQuiescentTokens:       profile.PrefillQuiescentTokens,
		PrefillContendedBudgetTokens: profile.PrefillContendedBudgetTokens,
		PrefillAggregateBudgetTokens: profile.PrefillAggregateBudgetTokens,
	}
}

func mustPredictiveRequestWorkProfile(
	t testing.TB,
	backendKind string,
) domainpredictive.BackendExecutionProfile {
	t.Helper()
	profile, err := predictiveRequestWorkProfile(backendKind)
	if err != nil {
		t.Fatal(err)
	}
	return profile
}

func publishAdmissionObservationForTest(
	t testing.TB,
	controller *coreadmission.AdmissionController,
	profile runtimepredictive.BackendCapabilityProfile,
	observation coreadmission.BackendObservation,
) coreadmission.PublicationResult {
	t.Helper()
	window, ok := controller.StartSampleWindow()
	if !ok {
		t.Fatal("start admission test sample window")
	}
	result := controller.PublishObservation(window, observation)
	if !result.Accepted {
		t.Fatalf("publish admission test observation: %+v profile=%+v", result, profile)
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
