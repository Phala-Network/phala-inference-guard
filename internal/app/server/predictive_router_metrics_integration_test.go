package server

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	appdynamic "github.com/Phala-Network/phala-inference-guard/internal/app/dynamic"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

type routerMetricsPredictiveShadow struct {
	telemetry predictiveAdmissionTelemetrySnapshot
}

type togglePredictiveUpstream struct {
	mu      sync.Mutex
	healthy bool
}

type panickingPredictiveUpstream struct{}

func (panickingPredictiveUpstream) Healthy(time.Time) bool {
	panic("injected upstream health panic")
}

func (panickingPredictiveUpstream) Close() error { return nil }

func (s *togglePredictiveUpstream) Healthy(time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.healthy
}

func (*togglePredictiveUpstream) Close() error { return nil }

func (s *togglePredictiveUpstream) SetHealthy(healthy bool) {
	s.mu.Lock()
	s.healthy = healthy
	s.mu.Unlock()
}

func (*routerMetricsPredictiveShadow) Decide(context.Context, string, predictiveShadowInput) predictiveAdmissionDecision {
	return predictiveAdmissionDecision{Outcome: predictiveAdmissionOutcomeForward}
}

func (*routerMetricsPredictiveShadow) Close() error { return nil }

func (s *routerMetricsPredictiveShadow) PredictiveAdmissionTelemetry() predictiveAdmissionTelemetrySnapshot {
	return s.telemetry
}

func TestPredictiveRejectProjectsProtectionIntoRouterConsumedMetrics(t *testing.T) {
	dynamicController := appdynamic.New(appdynamic.Config{
		GlobalGreen:  50,
		GlobalYellow: 50,
		GlobalRed:    50,
	}, appdynamic.Dependencies{
		GlobalLimit: func() int { return 50 },
	})
	predictiveShadow := &routerMetricsPredictiveShadow{telemetry: predictiveAdmissionTelemetrySnapshot{
		Attempts: predictiveAttemptSnapshot{
			Attempts:    1,
			Risks:       1,
			LastReason:  domainpredictive.ReasonExistingTPSAtRisk,
			LastSource:  runtimepredictive.PredictionSourceCalibrated,
			LastSamples: 7,
		},
		Manager: runtimepredictive.Snapshot{
			IntakeOpen: true,
			Virtual: domainpredictive.VirtualStateInterval{
				Upper: domainpredictive.VirtualState{DecodeSequences: 1},
			},
		},
		RouterBackpressure: predictiveRouterBackpressureSnapshot{
			Active:      true,
			Activation:  1,
			Scope:       predictiveProtectionScopeLoad,
			Reason:      domainpredictive.ReasonExistingTPSAtRisk,
			Source:      runtimepredictive.PredictionSourceCalibrated,
			Samples:     7,
			ActivatedAt: time.Unix(100, 0),
			Until:       time.Unix(102, 0),
			Hold:        2 * time.Second,
			Activations: 1,
		},
	}}
	srv := &proxyServer{
		cfg:               config{PredictiveAdmissionMode: "enforce"},
		dynamicController: dynamicController,
		predictiveShadow:  predictiveShadow,
	}
	srv.predictiveEnforcedRejects.Store(1)

	var out bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&out)
	got := out.String()
	for _, want := range []string{
		"pig_predictive_admission_enforced_rejects_total 1",
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_predictive_router_backpressure_predictive_running 1",
		`pig_predictive_router_backpressure_state_info{scope="load",reason="existing_tps_at_risk",source="calibrated"} 1`,
		"pig_dynamic_observed_running_raw 0",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit_raw 50",
		"pig_dynamic_global_limit 1",
		"pig_dynamic_admission_limit 50",
		"pig_dynamic_router_backpressure_active 1",
		"pig_dynamic_router_backpressure_applied 1",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("combined predictive/dynamic metrics missing %q:\n%s", want, got)
		}
	}
	router := parseRouterConsumedCapacity(t, out.String())
	if router.running != 1 || router.waiting != 0 || router.limit != 1 || router.fullness() < 1 {
		t.Fatalf("Router-consumed protected capacity = %+v fullness=%.3f, want at least 100%%", router, router.fullness())
	}
}

