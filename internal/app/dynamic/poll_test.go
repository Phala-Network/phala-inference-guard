package dynamic

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	runtimebackend "github.com/Phala-Network/phala-inference-guard/internal/runtime/backend"
	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

type fakeBackend struct {
	name       string
	metricsURL string
	status     runtimebackend.Runtime
	failures   atomic.Uint64
}

func (b *fakeBackend) Name() string {
	return b.name
}

func (b *fakeBackend) MetricsURL() string {
	return b.metricsURL
}

func (b *fakeBackend) StoreStatus(status runtimebackend.Runtime) {
	b.status = status
}

func (b *fakeBackend) ObserveMetricsFailure() {
	b.failures.Add(1)
}

func (b *fakeBackend) UpdateStatusFromSample(telemetry.Sample) runtimebackend.Runtime {
	status := b.status
	if status.Name == "" {
		status.Name = b.name
	}
	b.status = status
	return status
}

func TestPollBackendMetricsUsesPerBackendNormalizedSignals(t *testing.T) {
	backendA := &fakeBackend{name: "backend-a", status: runtimebackend.Runtime{
		Name:                 "backend-a",
		GenerationTPS:        250,
		GenerationTPSValid:   true,
		PreemptionDelta:      2,
		PreemptionDeltaValid: true,
		Updated:              time.Unix(100, 0),
	}}
	backendB := &fakeBackend{name: "backend-b", status: runtimebackend.Runtime{
		Name:                 "backend-b",
		GenerationTPS:        400,
		GenerationTPSValid:   true,
		PreemptionDelta:      3,
		PreemptionDeltaValid: true,
		Updated:              time.Unix(100, 0),
	}}
	serverA := backendMetricsServer(t, 3, 0, 10, 10)
	defer serverA.Close()
	serverB := backendMetricsServer(t, 4, 0, 20, 20)
	defer serverB.Close()
	backendA.metricsURL = serverA.URL
	backendB.metricsURL = serverB.URL

	controller := New(testDynamicConfig(), Dependencies{
		Backends: []Backend{backendA, backendB},
		GlobalLimit: func() int {
			return 100
		},
	})
	controller.pollBackendMetrics(serverA.Client())
	snapshot := controller.Snapshot()

	if !snapshot.GenerationTPSValid {
		t.Fatalf("generation TPS valid = false, want true")
	}
	if snapshot.GenerationTPS != 650 {
		t.Fatalf("generation TPS = %.1f, want 650", snapshot.GenerationTPS)
	}
	if snapshot.CapacityTPS != 650 {
		t.Fatalf("capacity TPS = %.1f, want 650", snapshot.CapacityTPS)
	}
	if snapshot.Preemptions != 30 {
		t.Fatalf("raw preemptions = %d, want 30", snapshot.Preemptions)
	}
	if !containsString(snapshot.RedReasons, "preemptions") {
		t.Fatalf("red reasons = %v, want preemptions from normalized delta", snapshot.RedReasons)
	}
	if snapshot.BackendCount != 2 || snapshot.BackendFailed != 0 {
		t.Fatalf("backend count/failed = %d/%d, want 2/0", snapshot.BackendCount, snapshot.BackendFailed)
	}
}

func TestPollBackendMetricsAllowsEmptyURLPartialFailure(t *testing.T) {
	healthy := &fakeBackend{name: "healthy"}
	missingURL := &fakeBackend{name: "missing-url"}
	server := backendMetricsServer(t, 3, 0, 10, 10)
	defer server.Close()
	healthy.metricsURL = server.URL

	controller := New(testDynamicConfig(), Dependencies{
		Backends: []Backend{healthy, missingURL},
		GlobalLimit: func() int {
			return 100
		},
	})
	controller.pollBackendMetrics(server.Client())
	snapshot := controller.Snapshot()

	if snapshot.Source != "metrics" {
		t.Fatalf("snapshot source = %q, want metrics", snapshot.Source)
	}
	if snapshot.BackendCount != 2 || snapshot.BackendFailed != 1 {
		t.Fatalf("backend count/failed = %d/%d, want 2/1", snapshot.BackendCount, snapshot.BackendFailed)
	}
	if !missingURL.status.Failed || missingURL.status.Error != "metrics_url_empty" {
		t.Fatalf("missing URL status = failed %t error %q, want metrics_url_empty failure", missingURL.status.Failed, missingURL.status.Error)
	}
	if missingURL.failures.Load() != 0 {
		t.Fatalf("missing URL failure counter = %d, want 0", missingURL.failures.Load())
	}
}

