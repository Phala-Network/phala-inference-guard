package server

import (
	"context"
	"encoding/json"
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
		SelectionInputTokens: 8 * 1024, KVReservationInputTokens: 8 * 1024, DecodeHorizonTokens: 256,
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