func TestRealPredictiveDecisionPublishesDurableProtectionAndRouterCapacity(t *testing.T) {
	now := time.Unix(120_000, 0)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	coordinator.rejectReason = domainpredictive.ReasonExistingTPSAtRisk
	coordinator.virtual = domainpredictive.VirtualState{DecodeSequences: 1, ActiveKVUpper: 256}
	var activationLogs []string
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "enforce",
		Now:                    func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
		OnRouterBackpressure: func(event predictiveRouterBackpressureEvent) {
			activationLogs = append(activationLogs, predictiveRouterBackpressureLogLine(event))
		},
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	if reservation := adapter.DecideAndReserve(context.Background(), "router-risk", approximateAdapterTestInput()); reservation != nil {
		t.Fatal("load-dependent enforce risk unexpectedly reserved")
	}
	if len(activationLogs) != 1 || !strings.Contains(activationLogs[0], "scope=load") || !strings.Contains(activationLogs[0], "reason=existing_tps_at_risk") {
		t.Fatalf("real rejection activation logs = %v", activationLogs)
	}

	dynamicController := appdynamic.New(appdynamic.Config{
		GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50,
	}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})
	srv := &proxyServer{
		cfg:               config{PredictiveAdmissionMode: "enforce"},
		dynamicController: dynamicController,
		predictiveShadow:  adapter,
	}
	srv.predictiveEnforcedRejects.Store(1)
	var first bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&first)
	for _, want := range []string{
		`pig_predictive_admission_last_reject_info{reason="existing_tps_at_risk",source="static",scope="load"} 1`,
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit 1",
	} {
		if !strings.Contains(first.String(), want) {
			t.Fatalf("real decision metrics missing %q:\n%s", want, first.String())
		}
	}

	coordinator.mu.Lock()
	coordinator.reject = false
	coordinator.mu.Unlock()
	now = now.Add(time.Second)
	fit := adapter.DecideAndReserve(context.Background(), "router-fit", approximateAdapterTestInput())
	if fit == nil {
		t.Fatal("post-risk fit did not reserve")
	}
	defer fit.Terminate(runtimepredictive.TerminalCompleted)
	var afterFit bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&afterFit)
	if !strings.Contains(afterFit.String(), `pig_predictive_admission_last_decision_info{reason="fit",source="static"} 1`) ||
		!strings.Contains(afterFit.String(), `pig_predictive_admission_last_reject_info{reason="existing_tps_at_risk",source="static",scope="load"} 1`) {
		t.Fatalf("later fit erased durable protection diagnostics:\n%s", afterFit.String())
	}
}

