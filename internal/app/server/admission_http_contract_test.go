package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coreadmission "github.com/Phala-Network/phala-inference-guard/internal/admission"
	domainpredictive "github.com/Phala-Network/phala-inference-guard/internal/domain/predictive"
)

func TestV01218EndpointEstimatorDoesNotChangeRequestOrResponseBytes(t *testing.T) {
	tests := []struct {
		name string
		path string
		body string
	}{
		{
			name: "chat",
			path: "/v1/chat/completions",
			body: `{"model":"model-agnostic","messages":[{"role":"user","content":"hello"}],"max_tokens":8}`,
		},
		{
			name: "completions",
			path: "/v1/completions",
			body: `{"model":"model-agnostic","prompt":"hello","max_tokens":8}`,
		},
		{
			name: "responses",
			path: "/v1/responses",
			body: `{"model":"model-agnostic","input":"hello","max_output_tokens":8}`,
		},
	}
	const responseBody = " \n{\"id\":\"byte-contract\",\"choices\":[]} \t"
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var forwardedPath, forwardedBody string
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				forwardedPath = request.URL.Path
				body, err := io.ReadAll(request.Body)
				if err != nil {
					t.Errorf("read forwarded request: %v", err)
				}
				forwardedBody = string(body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(responseBody))
			}))
			defer backend.Close()
			runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
				Mode: "enforce", KVCapacity: 64_000, MaxModelLen: 4_096,
			})
			srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer secret")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			srv.ServeHTTP(response, request)

			if response.Code != http.StatusOK || forwardedPath != test.path || forwardedBody != test.body ||
				response.Body.String() != responseBody {
				t.Fatalf("endpoint byte contract changed: status=%d path=%q body=%q response=%q",
					response.Code, forwardedPath, forwardedBody, response.Body.String())
			}
		})
	}
}

func TestV01218AdmissionMetricsAccumulateBoundedRequestEvidenceWithoutChangingDecisions(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"completion","choices":[]}`))
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", KVCapacity: 64_000, MaxModelLen: 4_096,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)

	protected := serveAdmissionRequest(t, srv, strings.Repeat("x ", 5_000))
	admitted := serveAdmissionRequest(t, srv, "small")
	if protected.Code != http.StatusTooManyRequests || admitted.Code != http.StatusOK ||
		backendCalls.Load() != 1 {
		t.Fatalf(
			"evidence setup changed decisions: protected=%d admitted=%d backend_calls=%d",
			protected.Code,
			admitted.Code,
			backendCalls.Load(),
		)
	}

	var output bytes.Buffer
	srv.writeLocalMetrics(&output)
	metricsBody := output.String()
	for _, want := range []string{
		`pig_predictive_admission_outcomes_total{outcome="admitted"} 1`,
		`pig_predictive_admission_outcomes_total{outcome="request_protected"} 1`,
		`pig_predictive_admission_protections_total{reason="input_limit",scope="request"} 1`,
		`pig_predictive_admission_estimate_confidence_total{confidence="lexical",outcome="admitted"} 1`,
		`pig_predictive_admission_prefill_class_total{outcome="admitted",prefill_class="regular"} 1`,
		`pig_predictive_admission_decode_fanout_total{bucket="1",outcome="admitted"} 1`,
		`pig_predictive_admission_selection_input_tokens_bucket{le="1024",outcome="admitted"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("cumulative bounded admission evidence missing %q\nmetrics:\n%s", want, metricsBody)
		}
	}
}

