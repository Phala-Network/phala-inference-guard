package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

var blockedBackendNativePaths = []string{
	"/v1/tokenize",
	"/tokenize",
	"/generate",
	"/v1/generate",
	"/encode",
	"/decode",
	"/detokenize",
	"/pooling",
	"/score",
	"/rerank",
}

func TestStrictPublicRoutePolicyBlocksBackendNativePathsBeforeAuthAdmissionAndProxy(t *testing.T) {
	authHeaders := []struct {
		name   string
		values []string
	}{
		{name: "missing"},
		{name: "valid", values: []string{"Bearer secret"}},
		{name: "wrong", values: []string{"Bearer wrong"}},
		{name: "duplicate", values: []string{"Bearer secret", "Bearer attacker"}},
	}
	for _, path := range blockedBackendNativePaths {
		for _, auth := range authHeaders {
			t.Run(strings.TrimPrefix(path, "/")+"/"+auth.name, func(t *testing.T) {
				var backendCalls atomic.Int64
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					backendCalls.Add(1)
					w.WriteHeader(http.StatusOK)
				}))
				defer backend.Close()
				admission := &routePolicyAdmissionSpy{}
				srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", admission)
				request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"secret":"must-not-be-read"}`))
				for _, value := range auth.values {
					request.Header.Add("Authorization", value)
				}
				response := httptest.NewRecorder()

				srv.ServeHTTP(response, request)

				assertGenericOpenAINotFound(t, response)
				assertRouteRejectedWithoutSideEffects(t, srv, admission, backendCalls.Load())
			})
		}
	}
}

func TestStrictPublicRoutePolicyRejectsMethodAndPathBypasses(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
	}{
		{name: "chat wrong method", method: http.MethodGet, target: "/v1/chat/completions"},
		{name: "models wrong method", method: http.MethodPost, target: "/v1/models"},
		{name: "prefix", method: http.MethodPost, target: "/foo/v1/chat/completions"},
		{name: "suffix", method: http.MethodPost, target: "/v1/chat/completions/suffix"},
		{name: "trailing slash", method: http.MethodPost, target: "/v1/chat/completions/"},
		{name: "repeated slash", method: http.MethodPost, target: "/v1//chat/completions"},
		{name: "dot segment", method: http.MethodPost, target: "/v1/chat/../completions"},
		{name: "encoded unreserved", method: http.MethodPost, target: "/v1/%63hat/completions"},
		{name: "encoded prefix", method: http.MethodPost, target: "/%76%31/chat/completions"},
		{name: "encoded slash", method: http.MethodPost, target: "/v1%2fchat%2fcompletions"},
		{name: "case mismatch", method: http.MethodPost, target: "/v1/Chat/completions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var backendCalls atomic.Int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				backendCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()
			admission := &routePolicyAdmissionSpy{}
			srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", admission)
			request := httptest.NewRequest(test.method, test.target, strings.NewReader(`{"model":"m"}`))
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()

			srv.ServeHTTP(response, request)

			assertGenericOpenAINotFound(t, response)
			assertRouteRejectedWithoutSideEffects(t, srv, admission, backendCalls.Load())
		})
	}
}

func TestStrictPublicRoutePolicyDoesNotReadBlockedLargeBody(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	admission := &routePolicyAdmissionSpy{}
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", admission)
	body := &countingReadCloser{reader: strings.NewReader(strings.Repeat("x", 4*1024*1024))}
	request := httptest.NewRequest(http.MethodPost, "/v1/tokenize", nil)
	request.Body = body
	request.ContentLength = 4 * 1024 * 1024
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	assertGenericOpenAINotFound(t, response)
	if reads := body.reads.Load(); reads != 0 {
		t.Fatalf("blocked body read calls=%d want 0", reads)
	}
	assertRouteRejectedWithoutSideEffects(t, srv, admission, backendCalls.Load())
}

func TestStrictPublicRoutePolicyAuthenticatesModelsAndDoesNotAdmitIt(t *testing.T) {
	for _, test := range []struct {
		name       string
		authValues []string
		wantStatus int
		wantCalls  int64
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "wrong", authValues: []string{"Bearer wrong"}, wantStatus: http.StatusUnauthorized},
		{name: "duplicate", authValues: []string{"Bearer secret", "Bearer attacker"}, wantStatus: http.StatusUnauthorized},
		{name: "valid", authValues: []string{"Bearer secret"}, wantStatus: http.StatusOK, wantCalls: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var backendCalls atomic.Int64
			var seenQuery string
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				backendCalls.Add(1)
				seenQuery = r.URL.RawQuery
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
			}))
			defer backend.Close()
			admission := &routePolicyAdmissionSpy{}
			srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", admission)
			request := httptest.NewRequest(http.MethodGet, "/v1/models?view=public", nil)
			for _, value := range test.authValues {
				request.Header.Add("Authorization", value)
			}
			response := httptest.NewRecorder()

			srv.ServeHTTP(response, request)

			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.wantStatus, response.Body.String())
			}
			if backendCalls.Load() != test.wantCalls {
				t.Fatalf("backend calls=%d want=%d", backendCalls.Load(), test.wantCalls)
			}
			if admission.decisions.Load() != 0 || admission.liveReservations.Load() != 0 {
				t.Fatalf("models used admission decisions=%d reservations=%d",
					admission.decisions.Load(), admission.liveReservations.Load())
			}
			if test.wantCalls == 1 && seenQuery != "view=public" {
				t.Fatalf("backend query=%q want view=public", seenQuery)
			}
		})
	}
}

func TestStrictPublicRoutePolicyAuthenticatesAndAdmitsGenerationRoutes(t *testing.T) {
	tests := []struct {
		path string
		body string
	}{
		{path: "/v1/chat/completions", body: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{path: "/v1/completions", body: `{"model":"m","prompt":"hi"}`},
		{path: "/v1/responses", body: `{"model":"m","input":"hi"}`},
	}
	authHeaders := []struct {
		name          string
		values        []string
		wantStatus    int
		wantCalls     int64
		wantDecisions int64
	}{
		{name: "missing", wantStatus: http.StatusUnauthorized},
		{name: "wrong", values: []string{"Bearer wrong"}, wantStatus: http.StatusUnauthorized},
		{name: "duplicate", values: []string{"Bearer secret", "Bearer attacker"}, wantStatus: http.StatusUnauthorized},
		{name: "valid", values: []string{"Bearer secret"}, wantStatus: http.StatusOK, wantCalls: 1, wantDecisions: 1},
	}
	for _, endpoint := range tests {
		for _, auth := range authHeaders {
			t.Run(strings.TrimPrefix(endpoint.path, "/")+"/"+auth.name, func(t *testing.T) {
				var backendCalls atomic.Int64
				var seenQuery, seenAccept, seenTrace string
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backendCalls.Add(1)
					seenQuery = r.URL.RawQuery
					seenAccept = r.Header.Get("Accept")
					seenTrace = r.Header.Get("X-Gateway-Trace")
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"id":"ok","choices":[]}`))
				}))
				defer backend.Close()
				admission := &routePolicyAdmissionSpy{}
				srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", admission)
				request := httptest.NewRequest(http.MethodPost, endpoint.path+"?trace=1", strings.NewReader(endpoint.body))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("Accept", "text/event-stream")
				request.Header.Set("X-Gateway-Trace", "route-policy")
				for _, value := range auth.values {
					request.Header.Add("Authorization", value)
				}
				response := httptest.NewRecorder()

				srv.ServeHTTP(response, request)

				if response.Code != auth.wantStatus || backendCalls.Load() != auth.wantCalls ||
					admission.decisions.Load() != auth.wantDecisions || admission.liveReservations.Load() != 0 {
					t.Fatalf("status=%d calls=%d decisions=%d reservations=%d want=%d/%d/%d/0 body=%q",
						response.Code, backendCalls.Load(), admission.decisions.Load(), admission.liveReservations.Load(),
						auth.wantStatus, auth.wantCalls, auth.wantDecisions, response.Body.String())
				}
				if srv.routeNotAllowed.Load() != 0 {
					t.Fatalf("allowed/auth-rejected route_not_allowed=%d want 0", srv.routeNotAllowed.Load())
				}
				if auth.wantCalls == 1 && (seenQuery != "trace=1" || seenAccept != "text/event-stream" || seenTrace != "route-policy") {
					t.Fatalf("forwarded metadata query=%q accept=%q trace=%q", seenQuery, seenAccept, seenTrace)
				}
			})
		}
	}
}