func TestHTTPPredictiveRejectPublishesTheSameProtectionToLogsMetricsAndRouterCapacity(t *testing.T) {
	now := time.Unix(120_500, 0)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	coordinator.rejectReason = domainpredictive.ReasonExistingTPSAtRisk
	coordinator.virtual = domainpredictive.VirtualState{DecodeSequences: 1, ActiveKVUpper: 256}
	var activationLogs []string
	var duringDecision bytes.Buffer
	var srv *proxyServer
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "enforce",
		Now:                    func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
		OnRouterBackpressure: func(event predictiveRouterBackpressureEvent) {
			activationLogs = append(activationLogs, predictiveRouterBackpressureLogLine(event))
			if srv != nil {
				srv.writePredictiveAndDynamicMetrics(&duringDecision)
			}
		},
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		backendCalls++
	}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	srv, err = newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return adapter, nil },
	})
	if err != nil {
		t.Fatalf("new predictive server: %v", err)
	}
	defer srv.Close()
	srv.dynamicController = appdynamic.New(appdynamic.Config{
		GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50,
	}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})

	body := `{"model":"m","messages":[{"role":"user","content":"protect before upstream"}],"max_tokens":64}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("HTTP predictive protection response/backend = %d/%d, want 429/0", recorder.Code, backendCalls)
	}
	if len(activationLogs) != 1 || !strings.Contains(activationLogs[0], "scope=load") || !strings.Contains(activationLogs[0], "reason=existing_tps_at_risk") {
		t.Fatalf("HTTP decision activation logs = %v", activationLogs)
	}
	for _, want := range []string{
		"pig_predictive_admission_enforced_rejects_total 0",
		"pig_predictive_router_backpressure_active 1",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit 1",
	} {
		if !strings.Contains(duringDecision.String(), want) {
			t.Fatalf("pre-response protection snapshot missing %q:\n%s", want, duringDecision.String())
		}
	}

	var out bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&out)
	for _, want := range []string{
		"pig_predictive_admission_enforced_rejects_total 1",
		"pig_predictive_admission_attempts_total 1",
		`pig_predictive_admission_last_reject_info{reason="existing_tps_at_risk",source="static",scope="load"} 1`,
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("HTTP decision metrics missing %q:\n%s", want, out.String())
		}
	}
	router := parseRouterConsumedCapacity(t, out.String())
	if router.running != 1 || router.waiting != 0 || router.limit != 1 || router.fullness() < 1 {
		t.Fatalf("HTTP protection did not block the Router parser view: capacity=%+v fullness=%.3f", router, router.fullness())
	}
}

func TestSustainedHTTPPredictiveRejectsRenewRouterPublicationWithoutAnExpiryGap(t *testing.T) {
	started := time.Unix(120_600, 0)
	now := started
	hold := 2 * time.Second
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	coordinator.rejectReason = domainpredictive.ReasonExistingTPSAtRisk
	coordinator.virtual = domainpredictive.VirtualState{DecodeSequences: 1, ActiveKVUpper: 256}

	var transitionLogs []string
	var transitionMetrics []string
	var srv *proxyServer
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "enforce",
		Now:                    func() time.Time { return now },
		RouterBackpressureHold: hold,
		OnRouterBackpressure: func(event predictiveRouterBackpressureEvent) {
			transitionLogs = append(transitionLogs, predictiveRouterBackpressureLogLine(event))
			if srv != nil {
				var out bytes.Buffer
				srv.writePredictiveAndDynamicMetrics(&out)
				transitionMetrics = append(transitionMetrics, out.String())
			}
		},
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		backendCalls++
	}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	srv, err = newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return adapter, nil },
	})
	if err != nil {
		t.Fatalf("new predictive server: %v", err)
	}
	defer srv.Close()
	srv.dynamicController = appdynamic.New(appdynamic.Config{
		GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50,
	}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})

	serveReject := func(label string) {
		t.Helper()
		body := fmt.Sprintf(`{"model":"m","messages":[{"role":"user","content":%q}],"max_tokens":64}`, label)
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		srv.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusTooManyRequests {
			t.Fatalf("%s status = %d body=%q, want 429", label, recorder.Code, recorder.Body.String())
		}
	}

	serveReject("activate")
	now = started.Add(1500 * time.Millisecond)
	serveReject("logged renewal")
	now = started.Add(1600 * time.Millisecond)
	serveReject("suppressed renewal log")
	if backendCalls != 0 {
		t.Fatalf("sustained protected requests reached upstream %d times", backendCalls)
	}
	if len(transitionLogs) != 2 || len(transitionMetrics) != 2 {
		t.Fatalf("activation/renewal callbacks = %d/%d logs=%v, want two bounded transitions", len(transitionLogs), len(transitionMetrics), transitionLogs)
	}
	if !strings.Contains(transitionLogs[0], "event=activated") ||
		!strings.Contains(transitionLogs[1], "event=renewed") ||
		!strings.Contains(transitionLogs[1], "activation=1") ||
		!strings.Contains(transitionLogs[1], "extensions=1") {
		t.Fatalf("bounded transition logs = %v", transitionLogs)
	}
	for index, snapshot := range transitionMetrics {
		wantRejects := fmt.Sprintf("pig_predictive_admission_enforced_rejects_total %d", index)
		if !strings.Contains(snapshot, wantRejects) ||
			!strings.Contains(snapshot, "pig_predictive_router_backpressure_active 1") ||
			!strings.Contains(snapshot, "pig_predictive_router_backpressure_applied 1") {
			t.Fatalf("pre-429 transition %d was not synchronously published:\n%s", index, snapshot)
		}
		router := parseRouterConsumedCapacity(t, snapshot)
		if router.running != 1 || router.limit != 1 || router.fullness() < 1 {
			t.Fatalf("pre-429 transition %d Router capacity = %+v", index, router)
		}
	}

	// The first and logged-renewal deadlines have both passed here. Only the
	// third reject, whose log was rate-limited, keeps the lease active.
	now = started.Add(3550 * time.Millisecond)
	telemetry := adapter.PredictiveAdmissionTelemetry().RouterBackpressure
	wantUntil := started.Add(1600*time.Millisecond + hold)
	if !telemetry.Active || telemetry.Activation != 1 || telemetry.Activations != 1 ||
		telemetry.Extensions != 2 || telemetry.RenewalLogs != 1 || telemetry.RenewalsSuppressed != 1 ||
		!telemetry.LatestRejectAt.Equal(started.Add(1600*time.Millisecond)) || !telemetry.Until.Equal(wantUntil) {
		t.Fatalf("sustained publication telemetry = %+v, want one continuously renewed episode until %s", telemetry, wantUntil)
	}
	var sustained bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&sustained)
	for _, want := range []string{
		"pig_predictive_admission_enforced_rejects_total 3",
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_predictive_router_backpressure_activation 1",
		"pig_predictive_router_backpressure_extensions_total 2",
		"pig_predictive_router_backpressure_renewal_logs_total 1",
		"pig_predictive_router_backpressure_renewal_logs_suppressed_total 1",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit 1",
	} {
		if !strings.Contains(sustained.String(), want) {
			t.Fatalf("sustained post-deadline metrics missing %q:\n%s", want, sustained.String())
		}
	}
	if router := parseRouterConsumedCapacity(t, sustained.String()); router.running != 1 || router.limit != 1 || router.fullness() < 1 {
		t.Fatalf("sustained post-deadline Router capacity = %+v", router)
	}

	now = wantUntil.Add(time.Nanosecond)
	var recovered bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&recovered)
	if !strings.Contains(recovered.String(), "pig_predictive_router_backpressure_active 0") ||
		!strings.Contains(recovered.String(), "pig_predictive_router_backpressure_applied 0") ||
		!strings.Contains(recovered.String(), "pig_dynamic_global_limit 50") {
		t.Fatalf("finite last reject did not recover after the renewed deadline:\n%s", recovered.String())
	}
}

func TestHTTPUnscannableRequestRejectIsDurableButDoesNotSuppressIdleRouterCapacity(t *testing.T) {
	now := time.Unix(120_750, 0)
	coordinator := newRecordingUpperBoundCoordinator()
	var requestLogs []string
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "enforce",
		Now:                    func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
		OnRequestReject: func(event predictiveRequestRejectEvent) {
			requestLogs = append(requestLogs, predictiveRequestRejectLogLine(event))
		},
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		backendCalls++
	}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "enforce"
	cfg.JSONClassifyBodyBytes = 8
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return adapter, nil },
	})
	if err != nil {
		t.Fatalf("new predictive server: %v", err)
	}
	defer srv.Close()
	srv.dynamicController = appdynamic.New(appdynamic.Config{
		GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50,
	}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})

	body := `{"model":"m","messages":[{"role":"user","content":"larger than classifier limit"}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("unscannable request response/backend = %d/%d, want 429/0", recorder.Code, backendCalls)
	}
	if len(requestLogs) != 1 || !strings.Contains(requestLogs[0], "scope=request") || !strings.Contains(requestLogs[0], "reason=request_size_unknown") {
		t.Fatalf("unscannable request logs = %v", requestLogs)
	}

	var out bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&out)
	for _, want := range []string{
		"pig_predictive_admission_enforced_rejects_total 1",
		"pig_predictive_admission_attempts_total 1",
		`pig_predictive_admission_last_reject_info{reason="request_size_unknown",source="unknown",scope="request"} 1`,
		"pig_predictive_router_backpressure_active 0",
		"pig_predictive_router_backpressure_applied 0",
		"pig_dynamic_observed_running 0",
		"pig_dynamic_global_limit 50",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("unscannable request metrics missing %q:\n%s", want, out.String())
		}
	}
	router := parseRouterConsumedCapacity(t, out.String())
	if router.running != 0 || router.waiting != 0 || router.limit != 50 || router.fullness() != 0 {
		t.Fatalf("request-scoped reject incorrectly suppressed idle Router capacity: %+v", router)
	}
}

