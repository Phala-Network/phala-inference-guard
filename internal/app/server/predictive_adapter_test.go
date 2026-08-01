package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type adapterTestRenderer struct {
	mu    sync.Mutex
	calls int
	err   error
}

type adapterTestCounter struct {
	mu         sync.Mutex
	calls      int
	manifestID string
	epoch      string
	delay      time.Duration
}

type adapterTestClock struct {
	mu  sync.Mutex
	now time.Time
}

type adapterTestUpstreamState struct {
	mu         sync.Mutex
	healthy    bool
	checks     int
	closeCalls int
}

type blockingAdmissionCoordinator struct {
	entered    chan struct{}
	release    chan struct{}
	terminated chan runtimepredictive.TerminalCause
	enterOnce  sync.Once
}

func newBlockingAdmissionCoordinator() *blockingAdmissionCoordinator {
	return &blockingAdmissionCoordinator{
		entered:    make(chan struct{}),
		release:    make(chan struct{}),
		terminated: make(chan runtimepredictive.TerminalCause, 1),
	}
}

func (c *blockingAdmissionCoordinator) DecideAndReserve(_ time.Time, _ runtimepredictive.CountAdmissionProposal) runtimepredictive.CountAdmissionResult {
	c.enterOnce.Do(func() { close(c.entered) })
	<-c.release
	return runtimepredictive.CountAdmissionResult{
		Decision: domainpredictive.Decision{Reason: domainpredictive.ReasonFit},
		Prediction: runtimepredictive.SchedulerPrediction{
			Identity: adapterTestIdentity(),
		},
		Reserved: true,
	}
}

func (c *blockingAdmissionCoordinator) MarkPrefillComplete(string) bool { return false }

func (c *blockingAdmissionCoordinator) MarkForwarded(string) bool { return true }

func (c *blockingAdmissionCoordinator) Terminate(_ string, cause runtimepredictive.TerminalCause) bool {
	c.terminated <- cause
	return true
}

func (c *blockingAdmissionCoordinator) ObserveOutcome(string, runtimepredictive.SchedulerOutcome) bool {
	return false
}

func (c *blockingAdmissionCoordinator) TerminateWithOutcome(_ string, cause runtimepredictive.TerminalCause, _ *runtimepredictive.SchedulerOutcome) bool {
	c.terminated <- cause
	return true
}

type semanticTTFTPredictiveReservation interface {
	ObserveSemanticTTFT(time.Duration) bool
}

func (r *adapterTestRenderer) Render(_ context.Context, input predictiveShadowInput) (predictiveRenderedRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if r.err != nil {
		return predictiveRenderedRequest{}, r.err
	}
	class := runtimepredictive.RequestClassChat
	if input.Path == "/v1/completions" {
		class = runtimepredictive.RequestClassCompletion
	}
	decode := int64(16)
	if input.HasOutputTokens {
		decode = int64(input.OutputTokens)
	}
	return predictiveRenderedRequest{
		Class:              class,
		Rendered:           append([]byte("rendered:"), input.Body...),
		DecodeHorizonUpper: decode,
		Confidence:         1,
	}, nil
}

func (r *adapterTestRenderer) Calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

func (c *adapterTestCounter) Count(_ context.Context, _ runtimepredictive.RequestClass, _ []byte) (runtimepredictive.TokenCountAnalysis, error) {
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return runtimepredictive.TokenCountAnalysis{
		ManifestID:       c.manifestID,
		BackendEpoch:     c.epoch,
		ExactInputTokens: 4,
	}, nil
}

