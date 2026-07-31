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

func (c *adapterTestCounter) Count(_ context.Context, _ runtimepredictive.RequestClass, _ []byte, _ runtimepredictive.RequestFeatures) (runtimepredictive.TokenCountAnalysis, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	return runtimepredictive.TokenCountAnalysis{
		ManifestID:       c.manifestID,
		BackendEpoch:     c.epoch,
		ExactInputTokens: 4,
	}, nil
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

func TestRealPredictiveShadowEligibleHistoryChangesPreForwardDecision(t *testing.T) {
	finalTime := time.Unix(1_000, 0)
	coldClock := &adapterTestClock{now: finalTime}
	cold, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Counter:     newAdapterTestCounter(),
		Coordinator: newAdapterTestCoordinator(t),
		Now:         coldClock.Now,
	})
	if err != nil {
		t.Fatalf("new cold adapter: %v", err)
	}

	trainedClock := &adapterTestClock{now: finalTime.Add(-3 * time.Second)}
	trained, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Counter:     newAdapterTestCounter(),
		Coordinator: newAdapterTestCoordinator(t),
		Now:         trainedClock.Now,
	})
	if err != nil {
		t.Fatalf("new trained adapter: %v", err)
	}
	identity := adapterTestIdentity()
	for index := 0; index < 3; index++ {
		requestID := fmt.Sprintf("training-%d", index)
		reservation := trained.DecideAndReserve(context.Background(), requestID, predictiveShadowInput{
			Path: "/v1/chat/completions",
			Body: []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"training-%d"}]}`, index)),
		})
		if reservation == nil {
			t.Fatalf("training reservation %d was rejected", index)
		}
		outcome := runtimepredictive.SchedulerOutcome{
			Identity:        identity,
			ObservedAt:      trainedClock.Now().Add(time.Millisecond),
			Attributed:      true,
			AllUserTPS:      40,
			AllUserTPSValid: true,
			TTFT:            10 * time.Millisecond,
			TTFTValid:       true,
			TPOT:            10 * time.Millisecond,
			TPOTValid:       true,
		}
		if !trained.ObserveOutcome(requestID, outcome) {
			t.Fatalf("training outcome %d was not learned", index)
		}
		if !reservation.Terminate(runtimepredictive.TerminalCompleted) {
			t.Fatalf("training reservation %d did not terminate", index)
		}
		trainedClock.Advance(time.Second)
	}

	input := predictiveShadowInput{
		Path: "/v1/chat/completions",
		Body: []byte(`{"messages":[{"role":"user","content":"same request"}]}`),
	}
	coldReservation := cold.DecideAndReserve(context.Background(), "cold-final", input)
	trainedReservation := trained.DecideAndReserve(context.Background(), "trained-final", input)
	if coldReservation == nil {
		t.Fatal("cold static prediction unexpectedly rejected the final request")
	}
	defer coldReservation.Terminate(runtimepredictive.TerminalExpired)
	if trainedReservation != nil {
		trainedReservation.Terminate(runtimepredictive.TerminalExpired)
		t.Fatal("eligible adverse history did not reject the trained counterfactual")
	}
	coldAttempt := cold.Snapshot()
	trainedAttempt := trained.Snapshot()
	if coldAttempt.LastReason != domainpredictive.ReasonFit || coldAttempt.LastSource != runtimepredictive.PredictionSourceStatic {
		t.Fatalf("cold attempt = %+v", coldAttempt)
	}
	if trainedAttempt.LastReason != domainpredictive.ReasonNewTPSAtRisk || trainedAttempt.LastSource != runtimepredictive.PredictionSourceCalibrated || trainedAttempt.LastSamples != 3 {
		t.Fatalf("trained attempt = %+v", trainedAttempt)
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
			TTFTSLO:              time.Second,
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
	return coordinator
}

func adapterTestIdentity() runtimepredictive.ModelIdentity {
	return runtimepredictive.ModelIdentity{
		ProfileID:        "adapter-test-profile",
		BackendEpoch:     "adapter-test-epoch",
		PredictorVersion: "adapter-test-predictor-v1",
	}
}