func TestRequestScopedRejectDoesNotSuppressIdleRouterCapacity(t *testing.T) {
	now := time.Unix(121_000, 0)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	coordinator.rejectReason = domainpredictive.ReasonNewTPSAtRisk
	requestLogs := 0
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "enforce",
		Now:                    func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
		OnRequestReject: func(event predictiveRequestRejectEvent) {
			requestLogs++
			if event.Scope != predictiveProtectionScopeRequest {
				t.Fatalf("request reject scope = %q", event.Scope)
			}
		},
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	if reservation := adapter.DecideAndReserve(context.Background(), "idle-request-risk", approximateAdapterTestInput()); reservation != nil {
		t.Fatal("request-scoped risk unexpectedly reserved")
	}
	if requestLogs != 1 {
		t.Fatalf("request-scoped rejection logs = %d, want 1", requestLogs)
	}
	dynamicController := appdynamic.New(appdynamic.Config{GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})
	srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}, dynamicController: dynamicController, predictiveShadow: adapter}
	var out bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&out)
	for _, want := range []string{
		`pig_predictive_admission_last_reject_info{reason="new_tps_at_risk",source="static",scope="request"} 1`,
		"pig_predictive_router_backpressure_active 0",
		"pig_predictive_router_backpressure_applied 0",
		"pig_dynamic_observed_running 0",
		"pig_dynamic_global_limit 50",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("request-scoped reject metrics missing %q:\n%s", want, out.String())
		}
	}
}

