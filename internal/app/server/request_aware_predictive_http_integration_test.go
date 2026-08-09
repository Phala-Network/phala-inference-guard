package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestV0121RequestAwareHTTPMalformedJSONIsProtocolErrorBeforePrediction(t *testing.T) {
	for _, mode := range []string{"shadow", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			adapter, _ := newRequestAwareHTTPAdapter(t, mode)
			backendCalls := 0
			backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				backendCalls++
			}))
			defer backend.Close()
			srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, mode)
			defer srv.Close()
			before := adapter.PredictiveAdmissionTelemetry()
			beforeEnforced := srv.predictiveEnforcedRejects.Load()
			before429 := srv.total429.Load()

			response := serveRequestAwareHTTPBody(t, srv, `{"model":"model-agnostic","messages":[}`)
			if response.Code != http.StatusBadRequest || backendCalls != 0 {
				t.Fatalf("malformed response/backend=%d/%d body=%q, want protocol 400/0", response.Code, backendCalls, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), `"type":"invalid_request_error"`) ||
				!strings.Contains(response.Body.String(), `"code":400`) {
				t.Fatalf("malformed response is not a bounded OpenAI-compatible client error: %q", response.Body.String())
			}
			telemetry := adapter.PredictiveAdmissionTelemetry()
			if telemetry.Attempts != before.Attempts || telemetry.Manager.Reservations != before.Manager.Reservations ||
				telemetry.RouterBackpressure != before.RouterBackpressure || srv.predictiveEnforcedRejects.Load() != beforeEnforced ||
				srv.total429.Load() != before429 {
				t.Fatalf("malformed client error changed admission/load telemetry: before=%+v after=%+v enforced=%d/%d total429=%d/%d",
					before, telemetry, beforeEnforced, srv.predictiveEnforcedRejects.Load(), before429, srv.total429.Load())
			}
			routerActive := "0"
			if before.RouterBackpressure.Active {
				routerActive = "1"
			}
			var rendered strings.Builder
			srv.writeLocalMetrics(&rendered)
			for _, want := range []string{
				`pig_client_protocol_errors_total{reason="invalid_json"} 1`,
				"pig_predictive_admission_attempts_total 0",
				"pig_predictive_admission_enforced_rejects_total 0",
				"pig_predictive_router_backpressure_active " + routerActive,
			} {
				if !strings.Contains(rendered.String(), want) {
					t.Fatalf("malformed client-error metrics missing %q\n%s", want, rendered.String())
				}
			}
		})
	}
}

func TestV0121UnsupportedContentTypeDoesNotIncrementJSONProtocolErrors(t *testing.T) {
	for _, mode := range []string{"shadow", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			adapter, _ := newRequestAwareHTTPAdapter(t, mode)
			backendCalls := 0
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				backendCalls++
				w.WriteHeader(http.StatusUnsupportedMediaType)
			}))
			defer backend.Close()
			srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, mode)
			defer srv.Close()

			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`not-json`))
			request.Header.Set("Authorization", "Bearer secret")
			request.Header.Set("Content-Type", "text/plain")
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, request)

			if mode == "shadow" {
				if response.Code != http.StatusUnsupportedMediaType || backendCalls != 1 {
					t.Fatalf("shadow unsupported content type response/backend=%d/%d, want upstream 415/1", response.Code, backendCalls)
				}
			} else if response.Code != http.StatusTooManyRequests || backendCalls != 0 {
				t.Fatalf("enforce unsupported content type response/backend=%d/%d, want existing unknown-size 429/0", response.Code, backendCalls)
			}
			if got := srv.clientProtocolInvalidJSON.Load(); got != 0 {
				t.Fatalf("unsupported content type incremented invalid-JSON protocol errors: %d", got)
			}
		})
	}
}

