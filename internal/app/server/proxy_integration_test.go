package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestTrustedGatewayHeadersAreForwardedWithoutRequestMutation(t *testing.T) {
	var seenBody string
	var seenGatewayTrace string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		seenGatewayTrace = r.Header.Get("X-Gateway-Trace")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer backend.Close()

	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	body := `{"model":"m","messages":[{"role":"user","content":"plain"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gateway-Trace", "trace-123")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if seenBody != body {
		t.Fatalf("backend body=%s want %s", seenBody, body)
	}
	if seenGatewayTrace != "trace-123" {
		t.Fatalf("gateway trace header = %q, want forwarded trace", seenGatewayTrace)
	}
}

func TestPredictiveTimingSeparatesBodyReadEstimatorAndPreForwardDecision(t *testing.T) {
	const body = `{"model":"m","messages":[{"role":"user","content":"timing"}]}`
	const readDelay = 40 * time.Millisecond
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Body = &delayedReadCloser{Reader: strings.NewReader(body), delay: readDelay}
	request.ContentLength = int64(len(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("timed request status=%d body=%q, want 200", response.Code, response.Body.String())
	}
	var rendered strings.Builder
	srv.writeAdmissionAndRouterMetrics(&rendered)
	metricsBody := rendered.String()
	bodyRead := requirePrometheusMetric(t, metricsBody, "pig_predictive_admission_body_read_duration_seconds_sum")
	estimator := requirePrometheusMetric(t, metricsBody, "pig_predictive_admission_estimator_duration_seconds_sum")
	preForward := requirePrometheusMetric(t, metricsBody, "pig_predictive_admission_pre_forward_duration_seconds_sum")
	if bodyRead < readDelay.Seconds() {
		t.Fatalf("body-read duration=%f, want at least injected delay %f", bodyRead, readDelay.Seconds())
	}
	if estimator >= readDelay.Seconds()/2 {
		t.Fatalf("estimator duration=%f includes injected body-read delay %f", estimator, readDelay.Seconds())
	}
	if preForward < bodyRead+estimator {
		t.Fatalf("pre-forward duration=%f, want at least body-read+estimator=%f", preForward, bodyRead+estimator)
	}
	for _, name := range []string{
		"pig_predictive_admission_body_read_duration_seconds_count",
		"pig_predictive_admission_estimator_duration_seconds_count",
		"pig_predictive_admission_pre_forward_duration_seconds_count",
	} {
		if got := requirePrometheusMetric(t, metricsBody, name); got != 1 {
			t.Fatalf("%s=%f, want one measured request", name, got)
		}
	}
}

type delayedReadCloser struct {
	io.Reader
	delay time.Duration
	done  bool
}

func (r *delayedReadCloser) Read(buffer []byte) (int, error) {
	if !r.done {
		r.done = true
		time.Sleep(r.delay)
	}
	return r.Reader.Read(buffer)
}

func (*delayedReadCloser) Close() error { return nil }

func requirePrometheusMetric(t *testing.T, body, name string) float64 {
	t.Helper()
	prefix := name + " "
	for _, line := range strings.Split(body, "\n") {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(strings.TrimPrefix(line, prefix)), 64)
		if err != nil {
			t.Fatalf("parse %s from %q: %v", name, line, err)
		}
		return value
	}
	t.Fatal(fmt.Sprintf("metric %s missing from output:\n%s", name, body))
	return 0
}

func TestAPIAuthRejectsGenerationWithoutBearer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("backend should not be called")
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", recorder.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("unauthorized body is not json: %v", err)
	}
	if _, ok := body["error"]; !ok {
		t.Fatalf("missing OpenAI error body: %s", recorder.Body.String())
	}
}

func TestAPIAuthRejectsCompletionAndResponsesWithoutBearer(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}

	for _, path := range []string{"/v1/completions", "/v1/responses"} {
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"model":"m"}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()

		srv.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d want 401", path, recorder.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s unauthorized body is not json: %v", path, err)
		}
		if _, ok := body["error"]; !ok {
			t.Fatalf("%s missing OpenAI error body: %s", path, recorder.Body.String())
		}
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls=%d want 0", backendCalls)
	}
}

func TestProtectedPIGRoutesRequireBearerAndDoNotProxy(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}

	for _, path := range []string{"/pig/metrics", "/v1/metrics", "/v1/upstream-status", "/v1/attestation/report"} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		recorder := httptest.NewRecorder()

		srv.ServeHTTP(recorder, request)

		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d want 401", path, recorder.Code)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/attestation/report", nil)
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("POST /v1/attestation/report status=%d want 401 before method handling", recorder.Code)
	}
	request = httptest.NewRequest(http.MethodGet, "/pig/metrics", nil)
	request.Header.Add("Authorization", "Bearer secret")
	request.Header.Add("Authorization", "Bearer attacker")
	recorder = httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("duplicate Authorization status=%d want 401", recorder.Code)
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls=%d want 0", backendCalls)
	}
}

func TestUpstreamStatusReturnsAggregateCode(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/upstream-status", nil)
	request.Header.Set("Authorization", "Bearer secret")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("content-type=%q want text/plain; charset=utf-8", got)
	}
	if got := recorder.Body.String(); got != "0\n" {
		t.Fatalf("body=%q want aggregate green status", got)
	}
	if backendCalls != 0 {
		t.Fatalf("backend calls=%d want 0", backendCalls)
	}
}

func TestCompletionAndResponsesProxyWithoutApplicationBodyRewrite(t *testing.T) {
	for _, path := range []string{"/v1/completions", "/v1/responses"} {
		t.Run(path, func(t *testing.T) {
			var seenPath string
			var seenBody string
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seenPath = r.URL.Path
				seenBody = string(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"cmpl-test","choices":[{"text":"ok"}]}`))
			}))
			defer backend.Close()

			srv, err := newTestProxyServer(testProxyConfig(backend.URL))
			if err != nil {
				t.Fatalf("newProxyServer: %v", err)
			}

			body := `{"model":"m","messages":[{"role":"assistant","tool_calls":[]}],"priority":100}`
			request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer secret")
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-User-Tier", "premium")
			recorder := httptest.NewRecorder()

			srv.ServeHTTP(recorder, request)

			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if seenPath != path {
				t.Fatalf("backend path=%q want %q", seenPath, path)
			}
			if seenBody != body {
				t.Fatalf("application body mutated: got=%s want=%s", seenBody, body)
			}
		})
	}
}