func TestV01218RequestShapeEvidenceCoversStreamingClassifierAndFanoutWithoutChangingProxyBehavior(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"completion","choices":[]}`))
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", KVCapacity: 64_000, MaxModelLen: 4_096,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)

	bodies := []string{
		`{"model":"model-agnostic","messages":[{"role":"user","content":"true"}],"stream":true,"n":2,"max_tokens":8}`,
		`{"model":"model-agnostic","messages":[{"role":"user","content":"false"}],"stream":false,"max_tokens":8}`,
		`{"model":"model-agnostic","messages":[{"role":"user","content":"unspecified"}],"max_tokens":8}`,
		`{"model":"model-agnostic","messages":[{"role":"user","content":"invalid stream"}],"stream":"yes","max_tokens":8}`,
		`{"messages":[}`,
	}
	wantStatus := []int{
		http.StatusOK,
		http.StatusOK,
		http.StatusOK,
		http.StatusOK,
		http.StatusBadRequest,
	}
	for index, body := range bodies {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		if response.Code != wantStatus[index] {
			t.Fatalf("shape fixture %d status=%d body=%q, want %d", index, response.Code, response.Body.String(), wantStatus[index])
		}
	}
	if backendCalls.Load() != 4 {
		t.Fatalf("shape evidence changed proxy behavior: backend_calls=%d want=4", backendCalls.Load())
	}

	var output bytes.Buffer
	srv.writeLocalMetrics(&output)
	metricsBody := output.String()
	for _, want := range []string{
		`pig_predictive_classifier_outcomes_total{outcome="supported"} 4`,
		`pig_predictive_classifier_outcomes_total{outcome="invalid_json"} 1`,
		`pig_predictive_scanner_reserved_body_bytes 0`,
		`pig_predictive_request_streaming_total{state="true"} 1`,
		`pig_predictive_request_streaming_total{state="false"} 1`,
		`pig_predictive_request_streaming_total{state="unspecified"} 1`,
		`pig_predictive_request_streaming_total{state="invalid"} 1`,
		`pig_predictive_request_streaming_total{state="unknown"} 1`,
		`pig_predictive_request_decode_fanout_total{bucket="unknown"} 1`,
		`pig_predictive_request_decode_fanout_total{bucket="1"} 3`,
		`pig_predictive_request_decode_fanout_total{bucket="2"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="selection_input",result="known"} 4`,
		`pig_predictive_estimator_validation_total{estimate_kind="context_upper_bound",result="known"} 4`,
		`pig_predictive_estimator_validation_total{estimate_kind="kv_reservation",result="known"} 4`,
		`pig_predictive_estimator_validation_total{estimate_kind="selection_input",result="invalid_json"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="context_upper_bound",result="invalid_json"} 1`,
		`pig_predictive_estimator_validation_total{estimate_kind="kv_reservation",result="invalid_json"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("cumulative request-shape evidence missing %q\nmetrics:\n%s", want, metricsBody)
		}
	}
}

