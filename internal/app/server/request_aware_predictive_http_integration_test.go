package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestRequestAwareHTTPEnforceDifferentiatesSmallAndLargeBeforeUpstream(t *testing.T) {
	adapter, manager := newRequestAwareHTTPAdapter(t, "enforce")
	var decisionLogs []requestAwareDecisionLogEvent
	adapter.onDecision = func(event requestAwareDecisionLogEvent) {
		decisionLogs = append(decisionLogs, event)
	}
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"ok"}`))
	}))
	defer backend.Close()
	srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "enforce")
	defer srv.Close()

	small := serveRequestAwareHTTP(t, srv, "hello")
	if small.Code != http.StatusOK || backendCalls != 1 {
		t.Fatalf("small HTTP response/backend=%d/%d body=%q, want 200/1", small.Code, backendCalls, small.Body.String())
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 {
		t.Fatalf("small completed HTTP request leaked reservation: %+v", snapshot)
	}

	large := serveRequestAwareHTTP(t, srv, strings.Repeat("a", 3_600))
	if large.Code != http.StatusTooManyRequests || backendCalls != 1 {
		t.Fatalf("large HTTP response/backend=%d/%d body=%q, want 429/unchanged", large.Code, backendCalls, large.Body.String())
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonRequestSize ||
		telemetry.RouterBackpressure.InspectCapacity != 1 || !telemetry.RouterBackpressure.Active ||
		telemetry.Attempts.Attempts != 2 || telemetry.Attempts.Fits != 1 || telemetry.Attempts.Risks != 1 {
		t.Fatalf("enforce HTTP telemetry=%+v", telemetry)
	}
	if len(decisionLogs) != 1 || !decisionLogs[0].Enforced || decisionLogs[0].Action != runtimepredictive.RequestAwareSizeProtect {
		t.Fatalf("enforce HTTP decision logs=%+v, want one exact enforced size protection", decisionLogs)
	}
	var metrics strings.Builder
	srv.writePredictiveAndDynamicMetrics(&metrics)
	for _, want := range []string{
		`pig_predictive_request_aware_last_decision_info{action="size_protect",reason="request_size",pressure_source="tps",prefill_class="regular"} 1`,
		"pig_predictive_router_inspect_capacity 1",
		"pig_predictive_admission_attempts_total 2",
		`pig_predictive_admission_decisions_total{decision="fit"} 1`,
		`pig_predictive_admission_decisions_total{decision="risk"} 1`,
	} {
		if !strings.Contains(metrics.String(), want) {
			t.Fatalf("enforce HTTP metrics missing %q\n%s", want, metrics.String())
		}
	}
}

func TestRequestAwareHTTPShadowWouldProtectButStillForwards(t *testing.T) {
	adapter, manager := newRequestAwareHTTPAdapter(t, "shadow")
	var decisionLogs []requestAwareDecisionLogEvent
	adapter.onDecision = func(event requestAwareDecisionLogEvent) {
		decisionLogs = append(decisionLogs, event)
	}
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "shadow")
	defer srv.Close()

	response := serveRequestAwareHTTP(t, srv, strings.Repeat("a", 3_600))
	if response.Code != http.StatusOK || backendCalls != 1 {
		t.Fatalf("shadow HTTP response/backend=%d/%d, want 200/1", response.Code, backendCalls)
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.InspectCapacity != 0 ||
		manager.Snapshot().Reservations != 0 {
		t.Fatalf("shadow HTTP telemetry/manager=%+v/%+v", telemetry, manager.Snapshot())
	}
	if len(decisionLogs) != 1 || decisionLogs[0].Enforced || decisionLogs[0].Action != runtimepredictive.RequestAwareSizeProtect {
		t.Fatalf("shadow HTTP decision logs=%+v, want one non-enforced would-protect", decisionLogs)
	}
}

func TestRequestAwareHTTPLongPrefillProtectionIsPreForwardAndObservable(t *testing.T) {
	adapter, _ := newRequestAwareHTTPAdapter(t, "enforce")
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		SoftKVRatio: 0.60, HardKVRatio: 0.90, TPSTarget: 20, TPSFloor: 15, BlockSize: 16,
		PrefillRegularTokens: 4, PrefillExclusiveTokens: 8,
		PrefillQuiescentTokens: 16, PrefillAggregateBudgetTokens: 8,
	})
	if err != nil {
		t.Fatalf("new long-prefill policy: %v", err)
	}
	adapter.policy = policy
	var decisionLogs []requestAwareDecisionLogEvent
	adapter.onDecision = func(event requestAwareDecisionLogEvent) {
		decisionLogs = append(decisionLogs, event)
	}
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		backendCalls++
	}))
	defer backend.Close()
	srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "enforce")
	defer srv.Close()

	response := serveRequestAwareHTTP(t, srv, strings.Repeat("a", 256))
	if response.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("long-prefill response/backend=%d/%d body=%q, want pre-forward 429/0", response.Code, backendCalls, response.Body.String())
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
		telemetry.RequestAware.PressureSource != runtimepredictive.RequestAwarePressurePrefill ||
		telemetry.RequestAware.PrefillClass != runtimepredictive.RequestAwarePrefillQuiescent ||
		telemetry.RequestAware.EstimatedPrefillTokens < 16 || telemetry.RequestAware.PostAdmitPendingPrefillTokens < 16 ||
		!telemetry.RouterBackpressure.Active || telemetry.RouterBackpressure.InspectCapacity != 0 {
		t.Fatalf("long-prefill telemetry=%+v", telemetry)
	}
	if len(decisionLogs) != 1 || !decisionLogs[0].Enforced ||
		decisionLogs[0].Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
		decisionLogs[0].PrefillClass != runtimepredictive.RequestAwarePrefillQuiescent ||
		decisionLogs[0].EstimatedPrefillTokens < 16 {
		t.Fatalf("long-prefill decision logs=%+v", decisionLogs)
	}
	line := requestAwareDecisionLogLine(decisionLogs[0])
	for _, want := range []string{"reason=prefill_busy", "pressure_source=prefill", "prefill_class=quiescent", "estimated_prefill_tokens="} {
		if !strings.Contains(line, want) {
			t.Fatalf("long-prefill log missing %q: %s", want, line)
		}
	}
	var rendered strings.Builder
	srv.writePredictiveAndDynamicMetrics(&rendered)
	for _, want := range []string{
		`pig_predictive_request_aware_last_decision_info{action="size_protect",reason="prefill_busy",pressure_source="prefill",prefill_class="quiescent"} 1`,
		"pig_predictive_request_aware_estimated_prefill_tokens ",
		"pig_predictive_request_aware_post_admit_pending_prefill_tokens ",
		"pig_predictive_router_inspect_capacity 0",
	} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("long-prefill metrics missing %q\n%s", want, rendered.String())
		}
	}
}

func TestRequestAwareHTTPSeparatesPrefillInterferenceEstimateFromKVUpper(t *testing.T) {
	lowLexicalBody, highLexicalBody := equalSafetyEnvelopeRequestAwareBodies(t)

	t.Run("same safety envelope changes pre-forward decision by lexical work", func(t *testing.T) {
		adapter, _ := newRequestAwareHTTPAdapter(t, "enforce")
		manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{}, domainpredictive.Constraints{}, nil)
		adapter.manager = manager
		input := adapter.snapshot.(staticRequestAwareSnapshot).input
		input.CapacityTokens = 4 * 1024 * 1024
		input.Running = 1
		input.EffectiveSequences = 1
		input.TPSValid = false
		adapter.snapshot = staticRequestAwareSnapshot{input: input}

		backendCalls := 0
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			backendCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"ok"}`))
		}))
		defer backend.Close()
		srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "enforce")
		defer srv.Close()

		lowResponse := serveRequestAwareHTTPBody(t, srv, lowLexicalBody)
		if lowResponse.Code != http.StatusOK || backendCalls != 1 {
			t.Fatalf("low-lexical HTTP response/backend=%d/%d body=%q, want 200/1", lowResponse.Code, backendCalls, lowResponse.Body.String())
		}
		lowTelemetry := adapter.PredictiveAdmissionTelemetry()
		highResponse := serveRequestAwareHTTPBody(t, srv, highLexicalBody)
		if highResponse.Code != http.StatusTooManyRequests || backendCalls != 1 {
			t.Fatalf("high-lexical HTTP response/backend=%d/%d body=%q, want pre-forward 429/unchanged", highResponse.Code, backendCalls, highResponse.Body.String())
		}
		highTelemetry := adapter.PredictiveAdmissionTelemetry()
		if lowTelemetry.RequestAware.Action != runtimepredictive.RequestAwareAdmit ||
			lowTelemetry.RequestAware.PrefillClass != runtimepredictive.RequestAwarePrefillRegular ||
			lowTelemetry.RequestAware.EstimatedPrefillTokens <= 0 ||
			lowTelemetry.RequestAware.EstimatedPrefillTokens != lowTelemetry.RequestAware.SelectionInputTokens ||
			lowTelemetry.RequestAware.ReservedTokens <= lowTelemetry.RequestAware.EstimatedPrefillTokens*10 ||
			highTelemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
			highTelemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
			highTelemetry.RequestAware.PrefillClass != runtimepredictive.RequestAwarePrefillQuiescent ||
			highTelemetry.RequestAware.EstimatedPrefillTokens < 512*1024 ||
			highTelemetry.RequestAware.EstimatedPrefillTokens != highTelemetry.RequestAware.SelectionInputTokens ||
			highTelemetry.RequestAware.ReservedTokens != lowTelemetry.RequestAware.ReservedTokens ||
			manager.Snapshot().Reservations != 0 {
			t.Fatalf("same-envelope low/high telemetry/manager=%+v/%+v/%+v", lowTelemetry, highTelemetry, manager.Snapshot())
		}
	})

	t.Run("hard KV still rejects the same safety envelope", func(t *testing.T) {
		adapter, _ := newRequestAwareHTTPAdapter(t, "enforce")
		manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{}, domainpredictive.Constraints{}, nil)
		adapter.manager = manager
		input := adapter.snapshot.(staticRequestAwareSnapshot).input
		input.CapacityTokens = 512 * 1024
		input.Running = 0
		input.EffectiveSequences = 0
		input.TPSValid = false
		adapter.snapshot = staticRequestAwareSnapshot{input: input}

		backendCalls := 0
		backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			backendCalls++
		}))
		defer backend.Close()
		srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "enforce")
		defer srv.Close()

		response := serveRequestAwareHTTPBody(t, srv, lowLexicalBody)
		if response.Code != http.StatusTooManyRequests || backendCalls != 0 {
			t.Fatalf("divergent hard-KV response/backend=%d/%d body=%q, want 429/0", response.Code, backendCalls, response.Body.String())
		}
		telemetry := adapter.PredictiveAdmissionTelemetry()
		if telemetry.RequestAware.Action != runtimepredictive.RequestAwareHardProtect ||
			telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonKV ||
			telemetry.RequestAware.EstimatedPrefillTokens <= 0 ||
			telemetry.RequestAware.EstimatedPrefillTokens != telemetry.RequestAware.SelectionInputTokens ||
			telemetry.RequestAware.ReservedTokens <= telemetry.RequestAware.EstimatedPrefillTokens*10 {
			t.Fatalf("divergent hard-KV telemetry=%+v", telemetry)
		}
	})
}

