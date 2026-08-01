package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

const approximateHTTPUsageResponse = `{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":12,"completion_tokens":5},"metrics":{"mean_itl_ms":10}}`

func TestApproximatePredictiveHTTPColdTPSRiskRejectsBeforeUpstream(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := backendCalls.Add(1)
		if call == 1 {
			close(firstEntered)
			<-releaseFirst
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(approximateHTTPUsageResponse))
	}))
	defer backend.Close()
	adapter, _ := newApproximateHTTPTestAdapter(t, 20)
	srv := newApproximateHTTPTestServer(t, backend.URL, adapter)
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close predictive server: %v", err)
		}
	}()

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- serveApproximateHTTPRequest(srv) }()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first cold-safe request did not reach upstream")
	}
	second := serveApproximateHTTPRequest(srv)
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("cold concurrent status = %d body=%q, want pre-forward 429", second.Code, second.Body.String())
	}
	if got := backendCalls.Load(); got != 1 {
		t.Fatalf("cold TPS-risk request reached upstream: calls=%d", got)
	}
	close(releaseFirst)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first cold-safe response status = %d body=%q", first.Code, first.Body.String())
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot(); snapshot.Manager.Reservations != 0 {
		t.Fatalf("post-cold HTTP lifecycle leaked reservation: %+v", snapshot.Manager)
	}
}

func TestApproximatePredictiveHTTPShadowTPSRiskForwardsAndLearnsWithoutAccountingReservation(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := backendCalls.Add(1)
		if call == 1 {
			close(firstEntered)
			<-releaseFirst
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(approximateHTTPUsageResponse))
	}))
	defer backend.Close()
	adapter, _ := newApproximateHTTPTestAdapterWithMode(t, 20, "shadow")
	srv := newApproximateHTTPTestServerWithMode(t, backend.URL, adapter, "shadow")
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close predictive shadow server: %v", err)
		}
	}()

	firstDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { firstDone <- serveApproximateHTTPRequest(srv) }()
	select {
	case <-firstEntered:
	case <-time.After(time.Second):
		t.Fatal("first shadow request did not reach upstream")
	}
	second := serveApproximateHTTPRequest(srv)
	if second.Code != http.StatusOK {
		close(releaseFirst)
		<-firstDone
		t.Fatalf("shadow TPS-risk status = %d body=%q, want unchanged upstream response", second.Code, second.Body.String())
	}
	if got := backendCalls.Load(); got != 2 {
		close(releaseFirst)
		<-firstDone
		t.Fatalf("shadow TPS-risk request did not reach upstream: calls=%d", got)
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.ShadowObservations.Active != 0 || telemetry.ShadowObservations.Created != 1 || telemetry.ShadowObservations.Terminated != 1 || telemetry.ShadowObservations.Qualified != 1 {
		close(releaseFirst)
		<-firstDone
		t.Fatalf("qualified shadow HTTP observation telemetry = %+v", telemetry.ShadowObservations)
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 1 {
		close(releaseFirst)
		<-firstDone
		t.Fatalf("shadow-only request changed accounting reservations: %+v", snapshot)
	}
	close(releaseFirst)
	if first := <-firstDone; first.Code != http.StatusOK {
		t.Fatalf("first shadow response status = %d body=%q", first.Code, first.Body.String())
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 0 {
		t.Fatalf("shadow HTTP lifecycle leaked reservation: %+v", snapshot)
	}
}

func TestApproximatePredictiveHTTPQualifiedTPSHeadroomAdmitsNextConcurrency(t *testing.T) {
	fourthEntered := make(chan struct{})
	releaseFourth := make(chan struct{})
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := backendCalls.Add(1)
		if call == 4 {
			close(fourthEntered)
			<-releaseFourth
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(approximateHTTPUsageResponse))
	}))
	defer backend.Close()
	adapter, scheduler := newApproximateHTTPTestAdapter(t, 20)
	srv := newApproximateHTTPTestServer(t, backend.URL, adapter)
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close predictive server: %v", err)
		}
	}()

	for index := 0; index < 3; index++ {
		response := serveApproximateHTTPRequest(srv)
		if response.Code != http.StatusOK {
			t.Fatalf("qualified training response %d = %d body=%q", index, response.Code, response.Body.String())
		}
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 3 || snapshot.GlobalSamples != 3 {
		t.Fatalf("qualified TPS learning snapshot = %+v", snapshot)
	}

	fourthDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { fourthDone <- serveApproximateHTTPRequest(srv) }()
	select {
	case <-fourthEntered:
	case <-time.After(time.Second):
		t.Fatal("learned first concurrent request did not reach upstream")
	}
	fifth := serveApproximateHTTPRequest(srv)
	if fifth.Code != http.StatusOK {
		close(releaseFourth)
		<-fourthDone
		t.Fatalf("learned second concurrent status = %d body=%q, want admitted", fifth.Code, fifth.Body.String())
	}
	if got := backendCalls.Load(); got != 5 {
		close(releaseFourth)
		<-fourthDone
		t.Fatalf("learned headroom did not reach upstream: calls=%d", got)
	}
	close(releaseFourth)
	if fourth := <-fourthDone; fourth.Code != http.StatusOK {
		t.Fatalf("blocked learned response status = %d body=%q", fourth.Code, fourth.Body.String())
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot(); snapshot.Manager.Reservations != 0 {
		t.Fatalf("post-learned HTTP lifecycle leaked reservation: %+v", snapshot.Manager)
	}
}

func TestApproximatePredictiveHTTPEstimatorTelemetryWithoutLegacyKVShadow(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(approximateHTTPUsageResponse))
	}))
	defer backend.Close()
	adapter, _ := newApproximateHTTPTestAdapter(t, 20)
	srv := newApproximateHTTPTestServer(t, backend.URL, adapter)
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close predictive server: %v", err)
		}
	}()
	if srv.kvShadow != nil {
		t.Fatal("test requires legacy KV shadow to be disabled")
	}

	response := serveApproximateHTTPRequest(srv)
	if response.Code != http.StatusOK {
		t.Fatalf("predictive request status = %d body=%q", response.Code, response.Body.String())
	}
	if sample := srv.kvEstimatorDuration.Sample(); sample.Count != 1 {
		t.Fatalf("predictive estimator histogram count = %d, want 1 without legacy KV shadow", sample.Count)
	}
}