func TestV01218TPSMetricsExposeDecisionAndDenominatorSourcesWithoutChangingAdmission(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"completion","choices":[]}`))
	}))
	defer backend.Close()
	runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", Running: 1, Generation: 100, TPSReference: 25,
	})
	clock.Advance(time.Second)
	publishAdmissionObservationForTest(t, controller, runtime.profile, coreadmission.BackendObservation{
		CapabilityFingerprint: runtime.profile.ModelIdentitySHA256,
		MaxModelLenTokens:     runtime.profile.MaxModelLenTokens,
		KVCapacityTokens:      runtime.profile.KVCapacityTokens,
		KVBlockSize:           runtime.profile.KVBlockSize,
		ObservedAt:            clock.Now(),
		MaximumAge:            time.Hour,
		Running:               1,
		GenerationTokensTotal: 150,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	response := serveAdmissionRequest(t, srv, "warming")
	if response.Code != http.StatusOK || backendCalls.Load() != 1 {
		t.Fatalf(
			"TPS evidence changed warming admission: status=%d backend_calls=%d body=%q",
			response.Code,
			backendCalls.Load(),
			response.Body.String(),
		)
	}

	var output bytes.Buffer
	srv.writeLocalMetrics(&output)
	metricsBody := output.String()
	for _, want := range []string{
		`pig_predictive_tps_decisions_total{result="admit",subreason="warming"} 1`,
		`pig_predictive_tps_denominator_selections_total{source="endpoint"} 1`,
		`pig_predictive_tps_denominator_sequence_seconds_total{source="endpoint"} 1.000000`,
		`pig_predictive_tps_denominator_sequence_seconds_total{source="selected"} 1.000000`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("cumulative TPS evidence missing %q\nmetrics:\n%s", want, metricsBody)
		}
	}
}

func TestV01218ResponseUsageEvidenceDistinguishesAvailableUnavailableMalformedAndCensored(t *testing.T) {
	responses := []string{
		`{"choices":[{}],"usage":{"prompt_tokens":10,"completion_tokens":50}}`,
		`{"choices":[{}]}`,
		`{"choices":[{}],"usage":{"completion_tokens":"bad"}}`,
	}
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		call := int(backendCalls.Add(1)) - 1
		if call < 0 || call >= len(responses) {
			t.Fatalf("unexpected response-usage upstream call %d", call)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(responses[call]))
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", KVCapacity: 64_000, MaxModelLen: 4_096,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)

	serve := func(content string, outputLimit int) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(
			`{"model":"model-agnostic","messages":[{"role":"user","content":%q}],"max_tokens":%d}`,
			content,
			outputLimit,
		)
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		return response
	}

	for index, outputLimit := range []int{64, 256, 1_024} {
		response := serve("usage evidence", outputLimit)
		if response.Code != http.StatusOK || response.Body.String() != responses[index] {
			t.Fatalf(
				"response-usage fixture %d changed proxy response: status=%d body=%q",
				index,
				response.Code,
				response.Body.String(),
			)
		}
	}
	protected := serve(strings.Repeat("x ", 10_000), 4_096)
	if protected.Code != http.StatusTooManyRequests || backendCalls.Load() != 3 {
		t.Fatalf(
			"censored fixture changed admission: status=%d backend_calls=%d body=%q",
			protected.Code,
			backendCalls.Load(),
			protected.Body.String(),
		)
	}

	var output bytes.Buffer
	srv.writeLocalMetrics(&output)
	metricsBody := output.String()
	for _, want := range []string{
		`pig_predictive_successful_completion_tokens_total 50`,
		`pig_predictive_response_usage_outcomes_total{outcome="available"} 1`,
		`pig_predictive_response_usage_outcomes_total{outcome="unavailable"} 1`,
		`pig_predictive_response_usage_outcomes_total{outcome="malformed"} 1`,
		`pig_predictive_response_usage_outcomes_total{outcome="censored"} 1`,
		`pig_predictive_output_limit_comparison_total{actual_bucket="le_64",declared_bucket="le_64"} 1`,
		`pig_predictive_output_limit_comparison_total{actual_bucket="unavailable",declared_bucket="le_256"} 1`,
		`pig_predictive_output_limit_comparison_total{actual_bucket="malformed",declared_bucket="le_1024"} 1`,
		`pig_predictive_output_limit_comparison_total{actual_bucket="censored",declared_bucket="le_4096"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("bounded response-usage evidence missing %q\nmetrics:\n%s", want, metricsBody)
		}
	}
}