func equalSafetyEnvelopeRequestAwareBodies(t *testing.T) (string, string) {
	t.Helper()
	const targetBytes = 1_600_000
	prefix := `{"model":"model-agnostic","messages":[{"role":"user","content":"`
	suffix := `"}],"max_tokens":8}`
	build := func(content string) string {
		body := prefix + content + suffix
		if len(body) > targetBytes {
			t.Fatalf("same-envelope HTTP fixture bytes=%d exceed target=%d", len(body), targetBytes)
		}
		return body + strings.Repeat(" ", targetBytes-len(body))
	}
	return build("hello"), build(strings.Repeat("你", 525_000))
}

func TestRequestAwareHTTPHardGuardsRejectBeforeUpstreamWithZeroInspectCapacity(t *testing.T) {
	for _, test := range []struct {
		name       string
		prepare    func(*requestAwarePredictiveAdapter, *runtimepredictive.Manager)
		wantReason runtimepredictive.RequestAwareReason
	}{
		{
			name: "stale",
			prepare: func(adapter *requestAwarePredictiveAdapter, _ *runtimepredictive.Manager) {
				input := adapter.snapshot.(staticRequestAwareSnapshot).input
				input.MetricsFresh = false
				adapter.snapshot = staticRequestAwareSnapshot{input: input}
			},
			wantReason: runtimepredictive.RequestAwareReasonStale,
		},
		{
			name: "preemption_cooldown",
			prepare: func(adapter *requestAwarePredictiveAdapter, _ *runtimepredictive.Manager) {
				input := adapter.snapshot.(staticRequestAwareSnapshot).input
				input.PreemptionCooldown = true
				adapter.snapshot = staticRequestAwareSnapshot{input: input}
			},
			wantReason: runtimepredictive.RequestAwareReasonPreemption,
		},
		{
			name: "epoch_invalid",
			prepare: func(_ *requestAwarePredictiveAdapter, manager *runtimepredictive.Manager) {
				manager.InvalidateEpoch()
			},
			wantReason: runtimepredictive.RequestAwareReasonUnavailable,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, manager := newRequestAwareHTTPAdapter(t, "enforce")
			var decisionLogs []requestAwareDecisionLogEvent
			adapter.onDecision = func(event requestAwareDecisionLogEvent) {
				decisionLogs = append(decisionLogs, event)
			}
			test.prepare(adapter, manager)
			backendCalls := 0
			backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				backendCalls++
			}))
			defer backend.Close()
			srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "enforce")
			defer srv.Close()

			response := serveRequestAwareHTTP(t, srv, "hard guard")
			if response.Code != http.StatusTooManyRequests || backendCalls != 0 {
				t.Fatalf("hard guard response/backend=%d/%d body=%q, want 429/0", response.Code, backendCalls, response.Body.String())
			}
			telemetry := adapter.PredictiveAdmissionTelemetry()
			if telemetry.RequestAware.Action != runtimepredictive.RequestAwareHardProtect ||
				telemetry.RequestAware.Reason != test.wantReason || !telemetry.RouterBackpressure.Active ||
				telemetry.RouterBackpressure.InspectCapacity != 0 || telemetry.Attempts.Attempts != 1 {
				t.Fatalf("hard guard telemetry=%+v", telemetry)
			}
			if len(decisionLogs) != 1 || !decisionLogs[0].Enforced || decisionLogs[0].Reason != test.wantReason {
				t.Fatalf("hard guard decision logs=%+v", decisionLogs)
			}
		})
	}
}

