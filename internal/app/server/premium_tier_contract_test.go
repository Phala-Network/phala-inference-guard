package server

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

func TestPremiumTierBypassesAllAdmissionBeforeForward(t *testing.T) {
	var backendCalls atomic.Int64
	var seenBody atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backendCalls.Add(1)
		body, _ := io.ReadAll(r.Body)
		seenBody.Store(string(body))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", TPSReference: 50, Running: 100, Waiting: 100,
		WindowConcurrency: 1, RunningLimit: 1,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`not-json`,
	))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Tier", "premium")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	report := runtime.Snapshot(clock.Now()).Report
	if response.Code != http.StatusOK || backendCalls.Load() != 1 || report.Attempts != 0 || report.HasLastDecision {
		t.Fatalf("premium request entered admission: status=%d calls=%d attempts=%d has_last_decision=%t body=%q",
			response.Code, backendCalls.Load(), report.Attempts, report.HasLastDecision, response.Body.String())
	}
	if reserved := srv.requestClassifier.ReservedBodyBytes(); reserved != 0 {
		t.Fatalf("premium request left scanner reservation=%d", reserved)
	}
	if got := seenBody.Load(); got != "not-json" {
		t.Fatalf("premium body=%q want original body", got)
	}
}

func TestPremiumTierCannotBypassAuthOrRoutePolicy(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Tier", "premium")
	unauthorized := httptest.NewRecorder()
	srv.ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("premium request without bearer status=%d want 401", unauthorized.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/tokenize", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-User-Tier", "premium")
	blocked := httptest.NewRecorder()
	srv.ServeHTTP(blocked, request)
	if blocked.Code != http.StatusNotFound {
		t.Fatalf("premium unknown route status=%d want 404", blocked.Code)
	}
	if backendCalls.Load() != 0 {
		t.Fatalf("backend calls=%d want zero for auth/route rejects", backendCalls.Load())
	}
}

func TestPremiumTierDoesNotInterceptLocalManagement(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	request := httptest.NewRequest(http.MethodGet, "/pig/metrics", nil)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-User-Tier", "premium")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "pig_info{") {
		t.Fatalf("premium local metrics status=%d body=%q", response.Code, response.Body.String())
	}
	if backendCalls.Load() != 0 {
		t.Fatalf("backend calls=%d want zero for local management", backendCalls.Load())
	}
}

func TestBasicTierRemainsTPSProtected(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", TPSReference: 50, Waiting: 4, WindowConcurrency: 4,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"model-agnostic","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`,
	))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Tier", "basic")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	decision := runtime.Snapshot(clock.Now()).Report.LastDecision
	if response.Code != http.StatusTooManyRequests || backendCalls.Load() != 0 ||
		decision.Reason != coreadmission.ReasonTPSReference {
		t.Fatalf("basic request bypassed TPS protection: status=%d calls=%d decision=%+v",
			response.Code, backendCalls.Load(), decision)
	}
}