func TestV01218ResponseUsageEvidenceUnderstandsResponsesAPIWithoutChangingBytes(t *testing.T) {
	payload := `{"object":"response","status":"completed","output":[],` +
		`"usage":{"input_tokens":10,"output_tokens":200,"total_tokens":210}}`
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", KVCapacity: 64_000, MaxModelLen: 4_096,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	body := `{"model":"model-agnostic","input":"responses evidence","max_output_tokens":256}`
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != payload || backendCalls.Load() != 1 {
		t.Fatalf(
			"Responses usage evidence changed proxy behavior: status=%d body=%q backend_calls=%d",
			response.Code,
			response.Body.String(),
			backendCalls.Load(),
		)
	}

	var output bytes.Buffer
	srv.writeLocalMetrics(&output)
	metricsBody := output.String()
	for _, want := range []string{
		`pig_predictive_successful_completion_tokens_total 200`,
		`pig_predictive_response_usage_outcomes_total{outcome="available"} 1`,
		`pig_predictive_output_limit_comparison_total{actual_bucket="le_256",declared_bucket="le_256"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("Responses usage evidence missing %q\nmetrics:\n%s", want, metricsBody)
		}
	}
}

func TestV01218PrefillLifecycleEvidenceSeparatesFirstByteNoFirstByteAndPreForward(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch backendCalls.Add(1) {
		case 1:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{}]}`))
		case 2:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatal("unexpected Prefill-lifecycle upstream call")
		}
	}))
	defer backend.Close()
	runtime, _, _ := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", KVCapacity: 64_000, MaxModelLen: 4_096,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)

	single := serveAdmissionRequest(t, srv, "single")
	fanoutBody := `{"model":"model-agnostic","messages":[{"role":"user","content":"fanout"}],` +
		`"n":2,"max_tokens":8}`
	fanoutRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fanoutBody))
	fanoutRequest.Header.Set("Authorization", "Bearer secret")
	fanoutRequest.Header.Set("Content-Type", "application/json")
	fanout := httptest.NewRecorder()
	srv.ServeHTTP(fanout, fanoutRequest)
	protected := serveAdmissionRequest(t, srv, strings.Repeat("x ", 10_000))
	if single.Code != http.StatusOK || fanout.Code != http.StatusNoContent ||
		protected.Code != http.StatusTooManyRequests || backendCalls.Load() != 2 {
		t.Fatalf(
			"Prefill lifecycle evidence changed behavior: single=%d fanout=%d protected=%d backend_calls=%d",
			single.Code,
			fanout.Code,
			protected.Code,
			backendCalls.Load(),
		)
	}

	var output bytes.Buffer
	srv.writeLocalMetrics(&output)
	metricsBody := output.String()
	for _, want := range []string{
		`pig_predictive_prefill_lifecycle_total{outcome="first_byte_then_terminal",sequence_shape="single"} 1`,
		`pig_predictive_prefill_lifecycle_total{outcome="forwarded_terminal_before_first_byte",sequence_shape="single_prompt_fanout"} 1`,
		`pig_predictive_prefill_lifecycle_total{outcome="pre_forward_terminal",sequence_shape="single"} 1`,
		`pig_predictive_prefill_first_byte_to_terminal_seconds_count 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("Prefill lifecycle evidence missing %q\nmetrics:\n%s", want, metricsBody)
		}
	}
}

func TestAdmissionHTTPInputEstimateChangesPreForwardDecision(t *testing.T) {
	type outcome struct {
		status       int
		backendCalls int64
		report       admissionReportSnapshot
	}
	run := func(t *testing.T, content string) outcome {
		t.Helper()
		var backendCalls atomic.Int64
		backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			backendCalls.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"completion","choices":[]}`))
		}))
		defer backend.Close()
		runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
			Mode: "enforce", KVCapacity: 5_000, MaxModelLen: 4_096, UsedKVTokens: 3_000,
		})
		srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
		response := serveAdmissionRequest(t, srv, content)
		return outcome{
			status: response.Code, backendCalls: backendCalls.Load(), report: runtime.Snapshot(clock.Now()).Report,
		}
	}

	small := run(t, "small")
	large := run(t, strings.Repeat("a", 12_000))
	if small.status != http.StatusOK || small.backendCalls != 1 || !small.report.LastDecision.Admitted() {
		t.Fatalf("small request outcome=%+v, want pre-forward admission and one upstream call", small)
	}
	if large.status != http.StatusTooManyRequests || large.backendCalls != 0 ||
		large.report.LastDecision.Admitted() || large.report.LastDecision.Scope != coreadmission.ProtectionRequest {
		t.Fatalf("large request outcome=%+v, want request-specific pre-forward protection", large)
	}
	if large.report.LastDecision.Estimate.SelectionInputTokens <= small.report.LastDecision.Estimate.SelectionInputTokens ||
		large.report.LastDecision.Estimate.KVReservationInputTokens <= small.report.LastDecision.Estimate.KVReservationInputTokens {
		t.Fatalf("HTTP estimates did not preserve request-size ordering: small=%+v large=%+v",
			small.report.LastDecision.Estimate, large.report.LastDecision.Estimate)
	}
}

