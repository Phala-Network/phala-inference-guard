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

func TestPriorityTierUsesAdmissionAndPreservesRequestBodyContract(t *testing.T) {
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
		Mode: "enforce", BackendKind: "sglang", TPSReference: 0, Running: 0, Waiting: 0,
		WindowConcurrency: 1, RunningLimit: 1,
		RunningLimitSource: coreadmission.RunningLimitSourceAdmin,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
		`{"model":"m","messages":[{"role":"user","content":"hello"}]}`,
	))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-User-Tier", "priority")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	report := runtime.Snapshot(clock.Now()).Report
	if response.Code != http.StatusOK || backendCalls.Load() != 1 || report.Attempts != 1 ||
		!report.HasLastDecision || report.LastDecision.Demand.Tier != coreadmission.TierPriority {
		t.Fatalf("priority request did not use normal admission: status=%d calls=%d attempts=%d has_last_decision=%t decision=%+v body=%q",
			response.Code, backendCalls.Load(), report.Attempts, report.HasLastDecision, report.LastDecision, response.Body.String())
	}
	if reserved := srv.requestClassifier.ReservedBodyBytes(); reserved != 0 {
		t.Fatalf("priority request left scanner reservation=%d", reserved)
	}
	if got := seenBody.Load(); got != `{"model":"m","messages":[{"role":"user","content":"hello"}],"priority":2}` {
		t.Fatalf("priority body=%q want original body plus the scheduling priority", got)
	}
}

func TestPriorityTierCannotBypassAuthOrRoutePolicy(t *testing.T) {
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
	request.Header.Set("X-User-Tier", "priority")
	unauthorized := httptest.NewRecorder()
	srv.ServeHTTP(unauthorized, request)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("priority request without bearer status=%d want 401", unauthorized.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/v1/tokenize", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("X-User-Tier", "priority")
	blocked := httptest.NewRecorder()
	srv.ServeHTTP(blocked, request)
	if blocked.Code != http.StatusNotFound {
		t.Fatalf("priority unknown route status=%d want 404", blocked.Code)
	}
	if backendCalls.Load() != 0 {
		t.Fatalf("backend calls=%d want zero for auth/route rejects", backendCalls.Load())
	}
}

func TestPriorityTierDoesNotInterceptLocalManagement(t *testing.T) {
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
	request.Header.Set("X-User-Tier", "priority")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "pig_dynamic_observed_running ") {
		t.Fatalf("priority local metrics status=%d body=%q", response.Code, response.Body.String())
	}
	if backendCalls.Load() != 0 {
		t.Fatalf("backend calls=%d want zero for local management", backendCalls.Load())
	}
}

func TestFlexTierRemainsTPSProtected(t *testing.T) {
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
	for _, header := range []string{"flex", " FLEX "} {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
			`{"model":"model-agnostic","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`,
		))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-User-Tier", header)
		response := httptest.NewRecorder()

		srv.ServeHTTP(response, request)

		decision := runtime.Snapshot(clock.Now()).Report.LastDecision
		if response.Code != http.StatusTooManyRequests || backendCalls.Load() != 0 ||
			decision.Reason != coreadmission.ReasonTPSReference || decision.Demand.Tier != coreadmission.TierFlex {
			t.Fatalf("tier header %q bypassed TPS protection: status=%d calls=%d decision=%+v",
				header, response.Code, backendCalls.Load(), decision)
		}
	}
}

func TestPriorityTierBypassesNonTPSProtectionAndOtherTiersDoNot(t *testing.T) {
	for header, tier := range map[string]coreadmission.RequestTier{
		"priority": coreadmission.TierPriority,
		"default":  coreadmission.TierDefault,
		"":         coreadmission.TierDefault,
		"premium":  coreadmission.TierDefault,
		"basic":    coreadmission.TierDefault,
	} {
		var backendCalls atomic.Int64
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			backendCalls.Add(1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		}))
		defer backend.Close()
		runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
			Mode: "enforce", TPSReference: 50, Running: 1, Waiting: 1,
			WindowConcurrency: 1, RunningLimit: 1,
			RunningLimitSource: coreadmission.RunningLimitSourceAdmin,
		})
		srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(
			`{"model":"m","messages":[{"role":"user","content":"hello"}]}`,
		))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		if header != "" {
			request.Header.Set("X-User-Tier", header)
		}
		response := httptest.NewRecorder()

		srv.ServeHTTP(response, request)

		decision := runtime.Snapshot(clock.Now()).Report.LastDecision
		if decision.Demand.Tier != tier {
			t.Fatalf("header %q parsed as %s, want %s", header, decision.Demand.Tier, tier)
		}
		if tier == coreadmission.TierPriority {
			if response.Code != http.StatusOK || backendCalls.Load() != 1 || !decision.Admitted() {
				t.Fatalf("%s was blocked by non-TPS protection: status=%d calls=%d decision=%+v", header, response.Code, backendCalls.Load(), decision)
			}
			continue
		}
		// Every other header value is resold traffic: the running limit
		// applies to it exactly as it does to flex.
		if response.Code != http.StatusTooManyRequests || backendCalls.Load() != 0 ||
			decision.Reason != coreadmission.ReasonRunningLimit {
			t.Fatalf("header %q bypassed the running limit: status=%d calls=%d decision=%+v", header, response.Code, backendCalls.Load(), decision)
		}
	}
}

func TestBackendTierSchedulingWritesTierPriorityIntoForwardedBody(t *testing.T) {
	type seen struct {
		body          string
		contentLength int64
	}
	var last atomic.Value
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		last.Store(seen{body: string(body), contentLength: r.ContentLength})
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", BackendKind: "sglang", WindowConcurrency: 8, RunningLimit: 8,
		RunningLimitSource: coreadmission.RunningLimitSourceAdmin,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	original := `{"model":"m","priority":7,"messages":[{"role":"user","content":"hello"}]}`
	for header, want := range map[string]string{
		"priority": `,"priority":2}`, "default": `,"priority":1}`, "flex": `,"priority":0}`, "": `,"priority":1}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(original))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		if header != "" {
			request.Header.Set("X-User-Tier", header)
		}
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		got, _ := last.Load().(seen)
		if response.Code != http.StatusOK || !strings.HasSuffix(got.body, want) ||
			got.contentLength != int64(len(got.body)) {
			t.Fatalf("tier %q status=%d body=%q content_length=%d", header, response.Code, got.body, got.contentLength)
		}
	}
	metrics := httptest.NewRecorder()
	srv.writeSchedulingPriorityMetrics(metrics)
	if !strings.Contains(metrics.Body.String(), `pig_predictive_backend_tier_scheduling_requests_total{tier="priority",result="written"} 1`) {
		t.Fatalf("metrics=%s", metrics.Body.String())
	}
}