func TestV0121ValidNonObjectJSONPreservesUpstreamProtocolAuthority(t *testing.T) {
	for _, mode := range []string{"shadow", "enforce"} {
		for _, body := range []string{`null`, `[]`, `"scalar"`} {
			t.Run(mode+"_"+body, func(t *testing.T) {
				adapter, manager := newRequestAwareHTTPAdapter(t, mode)
				backendCalls := 0
				var upstreamBody string
				backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					backendCalls++
					payload, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read upstream body: %v", err)
					}
					upstreamBody = string(payload)
					w.WriteHeader(http.StatusUnprocessableEntity)
				}))
				defer backend.Close()
				srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, mode)
				defer srv.Close()

				response := serveRequestAwareHTTPBody(t, srv, body)
				if response.Code != http.StatusUnprocessableEntity || backendCalls != 1 || upstreamBody != body {
					t.Fatalf("valid JSON response/backend/body=%d/%d/%q, want upstream 422/1/%q", response.Code, backendCalls, upstreamBody, body)
				}
				if srv.clientProtocolInvalidJSON.Load() != 0 || srv.total429.Load() != 0 || manager.Snapshot().Reservations != 0 {
					t.Fatalf("valid JSON polluted local protocol/QoS state: protocol=%d total429=%d manager=%+v",
						srv.clientProtocolInvalidJSON.Load(), srv.total429.Load(), manager.Snapshot())
				}
			})
		}
	}
}

func TestV0121RequestAwarePredictionDurationIsExported(t *testing.T) {
	adapter, _ := newRequestAwareHTTPAdapter(t, "enforce")
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "enforce")
	defer srv.Close()

	response := serveRequestAwareHTTP(t, srv, "timed prediction")
	if response.Code != http.StatusOK {
		t.Fatalf("timed request status=%d body=%q, want 200", response.Code, response.Body.String())
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.PredictionDuration == nil {
		t.Fatal("active request-aware adapter did not expose prediction duration")
	}
	if sample := telemetry.PredictionDuration.Sample(); sample.Count != 1 || sample.Sum < 0 {
		t.Fatalf("prediction-duration sample=%+v, want exactly one non-negative observation", sample)
	}
	var rendered strings.Builder
	srv.writePredictiveAndDynamicMetrics(&rendered)
	if !strings.Contains(rendered.String(), "pig_predictive_admission_prediction_duration_seconds_count 1") {
		t.Fatalf("request-aware prediction duration not exported\n%s", rendered.String())
	}
}

func TestV0121PredictiveModesForwardApplicationRequestWithoutMutation(t *testing.T) {
	const body = `{"model":"model-agnostic","messages":[{"role":"assistant","content":"","tool_calls":[]}],"priority":77,"extra_body":{"priority":88},"max_tokens":8}`
	wantHeaders := map[string]string{
		"X-User-Tier":         "premium",
		"X-PIG-Lane":          "client-lane",
		"X-PIG-Tier":          "client-tier",
		"X-PIG-Output-Tokens": "client-output",
		"X-Client-Trace":      "trace-123",
	}
	for _, mode := range []string{"shadow", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			adapter, _ := newRequestAwareHTTPAdapter(t, mode)
			var gotBody string
			var gotContentLength int64
			gotHeaders := make(map[string]string, len(wantHeaders))
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				payload, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read upstream body: %v", err)
				}
				gotBody = string(payload)
				gotContentLength = r.ContentLength
				for name := range wantHeaders {
					gotHeaders[name] = r.Header.Get(name)
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()
			srv := newRequestAwareHTTPTestServerWithConfig(t, backend.URL, adapter, mode, func(cfg *config) {
				cfg.OutputTokenFields = []string{"max_tokens"}
			})
			defer srv.Close()

			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			request.Header.Set("Authorization", "Bearer secret")
			request.Header.Set("Content-Type", "application/json")
			for name, value := range wantHeaders {
				request.Header.Set(name, value)
			}
			response := httptest.NewRecorder()
			srv.ServeHTTP(response, request)

			if response.Code != http.StatusOK {
				t.Fatalf("transparent request status=%d body=%q, want 200", response.Code, response.Body.String())
			}
			if gotBody != body || gotContentLength != int64(len(body)) {
				t.Fatalf("upstream body/length mutated: got=%q/%d want=%q/%d", gotBody, gotContentLength, body, len(body))
			}
			for name, want := range wantHeaders {
				if got := gotHeaders[name]; got != want {
					t.Errorf("upstream header %s=%q, want unchanged %q", name, got, want)
				}
			}
		})
	}
}