func TestV01215AdmissionHTTPChargesAllDecodeSequencesBeforeForward(t *testing.T) {
	tests := []struct {
		name             string
		path             string
		body             string
		decodeSequences  int64
		minimumInputWork int64
	}{
		{
			name: "chat n",
			path: "/v1/chat/completions",
			body: `{"model":"model-agnostic","messages":[{"role":"user","content":"hello"}],` +
				`"n":8,"max_tokens":256}`,
			decodeSequences: 8,
		},
		{
			name: "completion string prompt batch charges best of",
			path: "/v1/completions",
			body: `{"model":"model-agnostic","prompt":["one","two","three"],` +
				`"n":2,"best_of":4,"max_tokens":256}`,
			decodeSequences: 12,
		},
		{
			name: "completion token id prompt batch",
			path: "/v1/completions",
			body: `{"model":"model-agnostic","prompt":[[1,2,3],[4,5]],` +
				`"n":2,"max_tokens":256}`,
			decodeSequences:  4,
			minimumInputWork: 5,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var backendCalls atomic.Int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				backendCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()
			runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
				Mode: "enforce", TPSReference: 50,
			})
			srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			request.Header.Set("Authorization", "Bearer secret")
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()

			srv.ServeHTTP(response, request)

			decision := runtime.Snapshot(clock.Now()).Report.LastDecision
			if response.Code != http.StatusTooManyRequests || backendCalls.Load() != 0 ||
				decision.Reason != coreadmission.ReasonTPSReference ||
				decision.TPSCurrentSequences != 0 ||
				decision.TPSPostAdmitSequences != test.decodeSequences {
				t.Fatalf(
					"multiplicity was not charged before forward: status=%d calls=%d decision=%+v",
					response.Code,
					backendCalls.Load(),
					decision,
				)
			}
			if decision.Work.FutureKVTokens < test.decodeSequences*256 ||
				decision.Estimate.SelectionInputTokens < test.minimumInputWork {
				t.Fatalf("multiplicity KV/input work is incomplete: decision=%+v", decision)
			}
		})
	}
}

func TestAdmissionHTTPProtectsRepeatedShortLexemeOverContextBeforeForward(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", KVCapacity: 862_437, MaxModelLen: 262_144, KVHardRatio: 0.88,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)

	protected := serveAdmissionRequest(t, srv, strings.Repeat("x ", 270_000))
	snapshot := runtime.Snapshot(clock.Now())
	if protected.Code != http.StatusTooManyRequests || backendCalls.Load() != 0 ||
		snapshot.Report.LastDecision.Reason != coreadmission.ReasonInputLimit ||
		snapshot.Report.LastDecision.Scope != coreadmission.ProtectionRequest ||
		snapshot.Report.LastDecision.Estimate.SelectionInputTokens <= 261_888 {
		t.Fatalf(
			"over-context short lexemes reached upstream or missed ContextGate: status=%d calls=%d snapshot=%+v",
			protected.Code, backendCalls.Load(), snapshot,
		)
	}

	following := serveAdmissionRequest(t, srv, "following supported request")
	if following.Code != http.StatusOK || backendCalls.Load() != 1 ||
		!runtime.Snapshot(clock.Now()).Report.LastDecision.Admitted() {
		t.Fatalf("over-context protection locked following request: status=%d calls=%d snapshot=%+v",
			following.Code, backendCalls.Load(), runtime.Snapshot(clock.Now()))
	}
}

func TestV01215AdmissionHTTPDoesNotValidateFullDeclaredOutputContext(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", KVCapacity: 64_000, MaxModelLen: 4_096,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"model-agnostic","messages":[{"role":"user","content":"x"}],"max_tokens":4096}`),
	)
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	srv.ServeHTTP(response, request)

	decision := runtime.Snapshot(clock.Now()).Report.LastDecision
	if response.Code != http.StatusOK || backendCalls.Load() != 1 || !decision.Admitted() ||
		!decision.Estimate.OutputLimitKnown || decision.Estimate.OutputLimitTokens != 4_096 ||
		decision.Estimate.DecodeHorizonTokens >= decision.Estimate.OutputLimitTokens {
		t.Fatalf(
			"PIG performed backend full-context validation instead of bounded QoS admission: status=%d calls=%d decision=%+v",
			response.Code,
			backendCalls.Load(),
			decision,
		)
	}
}