func TestUpstreamErrorClassificationConvertsInputImage500(t *testing.T) {
	const upstreamMessage = "403, message='Forbidden', url='https://halleonard-coverimages.s3.amazonaws.com/wl/02116757-wl.jpg'"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(testOpenAIErrorBody(upstreamMessage, "InternalServerError", http.StatusInternalServerError))
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	assertOpenAIError(t, recorder, http.StatusUnprocessableEntity, "UnprocessableEntityError", upstreamMessage)
}

func TestStreamingUpstreamErrorClassificationConvertsInputImage500(t *testing.T) {
	const upstreamMessage = "Cannot connect to host files.teleclaw.io:443 ssl:default [Name or service not known]"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(testOpenAIErrorBody(upstreamMessage, "InternalServerError", http.StatusInternalServerError))
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	assertOpenAIError(t, recorder, http.StatusUnprocessableEntity, "UnprocessableEntityError", upstreamMessage)
}

func TestUpstreamErrorClassificationLeavesBackendCrash500(t *testing.T) {
	const upstreamMessage = "Scheduler hit an exception while running the model"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(testOpenAIErrorBody(upstreamMessage, "InternalServerError", http.StatusInternalServerError))
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	assertOpenAIError(t, recorder, http.StatusInternalServerError, "InternalServerError", upstreamMessage)
}