func TestPredictiveEnforceChargesAccruedLocalAdmissionLatencyBeforeForward(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	counter := newAdapterTestCounter()
	counter.delay = 25 * time.Millisecond
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Counter:     counter,
		Coordinator: newAdapterTestCoordinatorWithTargets(t, 0, 20*time.Millisecond),
	})
	if err != nil {
		t.Fatalf("new real predictive admission: %v", err)
	}
	cfg := testProxyConfig(backend.URL)
	cfg.GlobalLimit = 0
	cfg.PredictiveAdmissionMode = "enforce"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return adapter, nil },
	})
	if err != nil {
		t.Fatalf("new proxy server: %v", err)
	}
	defer srv.Close()

	body := `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("status/backend calls = %d/%d, want accrued local TTFT reject 429/0", recorder.Code, backendCalls)
	}
	if snapshot := adapter.Snapshot(); snapshot.LastReason != domainpredictive.ReasonTTFTAtRisk || snapshot.Risks != 1 {
		t.Fatalf("predictive attempt = %+v, want pre-forward TTFT risk", snapshot)
	}
}

func (c *adapterTestCounter) Calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func (c *adapterTestCounter) Close() error {
	return nil
}

func (c *adapterTestClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *adapterTestClock) Advance(elapsed time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(elapsed)
	c.mu.Unlock()
}

func (s *adapterTestUpstreamState) Healthy(time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checks++
	return s.healthy
}

func (s *adapterTestUpstreamState) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	return nil
}

func (s *adapterTestUpstreamState) SetHealthy(healthy bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.healthy = healthy
}

func (s *adapterTestUpstreamState) Snapshot() (checks, closeCalls int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checks, s.closeCalls
}

func TestRealPredictiveShadowRunsExactTransactionBeforeQoS(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	clock := &adapterTestClock{now: time.Unix(100, 0)}
	renderer := &adapterTestRenderer{}
	counter := newAdapterTestCounter()
	coordinator := newAdapterTestCoordinator(t)
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Counter:     counter,
		Coordinator: coordinator,
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatalf("new real predictive shadow: %v", err)
	}

	cfg := testProxyConfig(backend.URL)
	cfg.GlobalLimit = 0
	cfg.PredictiveAdmissionMode = "shadow"
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return adapter, nil },
	})
	if err != nil {
		t.Fatalf("new proxy server: %v", err)
	}
	defer srv.Close()

	body := `{"model":"m","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("response/backend calls = %d/%d, want 429/0", recorder.Code, backendCalls)
	}
	if renderer.Calls() != 1 || counter.Calls() != 1 {
		t.Fatalf("renderer/counter calls = %d/%d, want 1/1", renderer.Calls(), counter.Calls())
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Manager.Reservations != 0 || snapshot.Manager.EventSequence != 2 {
		t.Fatalf("post-local-reject coordinator snapshot = %+v", snapshot)
	}
}

func TestRealPredictiveShadowRequiresFreshUpstreamStateBeforeCounting(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(200, 0)}
	counter := newAdapterTestCounter()
	upstream := &adapterTestUpstreamState{}
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Counter:     counter,
		Coordinator: newAdapterTestCoordinatorWithTPSTarget(t, 0),
		Upstream:    upstream,
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	input := predictiveShadowInput{Path: "/v1/chat/completions", Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	if reservation := adapter.DecideAndReserve(context.Background(), "stale", input); reservation != nil {
		reservation.Terminate(runtimepredictive.TerminalExpired)
		t.Fatal("stale upstream state received predictive headroom")
	}
	if counter.Calls() != 0 {
		t.Fatalf("stale upstream state performed %d tokenizer calls, want 0", counter.Calls())
	}
	upstream.SetHealthy(true)
	reservation := adapter.DecideAndReserve(context.Background(), "fresh", input)
	if reservation == nil {
		t.Fatalf("fresh upstream state did not reach count-only reservation: %+v", adapter.Snapshot())
	}
	if !reservation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("fresh reservation did not terminate")
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("close adapter: %v", err)
	}
	checks, closeCalls := upstream.Snapshot()
	if checks < 2 || closeCalls != 1 {
		t.Fatalf("upstream checks/close calls = %d/%d, want at least 2/1", checks, closeCalls)
	}
}