func TestAdmissionHTTPProtectsHighTokenDensityTextBeforeForward(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
	}{
		{name: "dense-digits", content: strings.Repeat("01", 132_000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var backendCalls atomic.Int64
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				backendCalls.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer backend.Close()
			runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
				Mode: "enforce", KVCapacity: 862_437, MaxModelLen: 262_144, KVHardRatio: 0.88,
			})
			srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)

			protected := serveAdmissionRequest(t, srv, test.content)
			snapshot := runtime.Snapshot(clock.Now())
			if protected.Code != http.StatusTooManyRequests || backendCalls.Load() != 0 ||
				snapshot.Report.LastDecision.Reason != coreadmission.ReasonInputLimit ||
				snapshot.Report.LastDecision.Scope != coreadmission.ProtectionRequest ||
				snapshot.Report.LastDecision.Estimate.SelectionInputTokens <= 261_888 {
				t.Fatalf(
					"high-token-density input reached upstream or missed ContextGate: status=%d calls=%d snapshot=%+v",
					protected.Code, backendCalls.Load(), snapshot,
				)
			}

			following := serveAdmissionRequest(t, srv, "following supported request")
			if following.Code != http.StatusOK || backendCalls.Load() != 1 ||
				!runtime.Snapshot(clock.Now()).Report.LastDecision.Admitted() {
				t.Fatalf("high-token-density protection locked following request: status=%d calls=%d snapshot=%+v",
					following.Code, backendCalls.Load(), runtime.Snapshot(clock.Now()))
			}
		})
	}
}

func TestAdmissionHTTPEnforceProtectionIsOpenAICompatibleAndObservable(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "enforce", KVCapacity: 5_000, MaxModelLen: 4_096, UsedKVTokens: 4_470,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	response := serveAdmissionRequest(t, srv, "protected")

	if response.Code != http.StatusTooManyRequests || backendCalls.Load() != 0 {
		t.Fatalf("enforce response=%d backend_calls=%d body=%q", response.Code, backendCalls.Load(), response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil || payload["error"] == nil {
		t.Fatalf("enforced protection is not an OpenAI-compatible JSON error: err=%v body=%q", err, response.Body.String())
	}
	snapshot := runtime.Snapshot(clock.Now())
	if snapshot.Report.Attempts != 1 || !snapshot.Report.HasLastReject ||
		snapshot.Report.LastReject.Reason == "" || srv.predictiveEnforcedRejects.Load() != 1 {
		t.Fatalf("enforced protection telemetry is incomplete: snapshot=%+v rejects=%d",
			snapshot, srv.predictiveEnforcedRejects.Load())
	}
}

func TestAdmissionHTTPShadowProtectedRequestForwardsWithoutHypotheticalReservation(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"shadow","choices":[]}`))
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{
		Mode: "shadow", KVCapacity: 5_000, MaxModelLen: 4_096, UsedKVTokens: 4_470,
	})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "shadow", runtime)
	response := serveAdmissionRequest(t, srv, "protected in shadow")

	snapshot := runtime.Snapshot(clock.Now())
	if response.Code != http.StatusOK || backendCalls.Load() != 1 || snapshot.Report.ShadowProtectedForwards != 1 ||
		snapshot.Report.LastDecision.Admitted() || snapshot.Capacity.State.LiveReservations != 0 ||
		snapshot.Capacity.State.ResidualDebts != 0 || srv.predictiveEnforcedRejects.Load() != 0 {
		t.Fatalf("shadow protected lifecycle is wrong: status=%d calls=%d snapshot=%+v enforced=%d",
			response.Code, backendCalls.Load(), snapshot, srv.predictiveEnforcedRejects.Load())
	}
}

func TestAdmissionShadowAndEnforceAdmittedLifecyclesAreEquivalent(t *testing.T) {
	estimate := domainpredictive.RequestEstimate{
		SelectionInputTokens: 8 * 1024, MaximumSequenceInputTokens: 8 * 1024,
		KVReservationInputTokens: 8 * 1024, DecodeHorizonTokens: 256,
		MaximumSequenceKVReservationInputTokens: 8 * 1024,
		BasePromptCount:                         1, DecodeSequences: 1,
	}
	type state struct {
		decision coreadmission.DecisionRecord
		before   coreadmission.ProjectedState
		decode   coreadmission.ProjectedState
		terminal coreadmission.ProjectedState
		covered  coreadmission.ProjectedState
	}
	run := func(t *testing.T, mode string) state {
		t.Helper()
		runtime, controller, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: mode})
		decision := runtime.Decide(context.Background(), estimate)
		if !decision.Record.Admitted() || decision.Reservation == nil {
			t.Fatalf("%s admitted decision=%+v", mode, decision)
		}
		before := controller.Snapshot(clock.Now()).State
		if !decision.Reservation.MarkForwarded() || !decision.Reservation.MarkFirstByte() {
			t.Fatalf("%s forward/first-byte transition failed", mode)
		}
		decode := controller.Snapshot(clock.Now()).State
		if !decision.Reservation.Terminate(coreadmission.TerminalSuccess) {
			t.Fatalf("%s terminal transition failed", mode)
		}
		terminal := controller.Snapshot(clock.Now()).State
		clock.Advance(10)
		publishAdmissionObservationForTest(t, controller, runtime.profile, coreadmission.BackendObservation{
			CapabilityFingerprint: runtime.profile.ModelIdentitySHA256,
			MaxModelLenTokens:     runtime.profile.MaxModelLenTokens, KVCapacityTokens: runtime.profile.KVCapacityTokens,
			KVBlockSize: runtime.profile.KVBlockSize, ObservedAt: clock.Now(), MaximumAge: time.Hour,
		})
		covered := controller.Snapshot(clock.Now()).State
		return state{decision: decision.Record, before: before, decode: decode, terminal: terminal, covered: covered}
	}

	enforce := run(t, "enforce")
	shadow := run(t, "shadow")
	if !reflect.DeepEqual(enforce, shadow) {
		t.Fatalf("shadow/enforce admitted evolution diverged:\nenforce=%+v\nshadow=%+v", enforce, shadow)
	}
	if enforce.before.LiveReservations != 1 || enforce.before.PendingPrefillSequences != 1 ||
		enforce.decode.LiveReservations != 1 || enforce.decode.LocalActiveDecode != 1 ||
		enforce.terminal.LiveReservations != 0 || enforce.terminal.ResidualDebts != 1 ||
		enforce.covered.LiveReservations != 0 || enforce.covered.ResidualDebts != 0 {
		t.Fatalf("admitted lifecycle states are incomplete: %+v", enforce)
	}
}

func TestAdmissionHTTPProtocolErrorDoesNotReachPredictionOrUpstream(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[}`))
	request.Header.Set("Authorization", "Bearer secret")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	srv.ServeHTTP(response, request)

	if response.Code != http.StatusBadRequest || backendCalls.Load() != 0 || runtime.Snapshot(clock.Now()).Report.Attempts != 0 {
		t.Fatalf("protocol error status=%d calls=%d report=%+v", response.Code, backendCalls.Load(), runtime.Snapshot(clock.Now()).Report)
	}
}