func TestRequestAwareHTTPEnforceDifferentiatesSmallAndLargeBeforeUpstream(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 0, "enforce")
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

	large := serveRequestAwareHTTP(t, srv, strings.Repeat("\u4e2d", 650_000))
	if large.Code != http.StatusTooManyRequests || backendCalls != 1 {
		t.Fatalf("large HTTP response/backend=%d/%d body=%q, want 429/unchanged", large.Code, backendCalls, large.Body.String())
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
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
		`pig_predictive_request_aware_last_decision_info{action="size_protect",reason="prefill_busy",pressure_source="prefill",prefill_class="quiescent"} 1`,
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

func TestRequestAwareHTTPHardKVRejectProjectsSelectiveRouterCapacity(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	backendCalls := 0
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls++
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	srv := newRequestAwareHTTPTestServer(t, backend.URL, adapter, "enforce")
	defer srv.Close()

	response := serveRequestAwareHTTP(t, srv, strings.Repeat("a", 16_000))
	if response.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("request-specific hard response/backend=%d/%d body=%q, want pre-forward 429/0", response.Code, backendCalls, response.Body.String())
	}
	if snapshot := manager.Snapshot(); snapshot.Reservations != 0 {
		t.Fatalf("request-specific hard rejection created reservation: %+v", snapshot)
	}
	var rendered strings.Builder
	srv.writePredictiveAndDynamicMetrics(&rendered)
	for _, want := range []string{
		`pig_predictive_request_aware_last_decision_info{action="hard_protect",reason="kv",pressure_source="none",prefill_class="regular"} 1`,
		"pig_predictive_admission_enforced_rejects_total 1",
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_backpressure_applied 1",
		`pig_predictive_router_backpressure_state_info{scope="load",reason="kv_over_budget",source="deterministic"} 1`,
		"pig_predictive_router_inspect_capacity 1",
	} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("request-specific hard metrics missing %q\n%s", want, rendered.String())
		}
	}
}

func TestRequestAwareHTTPShadowWouldProtectButStillForwards(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 0, "shadow")
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

	response := serveRequestAwareHTTP(t, srv, strings.Repeat("\u4e2d", 650_000))
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

func TestRequestAwareHTTPRegularBurstForwardsWithoutDecodePacingClamp(t *testing.T) {
	adapter, manager := newRequestAwareAdapterTestFixture(t, 5_000, 0)
	held := adapter.Decide(context.Background(), "held-regular", requestAwareAdapterInput(500, 100))
	if held.Reservation == nil {
		t.Fatalf("held regular setup=%+v, want reservation", held)
	}
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

	response := serveRequestAwareHTTP(t, srv, "same observation")
	if response.Code != http.StatusOK || backendCalls != 1 {
		t.Fatalf("regular burst response/backend=%d/%d body=%q, want 200/1", response.Code, backendCalls, response.Body.String())
	}
	telemetry := adapter.PredictiveAdmissionTelemetry()
	if telemetry.RequestAware.Action != runtimepredictive.RequestAwareAdmit ||
		telemetry.RequestAware.Reason != runtimepredictive.RequestAwareReasonOpen ||
		telemetry.RequestAware.PressureSource != runtimepredictive.RequestAwarePressureNone ||
		telemetry.RouterBackpressure.Active || telemetry.Manager.Reservations != 1 {
		t.Fatalf("regular burst telemetry=%+v", telemetry)
	}
	if len(decisionLogs) != 0 {
		t.Fatalf("admitted regular burst emitted protection logs=%+v", decisionLogs)
	}
	var rendered strings.Builder
	srv.writePredictiveAndDynamicMetrics(&rendered)
	for _, want := range []string{
		`pig_predictive_request_aware_last_decision_info{action="admit",reason="open",pressure_source="none",prefill_class="regular"} 1`,
		"pig_predictive_router_backpressure_active 0",
	} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("regular burst metrics missing %q\n%s", want, rendered.String())
		}
	}
	for _, retired := range []string{"decode_pacer", "decode_pacing", "pacer="} {
		if strings.Contains(rendered.String(), retired) || strings.Contains(srv.statusLogLine(), retired) {
			t.Fatalf("retired decode pacing state %q remains in metrics/status", retired)
		}
	}
	if !held.Reservation.Terminate(runtimepredictive.TerminalExpired) || manager.Snapshot().Reservations != 0 {
		t.Fatalf("held regular did not terminate: %+v", manager.Snapshot())
	}
}

