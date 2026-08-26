package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

type predictivePolicyAPIDocument struct {
	SchemaVersion string     `json:"schema_version"`
	Revision      uint64     `json:"revision"`
	Source        string     `json:"source"`
	Persistence   string     `json:"persistence"`
	UpdatedAt     *time.Time `json:"updated_at"`
	Mutable       struct {
		TPSReference      float64 `json:"tps_reference"`
		WindowConcurrency int64   `json:"window_concurrency"`
		RunningLimit      int64   `json:"running_limit"`
	} `json:"mutable"`
	Effective struct {
		AdmissionMode             string `json:"admission_mode"`
		ObservationPollIntervalMS int64  `json:"observation_poll_interval_ms"`
		MaximumMetricsAgeMS       int64  `json:"maximum_metrics_age_ms"`
		RunningLimitSource        string `json:"running_limit_source"`
	} `json:"effective"`
}

func TestV01215PredictivePolicyAPIRequiresAuthDoesNotProxyAndReturnsEffectivePolicy(t *testing.T) {
	srv, _, backendCalls := newPredictivePolicyAPIFixture(t, admissionRuntimeTestConfig{TPSReference: 20})

	unauthorized := httptest.NewRequest(http.MethodGet, predictivePolicyAPIPath, nil)
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, unauthorized)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", response.Code, response.Body.String())
	}

	duplicate := httptest.NewRequest(http.MethodGet, predictivePolicyAPIPath, nil)
	duplicate.Header.Add("Authorization", "Bearer secret")
	duplicate.Header.Add("Authorization", "Bearer attacker")
	response = httptest.NewRecorder()
	srv.ServeHTTP(response, duplicate)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate auth status=%d body=%s", response.Code, response.Body.String())
	}

	wrongMethod := authorizedPolicyAPIRequest(http.MethodPut, nil)
	response = httptest.NewRecorder()
	srv.ServeHTTP(response, wrongMethod)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, PATCH" {
		t.Fatalf("method status=%d allow=%q body=%s", response.Code, response.Header().Get("Allow"), response.Body.String())
	}

	response = servePredictivePolicyAPI(t, srv, http.MethodGet, nil, "")
	document := decodePredictivePolicyDocument(t, response, http.StatusOK)
	if document.SchemaVersion != "pig.predictive-policy.v1" || document.Revision != 1 ||
		document.Source != "startup" || document.Persistence != "restart_restores_startup" ||
		document.UpdatedAt != nil || document.Mutable.TPSReference != 20 ||
		document.Mutable.WindowConcurrency != 32 || document.Mutable.RunningLimit != 0 {
		t.Fatalf("initial policy=%+v", document)
	}
	if document.Effective.AdmissionMode != "enforce" ||
		document.Effective.ObservationPollIntervalMS != 500 ||
		document.Effective.MaximumMetricsAgeMS != 1500 ||
		document.Effective.RunningLimitSource != "unknown" {
		t.Fatalf("effective policy=%+v", document.Effective)
	}
	for _, retired := range []string{
		"kv_capacity_tokens", "kv_block_size", "kv_hard_limit_tokens",
		"maximum_admissible_input_tokens", "prefill_regular_tokens",
		"prefill_exclusive_tokens", "prefill_quiescent_tokens",
		"prefill_aggregate_budget_tokens", "prefill_contended_budget_tokens",
	} {
		if strings.Contains(response.Body.String(), `"`+retired+`"`) {
			t.Fatalf("TPS-only policy document retained %q: %s", retired, response.Body.String())
		}
	}
	if response.Header().Get("Cache-Control") != "no-store" ||
		response.Header().Get("ETag") != `"1"` ||
		!strings.HasPrefix(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("headers=%v", response.Header())
	}
	body := response.Body.String()
	for _, secret := range []string{"secret", "Bearer", srv.cfg.Upstream, srv.cfg.PredictiveMetricsURL} {
		if strings.Contains(body, secret) {
			t.Fatalf("policy response leaked %q: %s", secret, body)
		}
	}
	if got := backendCalls.Load(); got != 0 {
		t.Fatalf("admin policy requests reached backend %d times", got)
	}
}