func newRequestAwareHTTPAdapter(t testing.TB, mode string) (*requestAwarePredictiveAdapter, *runtimepredictive.Manager) {
	t.Helper()
	adapter, manager := newRequestAwareAdapterTestFixtureWithMode(t, 7_000, 0, mode)
	adapter.snapshot = staticRequestAwareSnapshot{input: runtimepredictive.RequestAwareInput{
		MetricsFresh:       true,
		IdentityValid:      true,
		CapacityTokens:     10_000,
		Running:            4,
		AggregateTPSProxy:  79.2,
		MeanActiveTPSProxy: 19.8,
		TPSValid:           true,
	}}
	return adapter, manager
}

func newRequestAwareHTTPTestServer(t testing.TB, upstream string, adapter *requestAwarePredictiveAdapter, mode string) *proxyServer {
	t.Helper()
	cfg := testProxyConfig(upstream)
	cfg.PredictiveAdmissionMode = mode
	srv, err := newProxyServerWithDependencies(cfg, serverDependencies{
		NewPredictiveShadow: func(config) (predictiveAdmissionShadow, error) { return adapter, nil },
	})
	if err != nil {
		t.Fatalf("new request-aware HTTP proxy: %v", err)
	}
	return srv
}

func serveRequestAwareHTTP(t *testing.T, srv *proxyServer, content string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"model":"model-agnostic","messages":[{"role":"user","content":` + strconv.Quote(content) + `}],"max_tokens":8}`
	return serveRequestAwareHTTPBody(t, srv, body)
}

func serveRequestAwareHTTPBody(t *testing.T, srv *proxyServer, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	srv.ServeHTTP(recorder, request)
	return recorder
}