func TestLoadProtectionStopsApplyingImmediatelyAfterCurrentLoadDrains(t *testing.T) {
	started := time.Unix(121_500, 0)
	now := started
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	coordinator.rejectReason = domainpredictive.ReasonExistingTPSAtRisk
	coordinator.virtual = domainpredictive.VirtualState{DecodeSequences: 1, ActiveKVUpper: 256}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "enforce",
		Now:                    func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	if decision := adapter.Decide(context.Background(), "load-before-drain", approximateAdapterTestInput()); decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("load protection decision = %+v", decision)
	}
	now = started.Add(1500 * time.Millisecond)
	if decision := adapter.Decide(context.Background(), "load-renewal-logged", approximateAdapterTestInput()); decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("logged renewal decision = %+v", decision)
	}
	now = started.Add(1600 * time.Millisecond)
	if decision := adapter.Decide(context.Background(), "load-renewal-suppressed", approximateAdapterTestInput()); decision.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("rate-limited renewal decision = %+v", decision)
	}
	coordinator.mu.Lock()
	coordinator.virtual = domainpredictive.VirtualState{}
	coordinator.mu.Unlock()
	now = started.Add(1700 * time.Millisecond)
	dynamicController := appdynamic.New(appdynamic.Config{GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})
	srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}, dynamicController: dynamicController, predictiveShadow: adapter}
	var out bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&out)
	for _, want := range []string{
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 0",
		"pig_dynamic_observed_running 0",
		"pig_dynamic_global_limit 50",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("drained load projection missing %q:\n%s", want, out.String())
		}
	}
	if router := parseRouterConsumedCapacity(t, out.String()); router.fullness() != 0 {
		t.Fatalf("drained load remained Router-locked: %+v", router)
	}

	now = started.Add(1600*time.Millisecond + 2*time.Second + time.Nanosecond)
	var expired bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&expired)
	if !strings.Contains(expired.String(), "pig_predictive_router_backpressure_active 0") ||
		!strings.Contains(expired.String(), "pig_predictive_router_backpressure_applied 0") ||
		!strings.Contains(expired.String(), "pig_dynamic_global_limit 50") {
		t.Fatalf("drained load lease did not expire without new traffic:\n%s", expired.String())
	}
	coordinator.mu.Lock()
	coordinator.reject = false
	coordinator.mu.Unlock()
	fit := adapter.DecideAndReserve(context.Background(), "cold-safe-after-drain", approximateAdapterTestInput())
	if fit == nil {
		t.Fatal("cold-safe request remained self-locked after drain and lease expiry")
	}
	fit.Terminate(runtimepredictive.TerminalCompleted)
}

func TestUnavailableProtectionPublishesAndClearsFromCurrentHealth(t *testing.T) {
	now := time.Unix(122_000, 0)
	coordinator := newRecordingUpperBoundCoordinator()
	upstream := &togglePredictiveUpstream{}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Upstream:               upstream,
		Mode:                   "enforce",
		Now:                    func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	if reservation := adapter.DecideAndReserve(context.Background(), "unavailable", approximateAdapterTestInput()); reservation != nil {
		t.Fatal("unavailable upstream unexpectedly reserved")
	}
	dynamicController := appdynamic.New(appdynamic.Config{GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})
	srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}, dynamicController: dynamicController, predictiveShadow: adapter}
	var unavailable bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&unavailable)
	for _, want := range []string{
		`pig_predictive_admission_last_reject_info{reason="predictor_profile_unknown",source="unavailable",scope="availability"} 1`,
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit 1",
	} {
		if !strings.Contains(unavailable.String(), want) {
			t.Fatalf("availability protection metrics missing %q:\n%s", want, unavailable.String())
		}
	}

	upstream.SetHealthy(true)
	now = now.Add(time.Second)
	var recovered bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&recovered)
	if !strings.Contains(recovered.String(), "pig_predictive_router_backpressure_active 0") ||
		!strings.Contains(recovered.String(), "pig_dynamic_observed_running 0") ||
		!strings.Contains(recovered.String(), "pig_dynamic_global_limit 50") {
		t.Fatalf("current upstream health did not clear availability projection:\n%s", recovered.String())
	}
	if reservation := adapter.DecideAndReserve(context.Background(), "recovered", approximateAdapterTestInput()); reservation == nil {
		t.Fatal("recovered idle node remained self-locked")
	}
}