func TestRealPredictiveShadowRejectsUnsupportedBeforeAnalysis(t *testing.T) {
	renderer := &adapterTestRenderer{err: fmt.Errorf("unsupported request features")}
	counter := newAdapterTestCounter()
	coordinator := newAdapterTestCoordinator(t)
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Counter:     counter,
		Coordinator: coordinator,
	})
	if err != nil {
		t.Fatalf("new real predictive shadow: %v", err)
	}

	reservation := adapter.DecideAndReserve(context.Background(), "unsupported", predictiveShadowInput{
		Path: "/v1/chat/completions",
		Body: []byte(`{"messages":[{"role":"user","content":[{"type":"image_url"}]}]}`),
	})
	if reservation != nil || renderer.Calls() != 1 || counter.Calls() != 0 {
		t.Fatalf("unsupported reservation/render/count = %T/%d/%d", reservation, renderer.Calls(), counter.Calls())
	}
	attempt := adapter.Snapshot()
	if attempt.Attempts != 1 || attempt.Unknown != 1 || attempt.LastReason != domainpredictive.ReasonTokenizerProfileUnknown {
		t.Fatalf("unsupported attempt = %+v", attempt)
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.EventSequence != 0 || snapshot.Manager.Reservations != 0 {
		t.Fatalf("unsupported request mutated coordinator = %+v", snapshot)
	}
}

func TestRealPredictiveShadowTelemetryCoversPredictionLearningAndReservations(t *testing.T) {
	coordinator, scheduler := newAdapterTestCoordinatorAndSchedulerWithTargets(t, 0, time.Second)
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Counter:     newAdapterTestCounter(),
		Coordinator: coordinator,
		Learner:     scheduler,
	})
	if err != nil {
		t.Fatalf("new real predictive shadow: %v", err)
	}
	reservation := adapter.DecideAndReserve(context.Background(), "telemetry", predictiveShadowInput{
		Path: "/v1/chat/completions",
		Body: []byte(`{"messages":[{"role":"user","content":"telemetry"}]}`),
	})
	if reservation == nil || !reservation.MarkForwarded() || !reservation.Terminate(runtimepredictive.TerminalUpstreamFailure) {
		t.Fatal("telemetry reservation did not complete the forwarded censored lifecycle")
	}
	snapshot := adapter.PredictiveAdmissionTelemetry()
	if snapshot.Attempts.Attempts != 1 || snapshot.Attempts.Fits != 1 || snapshot.Attempts.Risks != 0 || snapshot.Attempts.Unknown != 0 {
		t.Fatalf("telemetry attempts = %+v", snapshot.Attempts)
	}
	if snapshot.Manager.Reservations != 0 || snapshot.Learning.SamplesAccepted != 1 || snapshot.Learning.SamplesRejected != 0 {
		t.Fatalf("telemetry manager/learning = %+v/%+v", snapshot.Manager, snapshot.Learning)
	}
	if got := snapshot.PredictionDuration.Sample().Count; got != 1 {
		t.Fatalf("prediction duration count = %d, want 1", got)
	}
	if got := snapshot.RendererDuration.Sample().Count; got != 1 {
		t.Fatalf("renderer duration count = %d, want 1", got)
	}
	if got := snapshot.TokenizerDuration.Sample().Count; got != 1 {
		t.Fatalf("tokenizer duration count = %d, want 1", got)
	}
}