func TestAdmissionHTTPUnsupportedEstimateProtectsOnlyThatRequest(t *testing.T) {
	var backendCalls atomic.Int64
	backend := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		backendCalls.Add(1)
		response.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{Mode: "enforce"})
	srv := newProxyServerWithAdmissionForTest(t, backend.URL, "enforce", runtime)
	unsupported := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"model":"model-agnostic","messages":[]}`),
	)
	unsupported.Header.Set("Authorization", "Bearer secret")
	unsupported.Header.Set("Content-Type", "text/plain")
	protected := httptest.NewRecorder()

	srv.ServeHTTP(protected, unsupported)

	afterProtect := runtime.Snapshot(clock.Now())
	if protected.Code != http.StatusTooManyRequests || backendCalls.Load() != 0 ||
		afterProtect.Report.LastDecision.Reason != coreadmission.ReasonInvalidRequest ||
		afterProtect.Report.LastDecision.Scope != coreadmission.ProtectionRequest ||
		!afterProtect.Capacity.Available || afterProtect.Capacity.State.LiveReservations != 0 {
		t.Fatalf(
			"unsupported estimate caused wrong protection status=%d calls=%d snapshot=%+v",
			protected.Code, backendCalls.Load(), afterProtect,
		)
	}

	following := serveAdmissionRequest(t, srv, "following supported request")
	afterFollowing := runtime.Snapshot(clock.Now())
	if following.Code != http.StatusOK || backendCalls.Load() != 1 ||
		!afterFollowing.Report.LastDecision.Admitted() || !afterFollowing.Capacity.Available {
		t.Fatalf(
			"unsupported estimate locked following request status=%d calls=%d snapshot=%+v",
			following.Code, backendCalls.Load(), afterFollowing,
		)
	}
}
