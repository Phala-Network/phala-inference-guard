package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	"github.com/Phala-Network/phala-inference-guard/internal/infra/prometheus"
)

// Exercise the production factory, not only a retry helper. A timeout must not
// escape to Run while the backend is loading, and readiness must need no restart.
func TestDefaultAdmissionWaitsPastStartupTimeoutThenRecovers(t *testing.T) {
	for _, unavailable := range []string{"503", "incomplete"} {
		t.Run(unavailable, func(t *testing.T) {
			var ready atomic.Bool
			metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !ready.Load() {
					if unavailable == "503" {
						w.WriteHeader(http.StatusServiceUnavailable)
					} else {
						_, _ = fmt.Fprintln(w, "sglang:num_running_reqs 0")
					}
					return
				}
				writeStartupSGLangMetrics(w)
			}))
			defer metrics.Close()
			defer ready.Store(true)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := testProxyConfig(metrics.URL)
			cfg.PredictiveStartupProbeTimeout = 80 * time.Millisecond
			cfg.PredictiveMetricsRequestTimeout = 40 * time.Millisecond
			cfg.PredictiveObservationPollInterval = 10 * time.Millisecond
			cfg.PredictiveRunningLimitConfigured = true
			done := make(chan error, 1)
			go func() {
				service, err := newDefaultAdmissionServiceContext(ctx, cfg)
				if service != nil {
					_ = service.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("factory returned before upstream readiness: %v", err)
			case <-time.After(3 * cfg.PredictiveStartupProbeTimeout):
			}
			ready.Store(true)
			select {
			case err := <-done:
				if err != nil {
					t.Fatalf("factory did not recover after upstream became ready: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("factory failed to initialize after readiness")
			}
		})
	}
}

func TestStartupWaitRefusedConnectionThenRecovers(t *testing.T) {
	for _, kind := range []string{"sglang", "vllm"} {
		t.Run(kind, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			address := listener.Addr().String()
			_ = listener.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cfg := predictiveBackendStartupProbeConfig{
				MetricsURL: "http://" + address + "/metrics", StartupTimeout: 60 * time.Millisecond,
				RequestTimeout: 30 * time.Millisecond, RetryInterval: 10 * time.Millisecond,
			}
			done := make(chan error, 1)
			go func() {
				startup, err := waitPredictiveBackendStartup(ctx, cfg)
				if err == nil && startup.BackendKind != kind {
					err = fmt.Errorf("unexpected backend kind %s", startup.BackendKind)
				}
				done <- err
			}()
			select {
			case err := <-done:
				t.Fatalf("refused upstream ended startup: %v", err)
			case <-time.After(3 * cfg.StartupTimeout):
			}
			listener, err = net.Listen("tcp", address)
			if err != nil {
				t.Fatal(err)
			}
			metrics := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if kind == "sglang" {
					writeStartupSGLangMetrics(w)
				} else {
					writeAdmissionVLLMTPSMetrics(w, "vendor/startup-model", 1, 0)
				}
			}))
			_ = metrics.Listener.Close()
			metrics.Listener = listener
			metrics.Start()
			defer metrics.Close()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("startup did not recover after listener returned")
			}
		})
	}
}

func TestStartupWaitCancellationStopsFetchAndRetry(t *testing.T) {
	for _, mode := range []string{"fetch", "retry"} {
		t.Run(mode, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			var calls atomic.Int64
			metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				select {
				case entered <- struct{}{}:
				default:
				}
				if mode == "fetch" {
					<-r.Context().Done()
				} else {
					w.WriteHeader(http.StatusServiceUnavailable)
				}
			}))
			defer metrics.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				_, err := waitPredictiveBackendStartup(ctx, predictiveBackendStartupProbeConfig{
					MetricsURL: metrics.URL, StartupTimeout: time.Minute,
					RequestTimeout: time.Minute, RetryInterval: 250 * time.Millisecond,
				})
				done <- err
			}()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("no startup probe")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("startup cancellation did not interrupt wait")
			}
			before := calls.Load()
			time.Sleep(30 * time.Millisecond)
			if calls.Load() != before {
				t.Fatal("startup continued fetching after cancellation")
			}
		})
	}
}