func TestRealPredictiveShadowCloseDoesNotWaitForPredictionAndRollsBackLateReservation(t *testing.T) {
	coordinator := newBlockingAdmissionCoordinator()
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Counter:     newAdapterTestCounter(),
		Coordinator: coordinator,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	decisionDone := make(chan predictiveShadowReservation, 1)
	go func() {
		decisionDone <- adapter.DecideAndReserve(context.Background(), "late", predictiveShadowInput{
			Path: "/v1/chat/completions",
			Body: []byte(`{"messages":[{"role":"user","content":"late"}]}`),
		})
	}()
	<-coordinator.entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- adapter.Close() }()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("close adapter: %v", err)
		}
	case <-time.After(time.Second):
		close(coordinator.release)
		<-decisionDone
		<-closeDone
		t.Fatal("adapter close waited for scheduler prediction while holding its lifecycle lock")
	}

	close(coordinator.release)
	if reservation := <-decisionDone; reservation != nil {
		reservation.Terminate(runtimepredictive.TerminalExpired)
		t.Fatal("prediction that completed after close escaped as a live reservation")
	}
	select {
	case cause := <-coordinator.terminated:
		if cause != runtimepredictive.TerminalExpired {
			t.Fatalf("late reservation rollback cause = %s, want %s", cause, runtimepredictive.TerminalExpired)
		}
	case <-time.After(time.Second):
		t.Fatal("prediction that completed after close was not rolled back")
	}
}

func TestRealPredictiveShadowLearnsCompletedAttributedSemanticTTFTBeforeNextDecision(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(2_000, 0)}
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Counter:     newAdapterTestCounter(),
		Coordinator: newAdapterTestCoordinatorWithTargets(t, 0, 15*time.Millisecond),
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	input := predictiveShadowInput{
		Path: "/v1/chat/completions",
		Body: []byte(`{"messages":[{"role":"user","content":"same feature cell"}]}`),
	}
	for index := 0; index < 3; index++ {
		requestID := fmt.Sprintf("semantic-training-%d", index)
		reservation := adapter.DecideAndReserve(context.Background(), requestID, input)
		if reservation == nil {
			t.Fatalf("training reservation %d was rejected: %+v", index, adapter.Snapshot())
		}
		if !reservation.MarkForwarded() {
			t.Fatalf("training reservation %d was not marked forwarded", index)
		}
		semantic, ok := reservation.(semanticTTFTPredictiveReservation)
		if !ok {
			reservation.Terminate(runtimepredictive.TerminalExpired)
			t.Fatal("real reservation does not expose attributed semantic TTFT observation")
		}
		if !semantic.ObserveSemanticTTFT(100 * time.Millisecond) {
			t.Fatalf("training semantic TTFT %d was not accepted", index)
		}
		if !reservation.Terminate(runtimepredictive.TerminalCompleted) {
			t.Fatalf("training reservation %d did not complete", index)
		}
		clock.Advance(time.Second)
	}

	final := adapter.DecideAndReserve(context.Background(), "semantic-final", input)
	if final != nil {
		final.Terminate(runtimepredictive.TerminalExpired)
		t.Fatal("completed attributed semantic TTFT did not protect the next request")
	}
	attempt := adapter.Snapshot()
	if attempt.LastReason != domainpredictive.ReasonTTFTAtRisk || attempt.LastSource != runtimepredictive.PredictionSourceCalibrated || attempt.LastSamples != 3 {
		t.Fatalf("post-learning decision = %+v, want calibrated TTFT risk from three samples", attempt)
	}
}

