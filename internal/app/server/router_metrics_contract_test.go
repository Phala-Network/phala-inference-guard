package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
)

func TestPIGMetricsIsExactMinimalRouterContract(t *testing.T) {
	tests := []struct {
		name     string
		capacity coreadmission.CapacitySnapshot
		want     string
	}{
		{
			name: "open",
			capacity: coreadmission.CapacitySnapshot{
				Available: true,
				MinimumDecision: coreadmission.DecisionRecord{
					Action: coreadmission.ActionAdmit,
					Reason: coreadmission.ReasonOpen,
				},
				State: coreadmission.ProjectedState{RawRunning: 2},
			},
			want: "pig_dynamic_observed_running 2\n" +
				"pig_dynamic_observed_waiting 0\n" +
				"pig_dynamic_global_limit 0\n" +
				"pig_predictive_admission_enforce 1\n" +
				"pig_predictive_router_backpressure_applied 0\n",
		},
		{
			name: "protected",
			capacity: coreadmission.CapacitySnapshot{
				MinimumDecision: coreadmission.DecisionRecord{
					Action: coreadmission.ActionProtect,
					Reason: coreadmission.ReasonTPSReference,
					Scope:  coreadmission.ProtectionLoad,
				},
				State: coreadmission.ProjectedState{RawRunning: 3, RawWaiting: 1},
			},
			want: "pig_dynamic_observed_running 3\n" +
				"pig_dynamic_observed_waiting 0\n" +
				"pig_dynamic_global_limit 3\n" +
				"pig_predictive_admission_enforce 1\n" +
				"pig_predictive_router_backpressure_applied 1\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := &proxyServer{
				cfg: config{Token: "secret", PredictiveAdmissionMode: "enforce"},
				admission: &staticAdmissionTelemetryService{snapshot: admissionTelemetrySnapshot{
					Capacity: test.capacity,
				}},
			}
			request := httptest.NewRequest(http.MethodGet, "/pig/metrics", nil)
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()

			srv.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("status=%d want=200 body=%q", response.Code, response.Body.String())
			}
			if got := response.Header().Get("Content-Type"); got != "text/plain; version=0.0.4" {
				t.Fatalf("content-type=%q", got)
			}
			if got := response.Body.String(); got != test.want {
				t.Fatalf("/pig/metrics contract mismatch:\ngot:\n%swant:\n%s", got, test.want)
			}
			if lines := strings.Count(response.Body.String(), "\n"); lines != 5 {
				t.Fatalf("metric lines=%d want=5 body=%q", lines, response.Body.String())
			}
		})
	}
}

func TestCombinedMetricsRetainsFullDiagnostics(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("backend_test_metric 1\n"))
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce", Running: 2})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	request := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
	request.Header.Set("Authorization", "Bearer secret")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d want=200 body=%q", response.Code, response.Body.String())
	}
	for _, required := range []string{
		"pig_info{version=\"PIG-v0.12.26\"} 1\n",
		"pig_predictive_tps_reference ",
		"# --- Backend Metrics ---\n",
		"backend_test_metric 1\n",
	} {
		if !strings.Contains(response.Body.String(), required) {
			t.Fatalf("/v1/metrics missing %q:\n%s", required, response.Body.String())
		}
	}
}

func TestPIGMetricsAuthenticationRemainsRequired(t *testing.T) {
	srv := &proxyServer{
		cfg:       config{Token: "secret", PredictiveAdmissionMode: "enforce"},
		admission: &staticAdmissionTelemetryService{},
	}
	request := httptest.NewRequest(http.MethodGet, "/pig/metrics", nil)
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want=401 body=%q", response.Code, response.Body.String())
	}
}