func newApproximateHTTPTestAdapter(t *testing.T, targetTPS float64) (*approximatePredictiveShadow, *runtimepredictive.LearnedScheduler) {
	return newApproximateHTTPTestAdapterWithMode(t, targetTPS, "enforce")
}

func newApproximateHTTPTestAdapterWithMode(t *testing.T, targetTPS float64, mode string) (*approximatePredictiveShadow, *runtimepredictive.LearnedScheduler) {
	t.Helper()
	identity := runtimepredictive.ModelIdentity{
		ProfileID: "approx-http", BackendEpoch: "approx-http-epoch", PredictorVersion: "approx-http-v1",
	}
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity: identity, BaseCompletionTPS: targetTPS,
		BaseTTFT: 100 * time.Millisecond, BaseTPOT: 25 * time.Millisecond,
		TPOTPerExistingDecodeSequence: 25 * time.Millisecond,
		Confidence:                    0.99,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity: identity, MinimumSamples: 3, MaximumSamplesPerCell: 16, MaximumCells: 32,
		MaxAge: time.Minute, LowerQuantile: 0.10, UpperQuantile: 0.90,
		MinimumTPSMultiplier: 0.10, MaximumTPSMultiplier: 8,
		MinimumLatencyMultiplier: 0.25, MaximumLatencyMultiplier: 4,
		CalibratedConfidence: 0.99, DecodeSequenceBucket: 1,
		ContextTokenBucket: 1_024, PrefillTokenBucket: 1_024, KVTokenBucket: 1_024,
	})
	if err != nil {
		t.Fatalf("new approximate HTTP scheduler: %v", err)
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID: "approx-http-manifest", BackendEpoch: identity.BackendEpoch,
			Scheduler: identity, BlockSize: 4,
		},
		ModelMaximumLength: 1_000_000,
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard: 900_000, ActiveKVHard: 900_000, UserTPSTarget: targetTPS,
			TTFTSLO: time.Second, TPOTSLO: time.Duration(float64(time.Second) / targetTPS),
			MinimumConfidence: 0.90,
		},
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatalf("new approximate HTTP coordinator: %v", err)
	}
	calibrator, err := runtimepredictive.NewInputSizeCalibrator(runtimepredictive.InputSizeCalibratorConfig{
		EstimatorVersion: "approx-http-json-v1", MinimumSamples: 3, MaximumSamplesPerClass: 16,
		MaxAge: time.Minute, UpperQuantile: 0.95, SafetyMargin: 1.10,
		MinimumMultiplier: 0.25, MaximumMultiplier: 8,
		ColdConfidence: 0.95, LearnedConfidence: 0.99,
	})
	if err != nil {
		t.Fatalf("new approximate HTTP size calibrator: %v", err)
	}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator: calibrator, Coordinator: coordinator, Learner: scheduler, Mode: mode,
	})
	if err != nil {
		t.Fatalf("new approximate HTTP adapter: %v", err)
	}
	return adapter, scheduler
}

func newApproximateHTTPTestServer(t *testing.T, upstream string, adapter *approximatePredictiveShadow) *proxyServer {
	return newApproximateHTTPTestServerWithMode(t, upstream, adapter, "enforce")
}

func newApproximateHTTPTestServerWithMode(t *testing.T, upstream string, adapter *approximatePredictiveShadow, mode string) *proxyServer {
	t.Helper()
	cfg := testProxyConfig(upstream)
	cfg.PredictiveAdmissionMode = mode
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return adapter, nil },
	})
	if err != nil {
		t.Fatalf("new approximate HTTP proxy: %v", err)
	}
	return srv
}

func serveApproximateHTTPRequest(srv *proxyServer) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"arbitrary/model","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	return recorder
}