func TestRealPredictiveShadowDoesNotLearnSemanticTTFTFromFailedRequests(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(3_000, 0)}
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Counter:     newAdapterTestCounter(),
		Coordinator: newAdapterTestCoordinatorWithTargets(t, 0, 15*time.Millisecond),
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}

	input := predictiveShadowInput{
		Path: "/v1/chat/completions",
		Body: []byte(`{"messages":[{"role":"user","content":"failed feature cell"}]}`),
	}
	for index := 0; index < 3; index++ {
		reservation := adapter.DecideAndReserve(context.Background(), fmt.Sprintf("failed-training-%d", index), input)
		if reservation == nil {
			t.Fatalf("failed training reservation %d was rejected", index)
		}
		if !reservation.MarkForwarded() {
			t.Fatalf("failed training reservation %d was not marked forwarded", index)
		}
		semantic, ok := reservation.(semanticTTFTPredictiveReservation)
		if !ok {
			reservation.Terminate(runtimepredictive.TerminalExpired)
			t.Fatal("real reservation does not expose attributed semantic TTFT observation")
		}
		if !semantic.ObserveSemanticTTFT(100 * time.Millisecond) {
			t.Fatalf("failed training semantic TTFT %d was not recorded", index)
		}
		if !reservation.Terminate(runtimepredictive.TerminalUpstreamFailure) {
			t.Fatalf("failed training reservation %d did not terminate", index)
		}
		clock.Advance(time.Second)
	}

	final := adapter.DecideAndReserve(context.Background(), "failed-final", input)
	if final == nil {
		t.Fatalf("failed requests contaminated future admission: %+v", adapter.Snapshot())
	}
	defer final.Terminate(runtimepredictive.TerminalExpired)
	if attempt := adapter.Snapshot(); attempt.LastReason != domainpredictive.ReasonFit || attempt.LastSource != runtimepredictive.PredictionSourceStatic {
		t.Fatalf("post-failure decision = %+v, want unchanged static fit", attempt)
	}
}

func TestRealPredictiveShadowRecordsForwardedTerminalsWithoutReliableTargetAsCensored(t *testing.T) {
	for _, cause := range []runtimepredictive.TerminalCause{
		runtimepredictive.TerminalCompleted,
		runtimepredictive.TerminalClientCancelled,
		runtimepredictive.TerminalClientDisconnected,
		runtimepredictive.TerminalUpstreamFailure,
		runtimepredictive.TerminalTimeout,
	} {
		t.Run(string(cause), func(t *testing.T) {
			clock := &adapterTestClock{now: time.Unix(3_500, 0)}
			coordinator, scheduler := newAdapterTestCoordinatorAndSchedulerWithTargets(t, 0, time.Second)
			adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
				Renderer:    &adapterTestRenderer{},
				Counter:     newAdapterTestCounter(),
				Coordinator: coordinator,
				Now:         clock.Now,
			})
			if err != nil {
				t.Fatalf("new adapter: %v", err)
			}
			reservation := adapter.DecideAndReserve(context.Background(), "censored-"+string(cause), predictiveShadowInput{
				Path: "/v1/chat/completions",
				Body: []byte(`{"messages":[{"role":"user","content":"censored terminal"}]}`),
			})
			if reservation == nil || !reservation.MarkForwarded() {
				t.Fatalf("forwarded reservation for %s was not created", cause)
			}
			clock.Advance(time.Second)
			if !reservation.Terminate(cause) {
				t.Fatalf("forwarded reservation for %s did not terminate", cause)
			}
			if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 1 || snapshot.SamplesRejected != 0 {
				t.Fatalf("forwarded %s terminal learner snapshot = %+v, want one accepted censored sample", cause, snapshot)
			}
		})
	}
}

func TestRealPredictiveShadowDoesNotCensorUnforwardedLocalReject(t *testing.T) {
	clock := &adapterTestClock{now: time.Unix(3_600, 0)}
	coordinator, scheduler := newAdapterTestCoordinatorAndSchedulerWithTargets(t, 0, time.Second)
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Counter:     newAdapterTestCounter(),
		Coordinator: coordinator,
		Now:         clock.Now,
	})
	if err != nil {
		t.Fatalf("new adapter: %v", err)
	}
	reservation := adapter.DecideAndReserve(context.Background(), "unforwarded-local", predictiveShadowInput{
		Path: "/v1/chat/completions",
		Body: []byte(`{"messages":[{"role":"user","content":"local reject"}]}`),
	})
	if reservation == nil || !reservation.Terminate(runtimepredictive.TerminalLocalQoSReject) {
		t.Fatal("unforwarded local reservation did not terminate")
	}
	if snapshot := scheduler.Snapshot(); snapshot.SamplesAccepted != 0 || snapshot.SamplesRejected != 0 {
		t.Fatalf("unforwarded local reject changed learner state: %+v", snapshot)
	}
}