func TestStrictPublicRoutePolicyCanPreserveExplicitlyDisabledPublicAuth(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[]}`))
	}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.APIAuthEnabled = false
	srv, err := newTestProxyServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	if response.Code != http.StatusOK || backendCalls.Load() != 1 {
		t.Fatalf("explicitly disabled auth status=%d calls=%d body=%q", response.Code, backendCalls.Load(), response.Body.String())
	}
}

func TestStrictPublicRoutePolicyRejectsNonOriginAndOpaqueRequestTargets(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name: "absolute form",
			mutate: func(request *http.Request) {
				request.URL.Scheme = "http"
				request.URL.Host = "backend.invalid"
				request.RequestURI = "http://backend.invalid/v1/chat/completions"
			},
		},
		{
			name: "opaque",
			mutate: func(request *http.Request) {
				request.URL.Opaque = "/v1/chat/completions"
			},
		},
		{
			name: "raw path",
			mutate: func(request *http.Request) {
				request.URL.RawPath = "/v1/%63hat/completions"
				request.RequestURI = "/v1/%63hat/completions"
			},
		},
		{
			name: "request uri mismatch",
			mutate: func(request *http.Request) {
				request.RequestURI = "/v1/chat/completions/suffix"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var backendCalls atomic.Int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				backendCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()
			admission := &routePolicyAdmissionSpy{}
			srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", admission)
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`))
			request.Header.Set("Authorization", "Bearer secret")
			test.mutate(request)
			response := httptest.NewRecorder()

			srv.ServeHTTP(response, request)

			assertGenericOpenAINotFound(t, response)
			assertRouteRejectedWithoutSideEffects(t, srv, admission, backendCalls.Load())
		})
	}
}