func TestCoordinatorUnavailableProtectionPublishesAndClearsFromCurrentHealth(t *testing.T) {
	now := time.Unix(122_500, 0)
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.available = false
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "enforce",
		Now:                    func() time.Time { return now },
		RouterBackpressureHold: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	decision := adapter.Decide(context.Background(), "coordinator-unavailable", approximateAdapterTestInput())
	if decision.Outcome != predictiveAdmissionOutcomeAvailabilityProtection || decision.Reservation != nil {
		t.Fatalf("coordinator-unavailable decision = %+v", decision)
	}
	dynamicController := appdynamic.New(appdynamic.Config{GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})
	srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}, dynamicController: dynamicController, predictiveShadow: adapter}
	var unavailable bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&unavailable)
	for _, want := range []string{
		`pig_predictive_admission_last_reject_info{reason="predictor_profile_unknown",source="unavailable",scope="availability"} 1`,
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit 1",
	} {
		if !strings.Contains(unavailable.String(), want) {
			t.Fatalf("coordinator availability metrics missing %q:\n%s", want, unavailable.String())
		}
	}

	coordinator.mu.Lock()
	coordinator.available = true
	coordinator.mu.Unlock()
	now = now.Add(time.Second)
	var recovered bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&recovered)
	if !strings.Contains(recovered.String(), "pig_predictive_router_backpressure_active 0") ||
		!strings.Contains(recovered.String(), "pig_dynamic_observed_running 0") ||
		!strings.Contains(recovered.String(), "pig_dynamic_global_limit 50") {
		t.Fatalf("current coordinator health did not clear availability projection:\n%s", recovered.String())
	}
}

func TestClosedPredictiveAdapterPublishesAvailabilityCapacityInsteadOfIdle429s(t *testing.T) {
	now := time.Unix(122_625, 0)
	var activationLogs []string
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:  newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator: newRecordingUpperBoundCoordinator(),
		Mode:        "enforce",
		Now:         func() time.Time { return now },
		OnRouterBackpressure: func(event predictiveRouterBackpressureEvent) {
			activationLogs = append(activationLogs, predictiveRouterBackpressureLogLine(event))
		},
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	if err := adapter.Close(); err != nil {
		t.Fatalf("close predictive adapter: %v", err)
	}
	decision := adapter.Decide(context.Background(), "closed-adapter", approximateAdapterTestInput())
	if decision.Outcome != predictiveAdmissionOutcomeAvailabilityProtection || decision.Reservation != nil {
		t.Fatalf("closed-adapter decision = %+v", decision)
	}

	dynamicController := appdynamic.New(appdynamic.Config{GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})
	srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}, dynamicController: dynamicController, predictiveShadow: adapter}
	var out bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&out)
	for _, want := range []string{
		`pig_predictive_admission_last_reject_info{reason="predictor_profile_unknown",source="unavailable",scope="availability"} 1`,
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("closed adapter availability metrics missing %q:\n%s", want, out.String())
		}
	}
	if len(activationLogs) != 1 || !strings.Contains(activationLogs[0], "scope=availability") {
		t.Fatalf("closed adapter activation logs = %v, want one availability activation", activationLogs)
	}
}

