package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	infrabackend "github.com/Phala-Network/phala-inference-guard/internal/infra/backend"
)

func TestReadinessUsesFreshObservationNotSpareCapacity(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/health" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Caller-Private") != "" {
			t.Errorf("unexpected backend health request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	proxies, _, _, err := infrabackend.Build([]infrabackend.Config{{Upstream: backend.URL}})
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"enforce", "shadow"} {
		for _, test := range []struct {
			name   string
			modify func(*coreadmission.CapacitySnapshot)
			want   int
		}{
			{"healthy", func(c *coreadmission.CapacitySnapshot) {}, http.StatusOK},
			{"full or TPS protected", func(c *coreadmission.CapacitySnapshot) {
				c.Available = false
				c.MinimumDecision = coreadmission.DecisionRecord{Action: coreadmission.ActionProtect, Scope: coreadmission.ProtectionLoad, Reason: coreadmission.ReasonTPSReference}
			}, http.StatusOK},
			{"missing", func(c *coreadmission.CapacitySnapshot) { c.HasObservation = false }, http.StatusServiceUnavailable},
			{"closed", func(c *coreadmission.CapacitySnapshot) { c.IntakeOpen = false }, http.StatusServiceUnavailable},
			{"stale", func(c *coreadmission.CapacitySnapshot) { c.Observation.ObservedAt = time.Now().Add(-time.Minute) }, http.StatusServiceUnavailable},
			{"future", func(c *coreadmission.CapacitySnapshot) { c.Observation.ObservedAt = time.Now().Add(time.Minute) }, http.StatusServiceUnavailable},
			{"invalid controller state", func(c *coreadmission.CapacitySnapshot) {
				c.MinimumDecision = coreadmission.DecisionRecord{Action: coreadmission.ActionProtect, Scope: coreadmission.ProtectionAvailability, Reason: coreadmission.ReasonControllerUnavailable}
			}, http.StatusServiceUnavailable},
			{"invalid decision", func(c *coreadmission.CapacitySnapshot) { c.MinimumDecision = coreadmission.DecisionRecord{} }, http.StatusServiceUnavailable},
		} {
			t.Run(mode+"/"+test.name, func(t *testing.T) {
				capacity := coreadmission.CapacitySnapshot{
					IntakeOpen: true, HasObservation: true, Available: true,
					Observation:     coreadmission.BackendObservation{ObservedAt: time.Now(), MaximumAge: time.Second},
					MinimumDecision: coreadmission.DecisionRecord{Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen},
				}
				test.modify(&capacity)
				srv := &proxyServer{cfg: config{PredictiveAdmissionMode: mode}, backend: proxies[0], admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{Capacity: capacity}}}
				response := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
				request.Header.Set("Authorization", "Bearer test-only-secret")
				request.Header.Set("X-Caller-Private", "test-only-value")
				srv.ServeHTTP(response, request)
				if response.Code != test.want {
					t.Fatalf("status=%d want=%d body=%q", response.Code, test.want, response.Body.String())
				}
				live := httptest.NewRecorder()
				srv.ServeHTTP(live, httptest.NewRequest(http.MethodGet, "/healthz", nil))
				if live.Code != http.StatusOK {
					t.Fatalf("liveness status=%d", live.Code)
				}
			})
		}
	}
}

func TestReadinessRejectsUnhealthyEngineWithFreshMetrics(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusTemporaryRedirect, http.StatusNoContent, http.StatusOK} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if status == http.StatusOK {
					<-r.Context().Done()
					return
				}
				w.Header().Set("Location", "/health")
				w.WriteHeader(status)
			}))
			defer backend.Close()
			proxies, _, _, err := infrabackend.Build([]infrabackend.Config{{Upstream: backend.URL}})
			if err != nil {
				t.Fatal(err)
			}
			capacity := coreadmission.CapacitySnapshot{
				IntakeOpen: true, HasObservation: true, Available: true,
				Observation:     coreadmission.BackendObservation{ObservedAt: time.Now(), MaximumAge: time.Minute},
				MinimumDecision: coreadmission.DecisionRecord{Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen},
			}
			srv := &proxyServer{backend: proxies[0], admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{Capacity: capacity}}}
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if response.Code != http.StatusServiceUnavailable {
				t.Fatalf("status=%d", response.Code)
			}
		})
	}
}

func TestReadinessMissingRuntimeAndCanonicalRoute(t *testing.T) {
	srv := &proxyServer{}
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable || response.Body.String() != "not ready\n" {
		t.Fatalf("missing runtime: status=%d body=%q", response.Code, response.Body.String())
	}
	for _, target := range []string{"/readyz/", "/%72eadyz", "//readyz"} {
		if _, ok := (LocalManagementRoutePolicy{}).Match(httptest.NewRequest(http.MethodGet, target, nil)); ok {
			t.Fatalf("accepted noncanonical readiness path %q", target)
		}
	}
	if _, ok := (LocalManagementRoutePolicy{}).Match(httptest.NewRequest(http.MethodPost, "/readyz", nil)); ok {
		t.Fatal("accepted POST readiness")
	}
}

