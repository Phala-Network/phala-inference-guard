package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	infrabackend "github.com/Phala-Network/phala-inference-guard/internal/infra/backend"
)

// Older SGLang health_generate waits for a one-second polling tick before
// returning success. A small scheduling margin reproduces that real timing.
func TestReadinessHealthDelayHasExplicitDeadline(t *testing.T) {
	for _, tc := range []struct {
		name, seconds string
		want          int
	}{
		{"default_remains_one_second", "", http.StatusServiceUnavailable},
		{"legacy_health_explicit_five_seconds", "5", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BACKEND_READINESS_PATH", "/health")
			t.Setenv("BACKEND_READINESS_TIMEOUT_SECONDS", tc.seconds)
			cfg, err := loadConfig()
			if err != nil {
				t.Fatal(err)
			}
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/health" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				select {
				case <-time.After(1100 * time.Millisecond):
					w.WriteHeader(http.StatusOK)
				case <-r.Context().Done():
				}
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
			srv := &proxyServer{cfg: cfg, backend: proxies[0], admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{BackendKind: "sglang", Capacity: capacity}}}
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
			if response.Code != tc.want {
				t.Fatalf("legacy polling health: status=%d want=%d", response.Code, tc.want)
			}
		})
	}
}

func TestReadinessConfiguredTimeoutAndParentCancellation(t *testing.T) {
	for _, parentCancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "bounded_five_seconds", true: "parent_cancellation"}[parentCancel], func(t *testing.T) {
			t.Setenv("BACKEND_READINESS_PATH", "/health")
			t.Setenv("BACKEND_READINESS_TIMEOUT_SECONDS", "5")
			cfg, err := loadConfig()
			if err != nil {
				t.Fatal(err)
			}
			entered, canceled := make(chan struct{}), make(chan struct{})
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-r.Context().Done()
				close(canceled)
			}))
			defer backend.Close()
			proxies, _, _, err := infrabackend.Build([]infrabackend.Config{{Upstream: backend.URL}})
			if err != nil {
				t.Fatal(err)
			}
			srv := &proxyServer{cfg: cfg, backend: proxies[0]}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan bool, 1)
			started := time.Now()
			go func() { done <- srv.backendHealthy(ctx, "sglang") }()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("probe never reached backend")
			}
			if parentCancel {
				cancel()
			}
			select {
			case healthy := <-done:
				if healthy {
					t.Fatal("hanging probe reported healthy")
				}
			case <-time.After(7 * time.Second):
				t.Fatal("configured deadline was not bounded")
			}
			elapsed := time.Since(started)
			if !parentCancel && elapsed < 4500*time.Millisecond {
				t.Fatal("five-second probe reverted to the one-second default")
			}
			if parentCancel && elapsed > time.Second {
				t.Fatal("parent cancellation was not propagated")
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("backend request context was not canceled")
			}
		})
	}
}

func TestReadinessExplicitTimeoutPreservesHTTPBoundaries(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusNoContent, http.StatusTemporaryRedirect, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			t.Setenv("BACKEND_READINESS_PATH", "/health")
			t.Setenv("BACKEND_READINESS_TIMEOUT_SECONDS", "5")
			cfg, err := loadConfig()
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/health" || r.URL.RawQuery != "" || r.Method != http.MethodGet || r.Header.Get("Authorization") != "" || r.Header.Get("X-Caller-Private") != "" {
					t.Error("readiness request changed its HTTP or caller-header boundary")
				}
				w.Header().Set("Location", "/must-not-follow")
				w.WriteHeader(status)
			}))
			defer backend.Close()
			proxies, _, _, err := infrabackend.Build([]infrabackend.Config{{Upstream: backend.URL + "/ignored?private=ignored"}})
			if err != nil {
				t.Fatal(err)
			}
			srv := &proxyServer{cfg: cfg, backend: proxies[0]}
			if got := srv.backendHealthy(context.Background(), "sglang"); got != (status == http.StatusOK) {
				t.Fatalf("status=%d health=%v", status, got)
			}
			if calls != 1 {
				t.Fatalf("probe followed a redirect: %d backend calls", calls)
			}
		})
	}
}