func TestStrictPublicRoutePolicyRejectsRawWireBypasses(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "encoded unreserved", target: "/v1/%63hat/completions"},
		{name: "encoded slash", target: "/v1%2fchat%2fcompletions"},
		{name: "repeated slash", target: "/v1//chat/completions"},
		{name: "absolute form", target: "http://backend.invalid/v1/chat/completions"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var backendCalls atomic.Int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				backendCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()
			admission := &routePolicyAdmissionSpy{}
			srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", admission)
			publicServer := httptest.NewServer(srv)
			defer publicServer.Close()

			connection, err := net.Dial("tcp", publicServer.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer connection.Close()
			request := "POST " + test.target + " HTTP/1.1\r\n" +
				"Host: " + publicServer.Listener.Addr().String() + "\r\n" +
				"Authorization: Bearer secret\r\n" +
				"Content-Length: 0\r\n" +
				"Connection: close\r\n\r\n"
			if _, err := io.WriteString(connection, request); err != nil {
				t.Fatal(err)
			}
			response, err := http.ReadResponse(bufio.NewReader(connection), &http.Request{Method: http.MethodPost})
			if err != nil {
				t.Fatal(err)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			if err := response.Body.Close(); err != nil {
				t.Fatal(err)
			}

			assertGenericOpenAINotFoundResponse(t, response.StatusCode, response.Header, body)
			assertRouteRejectedWithoutSideEffects(t, srv, admission, backendCalls.Load())
		})
	}
}

