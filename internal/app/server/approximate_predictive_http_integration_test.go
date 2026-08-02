package server

import (
	"errors"
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

const approximateHTTPStreamingUsageResponse = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\n" +
	"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":5},\"metrics\":{\"mean_itl_ms\":10}}\n\n" +
	"data: [DONE]\n\n"

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

func TestApproximatePredictiveHTTPUpstreamTerminalReleasesBeforeSlowDownstream(t *testing.T) {
	firstUpstreamFinished := make(chan struct{})
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(approximateHTTPStreamingUsageResponse))
		if backendCalls.Add(1) == 1 {
			close(firstUpstreamFinished)
		}
	}))
	defer backend.Close()
	adapter, scheduler := newApproximateHTTPTestAdapter(t, 20)
	srv := newApproximateHTTPTestServer(t, backend.URL, adapter)
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close predictive server: %v", err)
		}
	}()

	releaseDownstream := make(chan struct{})
	writer := &blockingPredictiveResponseWriter{
		header:  make(http.Header),
		blocked: make(chan struct{}),
		release: releaseDownstream,
	}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		srv.ServeHTTP(writer, newApproximateStreamingHTTPRequest())
	}()
	downstreamReleased := false
	defer func() {
		if !downstreamReleased {
			close(releaseDownstream)
		}
		select {
		case <-firstDone:
		case <-time.After(time.Second):
			t.Errorf("slow-downstream request did not finish during cleanup")
		}
	}()

	select {
	case <-firstUpstreamFinished:
	case <-time.After(time.Second):
		t.Fatal("first upstream did not emit a terminal response")
	}
	select {
	case <-writer.blocked:
	case <-time.After(time.Second):
		t.Fatal("first downstream writer did not block")
	}

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager.Reservations == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 0 {
		t.Fatalf("upstream terminal retained resource reservation behind slow downstream: %+v", snapshot)
	}

	second := serveApproximateStreamingHTTPRequest(srv)
	if second.Code != http.StatusOK {
		t.Fatalf("safe request after upstream terminal = %d body=%q, want admitted before slow downstream returns", second.Code, second.Body.String())
	}
	if got := backendCalls.Load(); got != 2 {
		t.Fatalf("safe request after upstream terminal did not reach backend: calls=%d", got)
	}

	close(releaseDownstream)
	downstreamReleased = true
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first request did not finish after downstream release")
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 0 {
		t.Fatalf("slow-downstream lifecycle leaked or resurrected a resource reservation: %+v", snapshot)
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 2 {
		t.Fatalf("qualified slow-downstream outcomes learned %d times, want exactly 2", snapshot.SamplesAccepted)
	}
}