func TestStartupWaitInvalidConfigAndPreCanceledMakeNoCalls(t *testing.T) {
	var calls atomic.Int64
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeStartupSGLangMetrics(w)
	}))
	defer metrics.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	valid := predictiveBackendStartupProbeConfig{
		MetricsURL: metrics.URL, StartupTimeout: time.Second,
		RequestTimeout: time.Second, RetryInterval: time.Millisecond,
	}
	if _, err := waitPredictiveBackendStartup(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled startup: %v", err)
	}
	for _, invalid := range []predictiveBackendStartupProbeConfig{
		{}, {MetricsURL: "file:///metrics", StartupTimeout: time.Second, RequestTimeout: time.Second, RetryInterval: time.Millisecond},
		{MetricsURL: metrics.URL, StartupTimeout: time.Second, RequestTimeout: 2 * time.Second, RetryInterval: time.Millisecond},
	} {
		if _, err := waitPredictiveBackendStartup(context.Background(), invalid); err == nil {
			t.Fatal("invalid configuration accepted")
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid/pre-canceled startup contacted backend")
	}
}

func TestStartupFactoryCancellationInterruptsOptionalRunningLimit(t *testing.T) {
	entered := make(chan struct{}, 1)
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/server_info" {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		writeStartupSGLangMetrics(w)
	}))
	defer metrics.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := testProxyConfig(metrics.URL)
	cfg.PredictiveStartupProbeTimeout = time.Minute
	cfg.PredictiveMetricsRequestTimeout = time.Minute
	done := make(chan error, 1)
	go func() {
		service, err := newDefaultAdmissionServiceContext(ctx, cfg)
		if service != nil {
			_ = service.Close()
		}
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("optional discovery never began")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("optional discovery cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("optional discovery ignored startup cancellation")
	}
}

func TestStartupSGLangFactoryRuntimeOutageRecoversWithoutRestart(t *testing.T) {
	var unavailable atomic.Bool
	var calls atomic.Int64
	metrics := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		if unavailable.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		writeStartupSGLangMetrics(w)
	}))
	defer metrics.Close()
	cfg := testProxyConfig(metrics.URL)
	cfg.PredictiveRunningLimitConfigured = true
	cfg.PredictiveObservationPollInterval = 10 * time.Millisecond
	cfg.PredictiveMaximumMetricsAge = 30 * time.Millisecond
	service, err := newDefaultAdmissionService(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	waitAdmissionCondition(t, func() bool { return service.Snapshot(time.Now()).Capacity.Available })
	unavailable.Store(true)
	waitAdmissionCondition(t, func() bool {
		snapshot := service.Snapshot(time.Now()).Capacity
		return !snapshot.Available && snapshot.MinimumDecision.Reason == coreadmission.ReasonObservationStale
	})
	failedCalls := calls.Load()
	unavailable.Store(false)
	waitAdmissionCondition(t, func() bool {
		snapshot := service.Snapshot(time.Now()).Capacity
		return snapshot.Available && snapshot.IntakeOpen && calls.Load() > failedCalls
	})
	if service.Snapshot(time.Now()).Capacity.State.ResidualDebts != 0 {
		t.Fatal("startup/outage without requests created residual debt")
	}
}

func writeStartupSGLangMetrics(w http.ResponseWriter) {
	_, _ = fmt.Fprint(w, `
# TYPE sglang:max_total_num_tokens gauge
# TYPE sglang:page_size gauge
# TYPE sglang:num_pages gauge
# TYPE sglang:num_running_reqs gauge
# TYPE sglang:num_queue_reqs gauge
# TYPE sglang:num_retracted_requests_total counter
# TYPE sglang:realtime_tokens_total counter
sglang:max_total_num_tokens{model_name="vendor/startup-model",engine_type="unified"} 100000
sglang:page_size{model_name="vendor/startup-model",engine_type="unified"} 1
sglang:num_pages{model_name="vendor/startup-model",engine_type="unified"} 100000
sglang:num_running_reqs{model_name="vendor/startup-model",engine_type="unified"} 0
sglang:num_queue_reqs{model_name="vendor/startup-model",engine_type="unified"} 0
sglang:num_retracted_requests_total{model_name="vendor/startup-model",engine_type="unified"} 0
sglang:realtime_tokens_total{model_name="vendor/startup-model",engine_type="unified",mode="decode"} 1
`)
}

func TestStartupSGLangMetricsFixtureIsCoherent(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeStartupSGLangMetrics(recorder)
	startup, err := predictiveBackendStartupFromSample(prometheus.ParseSample(recorder.Body.String()), time.Now())
	if err != nil || startup.BackendKind != "sglang" || startup.Generation != 1 {
		t.Fatalf("invalid startup fixture: startup=%+v err=%v", startup, err)
	}
}