func TestPollBackendMetricsAllowsFetchPartialFailure(t *testing.T) {
	healthy := &fakeBackend{name: "healthy"}
	failed := &fakeBackend{name: "failed"}
	healthyServer := backendMetricsServer(t, 3, 0, 10, 10)
	defer healthyServer.Close()
	failedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	defer failedServer.Close()
	healthy.metricsURL = healthyServer.URL
	failed.metricsURL = failedServer.URL

	controller := New(testDynamicConfig(), Dependencies{
		Backends: []Backend{healthy, failed},
		GlobalLimit: func() int {
			return 100
		},
	})
	controller.pollBackendMetrics(healthyServer.Client())
	snapshot := controller.Snapshot()

	if snapshot.Source != "metrics" {
		t.Fatalf("snapshot source = %q, want metrics", snapshot.Source)
	}
	if snapshot.BackendCount != 2 || snapshot.BackendFailed != 1 {
		t.Fatalf("backend count/failed = %d/%d, want 2/1", snapshot.BackendCount, snapshot.BackendFailed)
	}
	if !failed.status.Failed || failed.status.Error == "" {
		t.Fatalf("failed backend status = failed %t error %q, want fetch failure", failed.status.Failed, failed.status.Error)
	}
	if failed.failures.Load() != 1 {
		t.Fatalf("failed backend failure counter = %d, want 1", failed.failures.Load())
	}
}

func TestPollStaticMetricsURLsAllowsPartialFailure(t *testing.T) {
	healthy := backendMetricsServer(t, 3, 0, 10, 10)
	defer healthy.Close()
	failed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	defer failed.Close()

	cfg := testDynamicConfig()
	cfg.BackendRouting = false
	cfg.MetricsURLs = []string{healthy.URL, failed.URL}
	controller := New(cfg, Dependencies{
		GlobalLimit: func() int {
			return 100
		},
	})

	controller.pollStaticMetricsURLs(healthy.Client())
	snapshot := controller.Snapshot()

	if snapshot.Source != "metrics" {
		t.Fatalf("snapshot source = %q, want metrics", snapshot.Source)
	}
	if snapshot.BackendCount != 2 || snapshot.BackendFailed != 1 {
		t.Fatalf("backend count/failed = %d/%d, want 2/1", snapshot.BackendCount, snapshot.BackendFailed)
	}
	if snapshot.Error != "" {
		t.Fatalf("snapshot error = %q, want empty partial-failure metrics snapshot", snapshot.Error)
	}
}

func backendMetricsServer(t *testing.T, running, waiting int, preemptions, generation uint64) *httptest.Server {
	t.Helper()
	body := fmt.Sprintf(`
vllm:num_requests_running %d
vllm:num_requests_waiting %d
vllm:kv_cache_usage_perc 0.10
vllm:num_preemptions_total %d
vllm:generation_tokens_total %d
`, running, waiting, preemptions, generation)
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
}

func testDynamicConfig() Config {
	return Config{
		Enabled:                   true,
		Enforce:                   true,
		PollInterval:              time.Second,
		FailsafeState:             "yellow",
		BackendRouting:            true,
		KVYellow:                  0.70,
		KVRed:                     0.80,
		WaitingYellow:             1,
		WaitingRed:                2,
		PreemptRed:                1,
		PressureEnabled:           true,
		PressureHeadroom:          1,
		PressureMinLimit:          1,
		PressureLearnRatio:        0.75,
		PressureLearnMinRunning:   4,
		UserTPSEnabled:            true,
		UserTPSYellow:             25,
		UserTPSRed:                20,
		UserTPSMinRun:             1,
		UserTPSYellowN:            1,
		UserTPSRedN:               1,
		UserTPSCapacityRatio:      1,
		UserTPSCapacityRatioMax:   1,
		UserTPSCapacityLearn:      true,
		UserTPSCapacityStepUp:     0.10,
		UserTPSCapacityHealthyN:   2,
		UserTPSCapacityHealthyMul: 1.20,
		GlobalGreen:               100,
		GlobalYellow:              100,
		GlobalRed:                 100,
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