func TestStrictPublicRoutePolicyDoesNotAliasEncodedLocalManagementPath(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	admission := &routePolicyAdmissionSpy{}
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", admission)
	request := httptest.NewRequest(http.MethodGet, "/v1/%6detrics", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	assertGenericOpenAINotFound(t, response)
	assertRouteRejectedWithoutSideEffects(t, srv, admission, 0)
}

func TestRouteNotAllowedLogIsStructuredAndDoesNotExposeRequestData(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/generate/customer-secret?api_key=query-secret", strings.NewReader("body-secret"))
	request.Header.Set("Authorization", "Bearer authorization-secret")
	line := routeRejectionLogLine(routeRejectionUnknown, request)
	want := "level=warn component=route event=rejected reason=route_not_allowed class=unknown_path method_class=post"
	if line != want {
		t.Fatalf("log line=%q want=%q", line, want)
	}
	for _, secret := range []string{"customer-secret", "query-secret", "body-secret", "authorization-secret"} {
		if strings.Contains(line, secret) {
			t.Fatalf("route rejection log leaked %q: %s", secret, line)
		}
	}
}

func TestStrictPublicRoutePolicyBlockedPathStaysLocalWhenBackendUnavailable(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	admission := &routePolicyAdmissionSpy{}
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", admission)
	backend.Close()
	request := httptest.NewRequest(http.MethodPost, "/generate", strings.NewReader(`{"text":"x"}`))
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	assertGenericOpenAINotFound(t, response)
	assertRouteRejectedWithoutSideEffects(t, srv, admission, 0)
}

func TestStrictPublicRoutePolicyPreservesLocalManagementSurface(t *testing.T) {
	var unexpectedBackendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/metrics" {
			unexpectedBackendCalls.Add(1)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv, err := newTestProxyServer(testProxyConfig(backend.URL))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		method     string
		path       string
		authorized bool
		wantStatus int
	}{
		{name: "health", method: http.MethodDelete, path: "/healthz", wantStatus: http.StatusOK},
		{name: "pig metrics", method: http.MethodPost, path: "/pig/metrics", authorized: true, wantStatus: http.StatusOK},
		{name: "combined metrics", method: http.MethodGet, path: "/v1/metrics", authorized: true, wantStatus: http.StatusOK},
		{name: "upstream status", method: http.MethodGet, path: "/v1/upstream-status", authorized: true, wantStatus: http.StatusOK},
		{name: "policy wrong method", method: http.MethodPost, path: "/admin/v1/predictive-policy", authorized: true, wantStatus: http.StatusMethodNotAllowed},
		{name: "attestation disabled", method: http.MethodGet, path: "/v1/attestation/report", authorized: true, wantStatus: http.StatusNotFound},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, nil)
			if test.authorized {
				request.Header.Set("Authorization", "Bearer secret")
			}
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", response.Code, test.wantStatus, response.Body.String())
			}
		})
	}
	if calls := unexpectedBackendCalls.Load(); calls != 0 {
		t.Fatalf("local management route was forwarded to backend %d times", calls)
	}
}

func assertGenericOpenAINotFound(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	assertGenericOpenAINotFoundResponse(t, response.Code, response.Header(), response.Body.Bytes())
}