func newAdapterTestCounter() *adapterTestCounter {
	return &adapterTestCounter{
		manifestID: "adapter-test-manifest",
		epoch:      "adapter-test-epoch",
	}
}

func newAdapterTestCoordinator(t *testing.T) *runtimepredictive.CountCoordinator {
	return newAdapterTestCoordinatorWithTPSTarget(t, 80)
}

func newAdapterTestCoordinatorWithTPSTarget(t *testing.T, userTPSTarget float64) *runtimepredictive.CountCoordinator {
	return newAdapterTestCoordinatorWithTargets(t, userTPSTarget, time.Second)
}

func newAdapterTestCoordinatorWithTargets(t *testing.T, userTPSTarget float64, ttftSLO time.Duration) *runtimepredictive.CountCoordinator {
	coordinator, _ := newAdapterTestCoordinatorAndSchedulerWithTargets(t, userTPSTarget, ttftSLO)
	return coordinator
}

func newAdapterTestCoordinatorAndSchedulerWithTargets(t *testing.T, userTPSTarget float64, ttftSLO time.Duration) (*runtimepredictive.CountCoordinator, *runtimepredictive.LearnedScheduler) {
	t.Helper()
	identity := adapterTestIdentity()
	scheduler, err := runtimepredictive.NewLearnedScheduler(runtimepredictive.StaticSchedulerProfile{
		Identity:                      identity,
		BaseCompletionTPS:             100,
		PrefillTPSPenaltyPerKToken:    0,
		BaseTTFT:                      10 * time.Millisecond,
		TTFTPerUncachedPrefillToken:   0,
		BaseTPOT:                      10 * time.Millisecond,
		TPOTPerExistingDecodeSequence: 0,
		WorkspaceRiskUpper:            0,
		PreemptionRiskUpper:           0,
		Confidence:                    1,
	}, runtimepredictive.ResidualCalibratorConfig{
		Identity:                 identity,
		MinimumSamples:           3,
		MaximumSamplesPerCell:    8,
		MaximumCells:             64,
		MaxAge:                   time.Hour,
		LowerQuantile:            0.1,
		UpperQuantile:            0.9,
		MinimumTPSMultiplier:     0.2,
		MaximumTPSMultiplier:     1,
		MinimumLatencyMultiplier: 1,
		MaximumLatencyMultiplier: 2,
		CalibratedConfidence:     1,
		DecodeSequenceBucket:     1,
		ContextTokenBucket:       1,
		PrefillTokenBucket:       1,
		KVTokenBucket:            1,
	})
	if err != nil {
		t.Fatalf("new learned scheduler: %v", err)
	}
	coordinator, err := runtimepredictive.NewCountCoordinator(runtimepredictive.CountCoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID:   "adapter-test-manifest",
			BackendEpoch: "adapter-test-epoch",
			Scheduler:    identity,
			BlockSize:    4,
		},
		ModelMaximumLength: 262_144,
		Constraints: domainpredictive.Constraints{
			PhysicalKVHard:       1_000,
			ActiveKVHard:         1_000,
			UserTPSTarget:        userTPSTarget,
			TTFTSLO:              ttftSLO,
			TPOTSLO:              time.Second,
			WorkspaceRiskBudget:  1,
			PreemptionRiskBudget: 1,
			MinimumConfidence:    1,
		},
		Scheduler: scheduler,
	})
	if err != nil {
		t.Fatalf("new coordinator: %v", err)
	}
	return coordinator, scheduler
}

func adapterTestIdentity() runtimepredictive.ModelIdentity {
	return runtimepredictive.ModelIdentity{
		ProfileID:        "adapter-test-profile",
		BackendEpoch:     "adapter-test-epoch",
		PredictorVersion: "adapter-test-predictor-v1",
	}
}
