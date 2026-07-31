package server

import (
	"context"
	"crypto/sha256"
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

type adapterTestAnalyzer struct {
	mu         sync.Mutex
	calls      int
	manifestID string
	epoch      string
	blockSize  int
}

type adapterTestClock struct {
	mu  sync.Mutex
	now time.Time
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

func (a *adapterTestAnalyzer) Analyze(_ context.Context, _ runtimepredictive.RequestClass, rendered []byte, _ runtimepredictive.RequestFeatures) (runtimepredictive.TokenBlockAnalysis, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls++
	digest := sha256.Sum256(rendered)
	return runtimepredictive.TokenBlockAnalysis{
		ManifestID:       a.manifestID,
		BackendEpoch:     a.epoch,
		BlockSize:        a.blockSize,
		ExactInputTokens: int64(a.blockSize),
		FullBlockDigests: []runtimepredictive.CacheBlockDigest{runtimepredictive.CacheBlockDigest(digest)},
	}, nil
}

func (a *adapterTestAnalyzer) Calls() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
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

func TestRealPredictiveShadowRunsExactTransactionBeforeQoS(t *testing.T) {
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	clock := &adapterTestClock{now: time.Unix(100, 0)}
	renderer := &adapterTestRenderer{}
	analyzer := newAdapterTestAnalyzer()
	coordinator := newAdapterTestCoordinator(t)
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Analyzer:    analyzer,
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
	if renderer.Calls() != 1 || analyzer.Calls() != 1 {
		t.Fatalf("renderer/analyzer calls = %d/%d, want 1/1", renderer.Calls(), analyzer.Calls())
	}
	snapshot := coordinator.Snapshot()
	if snapshot.Manager.Reservations != 0 || snapshot.Cache.Requests != 0 || snapshot.Manager.EventSequence != 2 {
		t.Fatalf("post-local-reject coordinator snapshot = %+v", snapshot)
	}
}

func TestRealPredictiveShadowRejectsUnsupportedBeforeAnalysis(t *testing.T) {
	renderer := &adapterTestRenderer{err: fmt.Errorf("unsupported request features")}
	analyzer := newAdapterTestAnalyzer()
	coordinator := newAdapterTestCoordinator(t)
	adapter, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    renderer,
		Analyzer:    analyzer,
		Coordinator: coordinator,
	})
	if err != nil {
		t.Fatalf("new real predictive shadow: %v", err)
	}

	reservation := adapter.DecideAndReserve(context.Background(), "unsupported", predictiveShadowInput{
		Path: "/v1/chat/completions",
		Body: []byte(`{"messages":[{"role":"user","content":[{"type":"image_url"}]}]}`),
	})
	if reservation != nil || renderer.Calls() != 1 || analyzer.Calls() != 0 {
		t.Fatalf("unsupported reservation/render/analyze = %T/%d/%d", reservation, renderer.Calls(), analyzer.Calls())
	}
	attempt := adapter.Snapshot()
	if attempt.Attempts != 1 || attempt.Unknown != 1 || attempt.LastReason != domainpredictive.ReasonTokenizerProfileUnknown {
		t.Fatalf("unsupported attempt = %+v", attempt)
	}
	if snapshot := coordinator.Snapshot(); snapshot.Manager.EventSequence != 0 || snapshot.Manager.Reservations != 0 || snapshot.Cache.Requests != 0 {
		t.Fatalf("unsupported request mutated coordinator = %+v", snapshot)
	}
}

func TestRealPredictiveShadowEligibleHistoryChangesPreForwardDecision(t *testing.T) {
	finalTime := time.Unix(1_000, 0)
	coldClock := &adapterTestClock{now: finalTime}
	cold, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Analyzer:    newAdapterTestAnalyzer(),
		Coordinator: newAdapterTestCoordinator(t),
		Now:         coldClock.Now,
	})
	if err != nil {
		t.Fatalf("new cold adapter: %v", err)
	}

	trainedClock := &adapterTestClock{now: finalTime.Add(-3 * time.Second)}
	trained, err := newRealPredictiveShadow(realPredictiveShadowConfig{
		Renderer:    &adapterTestRenderer{},
		Analyzer:    newAdapterTestAnalyzer(),
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
			Identity:             identity,
			ObservedAt:           trainedClock.Now().Add(time.Millisecond),
			Attributed:           true,
			ExistingUserTPS:      100,
			ExistingUserTPSValid: true,
			AllUserTPS:           40,
			AllUserTPSValid:      true,
			TTFT:                 10 * time.Millisecond,
			TTFTValid:            true,
			TPOT:                 10 * time.Millisecond,
			TPOTValid:            true,
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

func newAdapterTestAnalyzer() *adapterTestAnalyzer {
	return &adapterTestAnalyzer{
		manifestID: "adapter-test-manifest",
		epoch:      "adapter-test-epoch",
		blockSize:  4,
	}
}

func newAdapterTestCoordinator(t *testing.T) *runtimepredictive.Coordinator {
	return newAdapterTestCoordinatorWithTPSTarget(t, 80)
}

func newAdapterTestCoordinatorWithTPSTarget(t *testing.T, userTPSTarget float64) *runtimepredictive.Coordinator {
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
	coordinator, err := runtimepredictive.NewCoordinator(runtimepredictive.CoordinatorConfig{
		Identity: runtimepredictive.CoordinatorIdentity{
			ManifestID:   "adapter-test-manifest",
			BackendEpoch: "adapter-test-epoch",
			Scheduler:    identity,
			BlockSize:    4,
		},
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
		Scheduler:           scheduler,
		CacheCapacityBlocks: 128,
		CacheHashKey:        []byte("0123456789abcdef0123456789abcdef"),
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