func assertGenericOpenAINotFoundResponse(t *testing.T, status int, header http.Header, body []byte) {
	t.Helper()
	if status != http.StatusNotFound {
		t.Fatalf("status=%d want=404 body=%q", status, body)
	}
	if contentType := header.Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("content-type=%q want application/json", contentType)
	}
	var payload struct {
		Error struct {
			Message string  `json:"message"`
			Type    string  `json:"type"`
			Param   *string `json:"param"`
			Code    int     `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("404 is not JSON: %v body=%q", err, body)
	}
	if payload.Error.Message != "The requested resource was not found" ||
		payload.Error.Type != "invalid_request_error" || payload.Error.Param != nil ||
		payload.Error.Code != http.StatusNotFound {
		t.Fatalf("unexpected OpenAI 404 payload: %+v", payload.Error)
	}
	if strings.Contains(string(body), "tokenize") || strings.Contains(string(body), "generate") {
		t.Fatalf("404 leaked blocked route: %q", body)
	}
}

func assertRouteRejectedWithoutSideEffects(
	t *testing.T,
	srv *proxyServer,
	admission *routePolicyAdmissionSpy,
	backendCalls int64,
) {
	t.Helper()
	if backendCalls != 0 || admission.decisions.Load() != 0 || admission.liveReservations.Load() != 0 {
		t.Fatalf("rejected route side effects: backend=%d decisions=%d reservations=%d",
			backendCalls, admission.decisions.Load(), admission.liveReservations.Load())
	}
	if srv.requestClassifier.Inflight() != 0 || srv.requestClassifier.ReservedBodyBytes() != 0 ||
		srv.requestClassifier.Rejected() != 0 || srv.total429.Load() != 0 ||
		srv.predictiveEnforcedRejects.Load() != 0 || srv.backendUnavailable.Load() != 0 {
		t.Fatalf("rejected route changed request/admission counters: inflight=%d bytes=%d scanner_rejected=%d total429=%d predictive=%d unavailable=%d",
			srv.requestClassifier.Inflight(), srv.requestClassifier.ReservedBodyBytes(), srv.requestClassifier.Rejected(),
			srv.total429.Load(), srv.predictiveEnforcedRejects.Load(), srv.backendUnavailable.Load())
	}
	var metrics strings.Builder
	srv.writeLocalMetrics(&metrics)
	metricsBody := metrics.String()
	if got := requirePrometheusMetric(t, metricsBody, "pig_route_not_allowed_total"); got != 1 {
		t.Fatalf("pig_route_not_allowed_total=%f want 1", got)
	}
	for _, line := range []string{
		"pig_predictive_admission_attempts_total 0",
		"pig_predictive_admission_reservations 0",
		`pig_backend_inflight{name="upstream"} 0`,
		`pig_backend_requests_total{name="upstream",decision="accepted"} 0`,
		`pig_backend_requests_total{name="upstream",decision="failed"} 0`,
		`pig_backend_completed_total{name="upstream"} 0`,
		`pig_backend_proxy_errors_total{name="upstream"} 0`,
	} {
		if !strings.Contains(metricsBody, line+"\n") {
			t.Fatalf("rejected route changed or omitted metric %q:\n%s", line, metricsBody)
		}
	}
}

type countingReadCloser struct {
	reader io.Reader
	reads  atomic.Int64
}

func (r *countingReadCloser) Read(buffer []byte) (int, error) {
	r.reads.Add(1)
	return r.reader.Read(buffer)
}

func (*countingReadCloser) Close() error { return nil }

type routePolicyAdmissionSpy struct {
	decisions        atomic.Int64
	liveReservations atomic.Int64
}

func (s *routePolicyAdmissionSpy) Decide(_ context.Context, demand coreadmission.TPSRequestDemand) admissionDecision {
	s.decisions.Add(1)
	s.liveReservations.Add(1)
	return admissionDecision{
		Record: coreadmission.DecisionRecord{
			Action: coreadmission.ActionAdmit,
			Reason: coreadmission.ReasonOpen,
			Demand: demand,
		},
		Reservation: &routePolicyReservationSpy{live: &s.liveReservations},
	}
}

func (*routePolicyAdmissionSpy) Snapshot(now time.Time) admissionTelemetrySnapshot {
	return admissionTelemetrySnapshot{Capacity: coreadmission.CapacitySnapshot{
		IntakeOpen:     true,
		HasObservation: true,
		Available:      true,
		MinimumDecision: coreadmission.DecisionRecord{
			Action: coreadmission.ActionAdmit,
			Reason: coreadmission.ReasonOpen,
		},
		Observation: coreadmission.BackendObservation{ObservedAt: now, MaximumAge: time.Minute},
	}}
}

func (*routePolicyAdmissionSpy) Close() error { return nil }

type routePolicyReservationSpy struct {
	live       *atomic.Int64
	terminated atomic.Bool
}

func (*routePolicyReservationSpy) MarkForwarded() bool { return true }
func (*routePolicyReservationSpy) MarkFirstByte() bool { return true }

func (r *routePolicyReservationSpy) Terminate(coreadmission.TerminalCause) bool {
	if r.terminated.CompareAndSwap(false, true) {
		r.live.Add(-1)
		return true
	}
	return false
}