func TestV0124RequestAwareHTTPBlocksRegularBehindWeightedPrefillAndRecovers(t *testing.T) {
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "enforce")
	weighted := adapter.Decide(
		context.Background(), "weighted-http-gate", requestAwareAdapterInput(195*1024, 0),
	)
	if weighted.Outcome != predictiveAdmissionOutcomeForward || weighted.Reservation == nil ||
		!weighted.Reservation.MarkForwarded() {
		t.Fatalf("weighted HTTP setup=%+v", weighted)
	}
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

	blocked := serveRequestAwareHTTP(t, srv, "regular during weighted Prefill")
	if blocked.Code != http.StatusTooManyRequests || backendCalls != 0 {
		t.Fatalf("regular during weighted HTTP response/backend=%d/%d body=%q, want pre-forward 429/0",
			blocked.Code, backendCalls, blocked.Body.String())
	}
	protected := adapter.PredictiveAdmissionTelemetry()
	if protected.RequestAware.Action != runtimepredictive.RequestAwareSizeProtect ||
		protected.RequestAware.Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
		protected.RequestAware.PressureSource != runtimepredictive.RequestAwarePressurePrefill ||
		!protected.RouterBackpressure.Active || protected.RouterBackpressure.InspectCapacity != 0 ||
		protected.RequestAware.PendingLongPrefillSequences != 1 ||
		srv.predictiveEnforcedRejects.Load() != 1 || srv.total429.Load() != 1 {
		t.Fatalf("regular during weighted HTTP telemetry=%+v enforced=%d total429=%d",
			protected, srv.predictiveEnforcedRejects.Load(), srv.total429.Load())
	}
	if len(decisionLogs) != 1 || !decisionLogs[0].Enforced ||
		decisionLogs[0].Action != runtimepredictive.RequestAwareSizeProtect ||
		decisionLogs[0].Reason != runtimepredictive.RequestAwareReasonPrefillBusy ||
		decisionLogs[0].PressureSource != runtimepredictive.RequestAwarePressurePrefill {
		t.Fatalf("regular during weighted HTTP decision logs=%+v", decisionLogs)
	}
	var rendered strings.Builder
	srv.writePredictiveAndDynamicMetrics(&rendered)
	for _, want := range []string{
		`pig_predictive_request_aware_last_decision_info{action="size_protect",reason="prefill_busy",pressure_source="prefill",prefill_class="regular"} 1`,
		"pig_predictive_request_aware_pending_long_prefill_sequences 1",
		"pig_predictive_admission_enforced_rejects_total 1",
		"pig_predictive_router_backpressure_active 1",
		"pig_predictive_router_inspect_capacity 0",
	} {
		if !strings.Contains(rendered.String(), want) {
			t.Fatalf("regular during weighted HTTP metrics missing %q\n%s", want, rendered.String())
		}
	}

	if !weighted.Reservation.MarkPrefillComplete() {
		t.Fatal("weighted HTTP Prefill did not complete")
	}
	postPrefill := adapter.PredictiveAdmissionTelemetry()
	if postPrefill.RouterBackpressure.Active || postPrefill.RouterBackpressure.InspectCapacity != 0 ||
		postPrefill.RequestAware.PendingLongPrefillSequences != 0 {
		t.Fatalf("post-Prefill HTTP Router recovery=%+v request=%+v",
			postPrefill.RouterBackpressure, postPrefill.RequestAware)
	}
	recovered := serveRequestAwareHTTP(t, srv, "regular immediately after weighted Prefill")
	if recovered.Code != http.StatusOK || backendCalls != 1 {
		t.Fatalf("regular after weighted HTTP response/backend=%d/%d body=%q, want 200/1",
			recovered.Code, backendCalls, recovered.Body.String())
	}
	if !weighted.Reservation.Terminate(runtimepredictive.TerminalCompleted) || manager.Snapshot().Reservations != 0 {
		t.Fatalf("weighted HTTP lifecycle leaked: %+v", manager.Snapshot())
	}
}

