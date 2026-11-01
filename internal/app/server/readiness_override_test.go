package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	infrabackend "github.com/Phala-Network/phala-inference-guard/internal/infra/backend"
)

func TestReadinessPathOverridePreservesProbeBoundaries(t *testing.T) {
	for _, override := range []string{"/health", "/ready"} {
		for _, status := range []int{200, 503, 307, 204, 0} {
			t.Run(override+"/"+http.StatusText(status), func(t *testing.T) {
				t.Setenv("BACKEND_READINESS_PATH", override)
				cfg, err := loadConfig()
				if err != nil {
					t.Fatal(err)
				}
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != override || r.URL.RawQuery != "" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("X-Caller-Private") != "" {
						t.Errorf("unexpected overridden readiness request: %s %s", r.Method, r.URL.Path)
						w.WriteHeader(http.StatusNotFound)
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
				proxies, _, _, err := infrabackend.Build([]infrabackend.Config{{Upstream: backend.URL + "/ignored-prefix"}})
				if err != nil {
					t.Fatal(err)
				}
				capacity := coreadmission.CapacitySnapshot{
					IntakeOpen: true, HasObservation: true, Available: true,
					Observation:     coreadmission.BackendObservation{ObservedAt: time.Now(), MaximumAge: time.Minute},
					MinimumDecision: coreadmission.DecisionRecord{Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen},
				}
				// The override must win over either automatic backend choice.
				kind := "sglang"
				if override == "/ready" {
					kind = "vllm"
				}
				srv := &proxyServer{cfg: cfg, backend: proxies[0], admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{BackendKind: kind, Capacity: capacity}}}
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
						t.Fatal("overridden probe exceeded its one-second deadline")
					}
				}
			})
		}
	}
}

func TestReadinessOverrideSupportsHealthOnlySGLang(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	proxies, _, _, err := infrabackend.Build([]infrabackend.Config{{Upstream: backend.URL}})
	if err != nil {
		t.Fatal(err)
	}
	for _, override := range []string{"", "/health"} {
		t.Run(override, func(t *testing.T) {
			t.Setenv("BACKEND_READINESS_PATH", override)
			cfg, err := loadConfig()
			if err != nil {
				t.Fatal(err)
			}
			capacity := coreadmission.CapacitySnapshot{
				IntakeOpen: true, HasObservation: true, Available: true,
				Observation:     coreadmission.BackendObservation{ObservedAt: time.Now(), MaximumAge: time.Minute},
				MinimumDecision: coreadmission.DecisionRecord{Action: coreadmission.ActionAdmit, Reason: coreadmission.ReasonOpen},
			}
			srv := &proxyServer{cfg: cfg, backend: proxies[0], admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{BackendKind: "sglang", Capacity: capacity}}}
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			want := http.StatusServiceUnavailable
			if override == "/health" {
				want = http.StatusOK
			}
			if response.Code != want {
				t.Fatalf("health-only backend: status=%d want=%d", response.Code, want)
			}
		})
	}
}
