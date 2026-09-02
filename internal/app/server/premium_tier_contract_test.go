package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

func TestPremiumTierBypassesTPSProtectionBeforeForward(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
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
	request.Header.Set("X-User-Tier", "premium")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	decision := runtime.Snapshot(clock.Now()).Report.LastDecision
	if response.Code != http.StatusOK || backendCalls.Load() != 1 || !decision.Admitted() {
		t.Fatalf("premium request was not forwarded: status=%d calls=%d decision=%+v body=%q",
			response.Code, backendCalls.Load(), decision, response.Body.String())
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