func TestUpstreamHealthPanicPublishesAvailabilityInsteadOfAnInvisibleHTTPFailure(t *testing.T) {
	now := time.Unix(122_750, 0)
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:  newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator: newRecordingUpperBoundCoordinator(),
		Upstream:    panickingPredictiveUpstream{},
		Mode:        "enforce",
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	decision := adapter.Decide(context.Background(), "health-panic", approximateAdapterTestInput())
	if decision.Outcome != predictiveAdmissionOutcomeAvailabilityProtection || decision.Reservation != nil ||
		decision.Source != runtimepredictive.PredictionSourceUnavailable {
		t.Fatalf("upstream-health panic decision = %+v", decision)
	}
	dynamicController := appdynamic.New(appdynamic.Config{GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})
	srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}, dynamicController: dynamicController, predictiveShadow: adapter}
	var out bytes.Buffer
	srv.writePredictiveAndDynamicMetrics(&out)
	for _, want := range []string{
		`pig_predictive_admission_last_reject_info{reason="predictor_profile_unknown",source="unavailable",scope="availability"} 1`,
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		"pig_dynamic_observed_running 1",
		"pig_dynamic_global_limit 1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("upstream-health panic metrics missing %q:\n%s", want, out.String())
		}
	}
}

func TestConcurrentPredictiveDecisionsAndRouterMetricsScrapesRemainCoherent(t *testing.T) {
	coordinator := newRecordingUpperBoundCoordinator()
	coordinator.reject = true
	coordinator.rejectReason = domainpredictive.ReasonExistingTPSAtRisk
	coordinator.virtual = domainpredictive.VirtualState{DecodeSequences: 1, ActiveKVUpper: 256}
	adapter, err := newApproximatePredictiveShadow(approximatePredictiveShadowConfig{
		Calibrator:             newApproximateAdapterTestCalibratorWithConfidence(t, 1, 1, 1),
		Coordinator:            coordinator,
		Mode:                   "enforce",
		RouterBackpressureHold: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("new predictive adapter: %v", err)
	}
	dynamicController := appdynamic.New(appdynamic.Config{GlobalGreen: 50, GlobalYellow: 50, GlobalRed: 50}, appdynamic.Dependencies{GlobalLimit: func() int { return 50 }})
	srv := &proxyServer{cfg: config{PredictiveAdmissionMode: "enforce"}, dynamicController: dynamicController, predictiveShadow: adapter}
	if seed := adapter.Decide(context.Background(), "race-seed", approximateAdapterTestInput()); seed.Outcome != predictiveAdmissionOutcomeLoadProtection {
		t.Fatalf("seed protection decision = %+v", seed)
	}

	start := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 4; worker++ {
		worker := worker
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for index := 0; index < 100; index++ {
				decision := adapter.Decide(context.Background(), fmt.Sprintf("race-%d-%d", worker, index), approximateAdapterTestInput())
				if decision.Outcome != predictiveAdmissionOutcomeLoadProtection || decision.Reservation != nil {
					t.Errorf("concurrent decision = %+v", decision)
					return
				}
			}
		}()
	}
	for worker := 0; worker < 4; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for index := 0; index < 100; index++ {
				var out bytes.Buffer
				srv.writePredictiveAndDynamicMetrics(&out)
				if !strings.Contains(out.String(), "pig_dynamic_observed_running 1") ||
					!strings.Contains(out.String(), "pig_dynamic_global_limit 1") {
					t.Errorf("concurrent Router projection was incoherent:\n%s", out.String())
					return
				}
			}
		}()
	}
	close(start)
	workers.Wait()
}

type routerConsumedCapacity struct {
	running int
	waiting int
	limit   int
}

func (c routerConsumedCapacity) fullness() float64 {
	if c.limit <= 0 {
		return 0
	}
	return float64(c.running+c.waiting) / float64(c.limit)
}

func parseRouterConsumedCapacity(t *testing.T, body string) routerConsumedCapacity {
	t.Helper()
	values := map[string]*int{}
	capacity := routerConsumedCapacity{}
	values["pig_dynamic_observed_running"] = &capacity.running
	values["pig_dynamic_observed_waiting"] = &capacity.waiting
	values["pig_dynamic_global_limit"] = &capacity.limit
	found := make(map[string]bool, len(values))
	for _, line := range strings.Split(body, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		target, ok := values[fields[0]]
		if !ok {
			continue
		}
		value, err := strconv.Atoi(fields[1])
		if err != nil {
			t.Fatalf("parse Router metric %s=%q: %v", fields[0], fields[1], err)
		}
		*target = value
		found[fields[0]] = true
	}
	for name := range values {
		if !found[name] {
			t.Fatalf("Router-consumed metric %s is absent", name)
		}
	}
	return capacity
}