func TestApproximatePredictiveHTTPNonStreamTerminalReleasesBeforeSlowDownstream(t *testing.T) {
	firstUpstreamFinished := make(chan struct{})
	var backendCalls atomic.Int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(approximateHTTPUsageResponse))
		if backendCalls.Add(1) == 1 {
			close(firstUpstreamFinished)
		}
	}))
	defer backend.Close()
	adapter, scheduler := newApproximateHTTPTestAdapter(t, 20)
	srv := newApproximateHTTPTestServer(t, backend.URL, adapter)
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close predictive server: %v", err)
		}
	}()

	releaseDownstream := make(chan struct{})
	writer := &blockingPredictiveResponseWriter{
		header:  make(http.Header),
		blocked: make(chan struct{}),
		release: releaseDownstream,
	}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		srv.ServeHTTP(writer, newApproximateHTTPRequest())
	}()
	downstreamReleased := false
	defer func() {
		if !downstreamReleased {
			close(releaseDownstream)
		}
		select {
		case <-firstDone:
		case <-time.After(time.Second):
			t.Errorf("slow non-stream request did not finish during cleanup")
		}
	}()

	select {
	case <-firstUpstreamFinished:
	case <-time.After(time.Second):
		t.Fatal("first non-stream upstream did not emit a terminal response")
	}
	select {
	case <-writer.blocked:
	case <-time.After(time.Second):
		t.Fatal("first non-stream downstream writer did not block")
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		if adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager.Reservations == 0 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 0 {
		t.Fatalf("non-stream upstream terminal retained resource reservation behind slow downstream: %+v", snapshot)
	}

	second := serveApproximateHTTPRequest(srv)
	if second.Code != http.StatusOK {
		t.Fatalf("safe non-stream request after upstream terminal = %d body=%q, want admitted", second.Code, second.Body.String())
	}
	if got := backendCalls.Load(); got != 2 {
		t.Fatalf("safe non-stream request after upstream terminal did not reach backend: calls=%d", got)
	}
	close(releaseDownstream)
	downstreamReleased = true
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("first non-stream request did not finish after downstream release")
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 0 {
		t.Fatalf("non-stream slow-downstream lifecycle leaked or resurrected a reservation: %+v", snapshot)
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 2 {
		t.Fatalf("qualified non-stream slow-downstream outcomes learned %d times, want exactly 2", snapshot.SamplesAccepted)
	}
}

func TestApproximatePredictiveHTTPTruncatedSSEDoesNotReleaseBeforeSlowDownstream(t *testing.T) {
	payload := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":5},\"metrics\":{\"mean_itl_ms\":10}}\n\n"
	upstreamReturned := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(upstreamReturned)
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "4096")
		_, _ = w.Write([]byte(payload))
	}))
	defer backend.Close()
	adapter, scheduler := newApproximateHTTPTestAdapter(t, 20)
	srv := newApproximateHTTPTestServer(t, backend.URL, adapter)
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close predictive server: %v", err)
		}
	}()

	releaseDownstream := make(chan struct{})
	writer := &blockingPredictiveResponseWriter{
		header:  make(http.Header),
		blocked: make(chan struct{}),
		release: releaseDownstream,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(writer, newApproximateStreamingHTTPRequest())
	}()
	downstreamReleased := false
	defer func() {
		if !downstreamReleased {
			close(releaseDownstream)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Errorf("truncated slow-downstream request did not finish during cleanup")
		}
	}()
	select {
	case <-upstreamReturned:
	case <-time.After(time.Second):
		t.Fatal("truncated upstream did not return")
	}
	select {
	case <-writer.blocked:
	case <-time.After(time.Second):
		t.Fatal("truncated response did not reach the slow downstream")
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 1 {
		t.Fatalf("truncated SSE released before downstream completion: %+v", snapshot)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Released != 0 {
		t.Fatalf("truncated SSE created premature deferred outcome: %+v", snapshot)
	}
	close(releaseDownstream)
	downstreamReleased = true
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("truncated request did not finish after downstream release")
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 0 {
		t.Fatalf("truncated SSE leaked final resource accounting: %+v", snapshot)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Qualified != 0 {
		t.Fatalf("truncated SSE retained or qualified deferred outcome: %+v", snapshot)
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 0 {
		t.Fatalf("truncated SSE trained %d scheduler samples", snapshot.SamplesAccepted)
	}
}

func TestApproximatePredictiveHTTPTruncatedNonStreamDoesNotReleaseBeforeSlowDownstream(t *testing.T) {
	upstreamReturned := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		defer close(upstreamReturned)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", "4096")
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

	releaseDownstream := make(chan struct{})
	writer := &blockingPredictiveResponseWriter{
		header:  make(http.Header),
		blocked: make(chan struct{}),
		release: releaseDownstream,
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(writer, newApproximateHTTPRequest())
	}()
	downstreamReleased := false
	defer func() {
		if !downstreamReleased {
			close(releaseDownstream)
		}
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Errorf("truncated non-stream request did not finish during cleanup")
		}
	}()
	select {
	case <-upstreamReturned:
	case <-time.After(time.Second):
		t.Fatal("truncated non-stream upstream did not return")
	}
	select {
	case <-writer.blocked:
	case <-time.After(time.Second):
		t.Fatal("truncated non-stream response did not reach the slow downstream")
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 1 {
		t.Fatalf("truncated non-stream released before downstream completion: %+v", snapshot)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Released != 0 {
		t.Fatalf("truncated non-stream created premature deferred outcome: %+v", snapshot)
	}
	close(releaseDownstream)
	downstreamReleased = true
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("truncated non-stream request did not finish after downstream release")
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 0 {
		t.Fatalf("truncated non-stream leaked final resource accounting: %+v", snapshot)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Qualified != 0 {
		t.Fatalf("truncated non-stream retained or qualified deferred outcome: %+v", snapshot)
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 0 {
		t.Fatalf("truncated non-stream trained %d scheduler samples", snapshot.SamplesAccepted)
	}
}

func TestApproximatePredictiveHTTPDownstreamWriteErrorAfterResourceReleaseIsCensored(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(approximateHTTPStreamingUsageResponse))
	}))
	defer backend.Close()
	adapter, scheduler := newApproximateHTTPTestAdapter(t, 20)
	srv := newApproximateHTTPTestServer(t, backend.URL, adapter)
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close predictive server: %v", err)
		}
	}()
	writer := &errorPredictiveResponseWriter{
		header: make(http.Header),
		err:    errors.New("injected downstream write failure"),
	}
	srv.ServeHTTP(writer, newApproximateStreamingHTTPRequest())
	if !writer.wrote.Load() {
		t.Fatal("downstream write failure was not exercised")
	}
	if snapshot := adapter.coordinator.(*runtimepredictive.CountCoordinator).Snapshot().Manager; snapshot.Reservations != 0 {
		t.Fatalf("downstream write failure leaked manager resources: %+v", snapshot)
	}
	if snapshot := adapter.PredictiveAdmissionTelemetry().DeferredOutcomes; snapshot.Active != 0 || snapshot.Released != 1 || snapshot.Terminated != 1 || snapshot.Censored != 1 || snapshot.Qualified != 0 {
		t.Fatalf("downstream write failure deferred telemetry = %+v", snapshot)
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 0 {
		t.Fatalf("downstream write failure trained %d scheduler samples", snapshot.SamplesAccepted)
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
	metricsInput := srv.predictiveAdmissionMetricsInput()
	for name, value := range map[string]*durationHistogram{
		"prediction": metricsInput.PredictionDuration,
		"estimator":  metricsInput.EstimatorDuration,
	} {
		if value == nil {
			t.Fatalf("%s histogram is nil", name)
		}
		sample := value.Sample()
		for _, required := range []float64{0.00025, 0.001} {
			found := false
			for _, bucket := range sample.Buckets {
				if bucket.UpperBound == required {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("%s histogram lacks required sub-ms bucket %g: %+v", name, required, sample.Buckets)
			}
		}
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
			TPOTSLO:           time.Duration(float64(time.Second) / targetTPS),
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
	request := newApproximateHTTPRequest()
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	return recorder
}

func newApproximateHTTPRequest() *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"arbitrary/model","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func newApproximateStreamingHTTPRequest() *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"arbitrary/model","messages":[{"role":"user","content":"hello"}],"stream":true,"stream_options":{"include_usage":true},"max_tokens":8}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream")
	return request
}

func serveApproximateStreamingHTTPRequest(srv *proxyServer) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, newApproximateStreamingHTTPRequest())
	return recorder
}

type blockingPredictiveResponseWriter struct {
	header  http.Header
	blocked chan struct{}
	release <-chan struct{}
	wrote   atomic.Bool
}

func (w *blockingPredictiveResponseWriter) Header() http.Header {
	return w.header
}

func (w *blockingPredictiveResponseWriter) WriteHeader(int) {}

func (w *blockingPredictiveResponseWriter) Write(body []byte) (int, error) {
	if w.wrote.CompareAndSwap(false, true) {
		close(w.blocked)
		<-w.release
	}
	return len(body), nil
}

type errorPredictiveResponseWriter struct {
	header http.Header
	err    error
	wrote  atomic.Bool
}

func (w *errorPredictiveResponseWriter) Header() http.Header { return w.header }

func (*errorPredictiveResponseWriter) WriteHeader(int) {}

func (w *errorPredictiveResponseWriter) Write([]byte) (int, error) {
	w.wrote.Store(true)
	return 0, w.err
}