func TestRequestAwareHTTPLongPrefillProtectionIsPreForwardAndObservable(t *testing.T) {
	adapter, _ := newRequestAwareHTTPAdapter(t, "enforce")
	policy, err := runtimepredictive.NewRequestAwarePolicy(runtimepredictive.RequestAwareConfig{
		HardKVLimitTokens: 8_992, BlockSize: 16,
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
		telemetry.RequestAware.EstimatedPrefillTokens < 16 ||
		telemetry.RequestAware.PostAdmitPendingPrefillTokens != 0 ||
		telemetry.RequestAware.LastDecisionPostAdmitPendingPrefillTokens < 16 ||
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
		manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{})
		adapter.manager = manager
		input := adapter.snapshot.(staticRequestAwareSnapshot).input
		input.CapacityTokens = 4 * 1024 * 1024
		input.Running = 1
		input.EffectiveSequences = 1
		input.TPSValid = false
		adapter.snapshot = staticRequestAwareSnapshot{input: input}
		adapter.policy = newLargeRequestAwareServerTestPolicy(t)

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
		manager := runtimepredictive.NewManager("request-aware-http-test", domainpredictive.VirtualState{})
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
		name         string
		prepare      func(*requestAwarePredictiveAdapter, *runtimepredictive.Manager)
		content      string
		wantReason   runtimepredictive.RequestAwareReason
		wantInspect  int
		largeProfile bool
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
			name: "preemption_observed",
			prepare: func(adapter *requestAwarePredictiveAdapter, _ *runtimepredictive.Manager) {
				input := adapter.snapshot.(staticRequestAwareSnapshot).input
				input.PreemptionObserved = true
				adapter.snapshot = staticRequestAwareSnapshot{input: input}
			},
			content:      strings.Repeat("\u4e2d", 100_000),
			wantReason:   runtimepredictive.RequestAwareReasonPreemption,
			wantInspect:  1,
			largeProfile: true,
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
			var adapter *requestAwarePredictiveAdapter
			var manager *runtimepredictive.Manager
			if test.largeProfile {
				adapter, manager = newLargeRequestAwareAdapterTestFixtureWithMode(t, 128*1024, 0, "enforce")
			} else {
				adapter, manager = newRequestAwareHTTPAdapter(t, "enforce")
			}
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

			content := test.content
			if content == "" {
				content = "hard guard"
			}
			response := serveRequestAwareHTTP(t, srv, content)
			if response.Code != http.StatusTooManyRequests || backendCalls != 0 {
				t.Fatalf("hard guard response/backend=%d/%d body=%q, want 429/0", response.Code, backendCalls, response.Body.String())
			}
			telemetry := adapter.PredictiveAdmissionTelemetry()
			if telemetry.RequestAware.Action != runtimepredictive.RequestAwareHardProtect ||
				telemetry.RequestAware.Reason != test.wantReason || !telemetry.RouterBackpressure.Active ||
				telemetry.RouterBackpressure.InspectCapacity != test.wantInspect || telemetry.Attempts.Attempts != 1 {
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
	return newRequestAwareHTTPTestServerWithConfig(t, upstream, adapter, mode, nil)
}

func newRequestAwareHTTPTestServerWithConfig(t testing.TB, upstream string, adapter *requestAwarePredictiveAdapter, mode string, configure func(*config)) *proxyServer {
	t.Helper()
	cfg := testProxyConfig(upstream)
	cfg.PredictiveAdmissionMode = mode
	if configure != nil {
		configure(&cfg)
	}
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