func TestV01215PredictivePolicyAPIRejectsInvalidPatchAtomically(t *testing.T) {
	srv, _, backendCalls := newPredictivePolicyAPIFixture(t, admissionRuntimeTestConfig{TPSReference: 20})
	tests := []struct {
		name        string
		body        string
		contentType string
		wantStatus  int
	}{
		{name: "media type", body: `{}`, contentType: "text/plain", wantStatus: http.StatusUnsupportedMediaType},
		{name: "missing revision", body: `{"tps_reference":25}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "missing reference", body: `{"expected_revision":1}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: `{"expected_revision":1,"tps_reference":25,"mode":"shadow"}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "trailing json", body: `{"expected_revision":1,"tps_reference":25}{}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "negative", body: `{"expected_revision":1,"tps_reference":-1}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "too large", body: `{"expected_revision":1,"tps_reference":1000001}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "zero window", body: `{"expected_revision":1,"window_concurrency":0}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "negative running", body: `{"expected_revision":1,"running_limit":-1}`, contentType: "application/json", wantStatus: http.StatusBadRequest},
		{name: "syntax", body: `{"expected_revision":1,`, contentType: "application/json", wantStatus: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := servePredictivePolicyAPI(t, srv, http.MethodPatch, []byte(test.body), test.contentType)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			current := decodePredictivePolicyDocument(
				t, servePredictivePolicyAPI(t, srv, http.MethodGet, nil, ""), http.StatusOK,
			)
			if current.Revision != 1 || current.Mutable.TPSReference != 20 ||
				current.Mutable.WindowConcurrency != 32 || current.Mutable.RunningLimit != 0 {
				t.Fatalf("invalid update changed policy: %+v", current)
			}
		})
	}

	oversized := bytes.Repeat([]byte(" "), predictivePolicyMaximumBodyBytes+1)
	response := servePredictivePolicyAPI(t, srv, http.MethodPatch, oversized, "application/json")
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized status=%d body=%s", response.Code, response.Body.String())
	}
	current := decodePredictivePolicyDocument(
		t, servePredictivePolicyAPI(t, srv, http.MethodGet, nil, ""), http.StatusOK,
	)
	if current.Revision != 1 || current.Mutable.TPSReference != 20 ||
		current.Mutable.WindowConcurrency != 32 || current.Mutable.RunningLimit != 0 || backendCalls.Load() != 0 {
		t.Fatalf("post-invalid policy=%+v backend_calls=%d", current, backendCalls.Load())
	}
}

func TestV01215PredictivePolicyAPIAppliesCASAndExportsMetricsAndStatus(t *testing.T) {
	srv, _, backendCalls := newPredictivePolicyAPIFixture(t, admissionRuntimeTestConfig{TPSReference: 20})
	applied := servePredictivePolicyAPI(
		t, srv, http.MethodPatch,
		[]byte(`{"expected_revision":1,"tps_reference":25,"window_concurrency":48,"running_limit":192}`),
		"application/json",
	)
	document := decodePredictivePolicyDocument(t, applied, http.StatusOK)
	if document.Revision != 2 || document.Source != "runtime_api" ||
		document.UpdatedAt == nil || document.Mutable.TPSReference != 25 ||
		document.Mutable.WindowConcurrency != 48 || document.Mutable.RunningLimit != 192 ||
		document.Effective.RunningLimitSource != "admin" ||
		applied.Header().Get("ETag") != `"2"` {
		t.Fatalf("applied policy=%+v headers=%v", document, applied.Header())
	}
	conflict := servePredictivePolicyAPI(
		t, srv, http.MethodPatch, []byte(`{"expected_revision":1,"tps_reference":30}`), "application/json",
	)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("conflict status=%d body=%s", conflict.Code, conflict.Body.String())
	}
	current := decodePredictivePolicyDocument(
		t, servePredictivePolicyAPI(t, srv, http.MethodGet, nil, ""), http.StatusOK,
	)
	if current.Revision != 2 || current.Mutable.TPSReference != 25 {
		t.Fatalf("conflict changed policy: %+v", current)
	}

	metricsRequest := httptest.NewRequest(http.MethodGet, "/pig/metrics", nil)
	metricsRequest.Header.Set("Authorization", "Bearer secret")
	metricsResponse := httptest.NewRecorder()
	srv.ServeHTTP(metricsResponse, metricsRequest)
	if metricsResponse.Code != http.StatusOK {
		t.Fatalf("metrics status=%d body=%s", metricsResponse.Code, metricsResponse.Body.String())
	}
	metricsBody := metricsResponse.Body.String()
	if requirePrometheusMetric(t, metricsBody, "pig_predictive_policy_revision") != 2 ||
		requirePrometheusMetric(t, metricsBody, "pig_predictive_tps_reference") != 25 ||
		!strings.Contains(metricsBody, `pig_predictive_policy_updates_total{result="applied"} 1`) ||
		!strings.Contains(metricsBody, `pig_predictive_policy_updates_total{result="conflict"} 1`) ||
		!strings.Contains(metricsBody, `pig_predictive_policy_updates_total{result="invalid"} 0`) ||
		!strings.Contains(metricsBody, `pig_predictive_policy_updates_total{result="failed"} 0`) ||
		requirePrometheusMetric(t, metricsBody, "pig_predictive_policy_last_updated_at_seconds") <= 0 {
		t.Fatalf("policy metrics missing or inconsistent:\n%s", metricsBody)
	}
	status := srv.statusLogLine()
	if !strings.Contains(status, "policy=2/runtime_api") || !strings.Contains(status, "tps=0.000/25.000") {
		t.Fatalf("status does not expose policy revision: %s", status)
	}
	if backendCalls.Load() != 0 {
		t.Fatalf("policy/metrics requests reached backend %d times", backendCalls.Load())
	}
}

func TestV01215PredictivePolicyAPIConcurrentPatchHasOneWinner(t *testing.T) {
	srv, _, backendCalls := newPredictivePolicyAPIFixture(t, admissionRuntimeTestConfig{TPSReference: 20})
	bodies := [][]byte{
		[]byte(`{"expected_revision":1,"tps_reference":25}`),
		[]byte(`{"expected_revision":1,"tps_reference":30}`),
	}
	statuses := make(chan int, len(bodies))
	var wait sync.WaitGroup
	for _, body := range bodies {
		body := body
		wait.Add(1)
		go func() {
			defer wait.Done()
			response := servePredictivePolicyAPI(t, srv, http.MethodPatch, body, "application/json")
			statuses <- response.Code
		}()
	}
	wait.Wait()
	close(statuses)
	var applied, conflicts int
	for status := range statuses {
		switch status {
		case http.StatusOK:
			applied++
		case http.StatusConflict:
			conflicts++
		default:
			t.Fatalf("unexpected status=%d", status)
		}
	}
	document := decodePredictivePolicyDocument(
		t, servePredictivePolicyAPI(t, srv, http.MethodGet, nil, ""), http.StatusOK,
	)
	if applied != 1 || conflicts != 1 || document.Revision != 2 ||
		(document.Mutable.TPSReference != 25 && document.Mutable.TPSReference != 30) ||
		backendCalls.Load() != 0 {
		t.Fatalf("applied=%d conflicts=%d policy=%+v backend_calls=%d", applied, conflicts, document, backendCalls.Load())
	}
}

func TestV01215PredictivePolicyAPIFailsClosedWhenPolicyServicePanics(t *testing.T) {
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{TPSReference: 20})
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("policy update reached backend")
	}))
	t.Cleanup(backend.Close)
	service := &panickingPolicyAdmissionService{admissionService: runtime}
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", service)

	response := servePredictivePolicyAPI(
		t, srv, http.MethodPatch, []byte(`{"expected_revision":1,"tps_reference":25}`), "application/json",
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("panic status=%d body=%s", response.Code, response.Body.String())
	}
	current := decodePredictivePolicyDocument(
		t, servePredictivePolicyAPI(t, srv, http.MethodGet, nil, ""), http.StatusOK,
	)
	if current.Revision != 1 || current.Mutable.TPSReference != 20 ||
		srv.policyUpdates.failed.Load() != 1 || srv.policyUpdates.applied.Load() != 0 {
		t.Fatalf(
			"panic changed policy=%+v failed=%d applied=%d",
			current,
			srv.policyUpdates.failed.Load(),
			srv.policyUpdates.applied.Load(),
		)
	}
}

func TestV01215PredictivePolicyUpdateChangesNextPreForwardDecision(t *testing.T) {
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		TPSReference: 0,
		Running:      3,
		Generation:   0,
	})
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)

	updated := servePredictivePolicyAPI(
		t, srv, http.MethodPatch, []byte(`{"expected_revision":1,"running_limit":3}`), "application/json",
	)
	decodePredictivePolicyDocument(t, updated, http.StatusOK)
	response := serveAdmissionRequest(t, srv, "small request")
	if response.Code != http.StatusTooManyRequests || backendCalls.Load() != 0 {
		t.Fatalf("post-update decision status=%d backend_calls=%d body=%s", response.Code, backendCalls.Load(), response.Body.String())
	}
	last := runtime.Snapshot(time.Now()).Report.LastDecision
	if last.Reason != coreadmission.ReasonRunningLimit || last.RunningLimit != 3 ||
		last.RunningLimitSource != coreadmission.RunningLimitSourceAdmin || last.PolicyRevision != 2 {
		t.Fatalf("post-update decision=%+v", last)
	}
}

func newPredictivePolicyAPIFixture(
	t testing.TB,
	config admissionRuntimeTestConfig,
) (*proxyServer, *admissionRuntime, *atomic.Int64) {
	t.Helper()
	runtime, _, _ := newAdmissionRuntimeForTest(t, config)
	backendCalls := &atomic.Int64{}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(backend.Close)
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	return srv, runtime, backendCalls
}

func authorizedPolicyAPIRequest(method string, body []byte) *http.Request {
	request := httptest.NewRequest(method, predictivePolicyAPIPath, bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	return request
}

func servePredictivePolicyAPI(
	t testing.TB,
	srv *proxyServer,
	method string,
	body []byte,
	contentType string,
) *httptest.ResponseRecorder {
	t.Helper()
	request := authorizedPolicyAPIRequest(method, body)
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	return response
}

func decodePredictivePolicyDocument(
	t testing.TB,
	response *httptest.ResponseRecorder,
	wantStatus int,
) predictivePolicyAPIDocument {
	t.Helper()
	if response.Code != wantStatus {
		t.Fatalf("status=%d want=%d body=%s", response.Code, wantStatus, response.Body.String())
	}
	var document predictivePolicyAPIDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatalf("decode policy response: %v body=%s", err, response.Body.String())
	}
	return document
}

type panickingPolicyAdmissionService struct {
	admissionService
}

func (*panickingPolicyAdmissionService) UpdatePolicy(
	coreadmission.PolicyUpdate,
) (coreadmission.PolicyUpdateResult, error) {
	panic("policy update panic")
}