func TestHealthAliasUsesRealReadiness(t *testing.T) {
	for _, healthy := range []bool{true, false} {
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" || r.Header.Get("Authorization") != "" {
				t.Error("unexpected health request or forwarded authorization")
			}
			if !healthy {
				w.WriteHeader(http.StatusInternalServerError)
			}
		}))
		proxies, _, _, err := infrabackend.Build([]infrabackend.Config{{Upstream: backend.URL}})
		if err != nil {
			t.Fatal(err)
		}
		for _, fresh := range []bool{true, false} {
			capacity := coreadmission.CapacitySnapshot{
				IntakeOpen: true, HasObservation: fresh, Available: true,
				Observation:     coreadmission.BackendObservation{ObservedAt: time.Now(), MaximumAge: time.Minute},
				MinimumDecision: coreadmission.DecisionRecord{Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen},
			}
			srv := &proxyServer{backend: proxies[0], authentication: AuthenticationPolicy{enabled: true}, admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{Capacity: capacity}}}
			for _, path := range []string{"/readyz", "/health"} {
				for _, token := range []string{"", "Bearer test-only-secret"} {
					request := httptest.NewRequest(http.MethodGet, path, nil)
					request.Header.Set("Authorization", token)
					response := httptest.NewRecorder()
					srv.ServeHTTP(response, request)
					want := http.StatusServiceUnavailable
					if healthy && fresh {
						want = http.StatusOK
					}
					if response.Code != want {
						t.Fatalf("path=%s healthy=%t fresh=%t status=%d want=%d", path, healthy, fresh, response.Code, want)
					}
				}
			}
		}
		backend.Close()
	}
	for _, method := range []string{http.MethodHead, http.MethodPost, http.MethodDelete} {
		if _, ok := (LocalManagementRoutePolicy{}).Match(httptest.NewRequest(method, "/health", nil)); ok {
			t.Fatalf("accepted health alias method %s", method)
		}
	}
	for _, path := range []string{"/health/", "/%68ealth", "//health"} {
		if _, ok := (LocalManagementRoutePolicy{}).Match(httptest.NewRequest(http.MethodGet, path, nil)); ok {
			t.Fatalf("accepted noncanonical health alias %s", path)
		}
	}
}

// A ready SGLang engine need not complete its generation-based /health within
// one second. The detected backend kind must select its immediate /ready probe.
func TestReadinessSelectsDetectedBackendProbe(t *testing.T) {
	for _, kind := range []string{"sglang", "vllm", ""} {
		for _, status := range []int{200, 503, 307, 204, 0} {
			t.Run(kind+"/"+http.StatusText(status), func(t *testing.T) {
				wantPath := "/health"
				if kind == "sglang" {
					wantPath = "/ready"
				}
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != wantPath || r.URL.RawQuery != "" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("X-Caller-Private") != "" {
						t.Errorf("unexpected readiness request: %s %s", r.Method, r.URL.Path)
						<-r.Context().Done()
						return
					}
					if status == 0 {
						<-r.Context().Done()
						return
					}
					w.Header().Set("Location", "/redirect-must-not-be-followed")
					w.WriteHeader(status)
				}))
				defer backend.Close()
				proxies, _, _, err := infrabackend.Build([]infrabackend.Config{{Upstream: backend.URL}})
				if err != nil {
					t.Fatal(err)
				}
				capacity := coreadmission.CapacitySnapshot{
					IntakeOpen: true, HasObservation: true, Available: true,
					Observation:     coreadmission.BackendObservation{ObservedAt: time.Now(), MaximumAge: time.Minute},
					MinimumDecision: coreadmission.DecisionRecord{Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen},
				}
				srv := &proxyServer{backend: proxies[0], admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{BackendKind: kind, Capacity: capacity}}}
				for _, path := range []string{"/readyz", "/health"} {
					response := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodGet, path, nil)
					request.Header.Set("Authorization", "Bearer test-only-secret")
					request.Header.Set("X-Caller-Private", "test-only-value")
					started := time.Now()
					srv.ServeHTTP(response, request)
					want := http.StatusServiceUnavailable
					if status == http.StatusOK {
						want = http.StatusOK
					}
					if response.Code != want {
						t.Fatalf("path=%s status=%d want=%d", path, response.Code, want)
					}
					if status == 0 && time.Since(started) > 2*time.Second {
						t.Fatal("backend probe did not honor its one-second deadline")
					}
				}
			})
		}
	}
}