func TestUpstreamErrorClassificationCanBeDisabled(t *testing.T) {
	const upstreamMessage = "This model's maximum context length is 262144 tokens. However, you requested too many tokens."
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write(testOpenAIErrorBody(upstreamMessage, "InternalServerError", http.StatusInternalServerError))
	}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.UpstreamErrorClassificationEnabled = false
	srv, err := newTestProxyServer(cfg)
	if err != nil {
		t.Fatalf("newProxyServer: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[]}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()

	srv.ServeHTTP(recorder, request)

	assertOpenAIError(t, recorder, http.StatusInternalServerError, "InternalServerError", upstreamMessage)
}

func testProxyConfig(upstream string) config {
	return config{
		Listen:                             ":0",
		Upstream:                           upstream,
		PredictiveMetricsURL:               upstream + "/metrics",
		Token:                              "secret",
		QoSPaths:                           []string{"/v1/chat/completions", "/v1/completions", "/v1/responses"},
		APIAuthEnabled:                     true,
		APIAuthPaths:                       []string{"/v1/chat/completions", "/v1/completions", "/v1/responses"},
		UpstreamErrorClassificationEnabled: true,
		AttestationEnabled:                 false,
		ProxyTimeout:                       10 * time.Second,
		PredictiveAdmissionMode:            "enforce",
		PredictiveScannerBodyBytes:         2 * 1024 * 1024,
		PredictiveScannerConcurrency:       16,
		OutputTokenFields:                  []string{"max_tokens", "max_completion_tokens", "max_output_tokens"},
		PredictiveEstimator:                kvadmission.DefaultEstimatorConfig(),
		PredictiveStartupProbeTimeout:      time.Second,
		PredictiveMetricsRequestTimeout:    100 * time.Millisecond,
		PredictiveObservationPollInterval:  500 * time.Millisecond,
		PredictiveMaximumMetricsAge:        1500 * time.Millisecond,
		PredictiveKVHardRatio:              0.88,
	}
}

func newTestProxyServer(cfg config) (*proxyServer, error) {
	return newProxyServerWithDependencies(cfg, serverDependencies{
		NewAdmission: func(config) (admissionService, error) {
			return &testForwardAdmissionService{}, nil
		},
	})
}

type testForwardAdmissionService struct{}

func (*testForwardAdmissionService) Decide(_ context.Context, estimate domainpredictive.RequestEstimate) admissionDecision {
	return admissionDecision{
		Record: coreadmission.DecisionRecord{
			Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen, Estimate: estimate,
		},
		Reservation: &testForwardReservation{},
	}
}

func (*testForwardAdmissionService) Snapshot(now time.Time) admissionTelemetrySnapshot {
	return admissionTelemetrySnapshot{Capacity: coreadmission.CapacitySnapshot{
		IntakeOpen: true, HasObservation: true, Available: true,
		MinimumDecision: coreadmission.DecisionRecord{Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen},
		Observation:     coreadmission.BackendObservation{ObservedAt: now, MaximumAge: time.Minute},
	}}
}

func (*testForwardAdmissionService) Close() error { return nil }

type testForwardReservation struct{}

func (*testForwardReservation) MarkForwarded() bool { return true }
func (*testForwardReservation) MarkFirstByte() bool { return true }
func (*testForwardReservation) Terminate(coreadmission.TerminalCause) bool {
	return true
}

func testOpenAIErrorBody(message, errorType string, code int) []byte {
	body, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errorType,
			"param":   nil,
			"code":    code,
		},
	})
	if err != nil {
		panic(err)
	}
	return body
}

func assertOpenAIError(t *testing.T, recorder *httptest.ResponseRecorder, wantStatus int, wantType, wantMessage string) {
	t.Helper()
	if recorder.Code != wantStatus {
		t.Fatalf("status=%d want %d body=%s", recorder.Code, wantStatus, recorder.Body.String())
	}
	var payload map[string]map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("body is not json: %v; body=%s", err, recorder.Body.String())
	}
	errorPayload := payload["error"]
	if errorPayload["message"] != wantMessage {
		t.Fatalf("message=%v want %s", errorPayload["message"], wantMessage)
	}
	if errorPayload["type"] != wantType {
		t.Fatalf("type=%v want %s", errorPayload["type"], wantType)
	}
	if int(errorPayload["code"].(float64)) != wantStatus {
		t.Fatalf("code=%v want %d", errorPayload["code"], wantStatus)
	}
}
