package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBackendMetricsExposeFrozenSGLangKind(t *testing.T) {
	runtime, _, clock := newAdmissionRuntimeForTest(t, admissionRuntimeTestConfig{BackendKind: "sglang"})
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer upstream.Close()
	server := newProxyServerWithAdmissionForTest(t, upstream.URL, "enforce", runtime)

	backends := server.backendMetricsInput(runtime.Snapshot(clock.Now()), clock.Now())
	if len(backends) != 1 || backends[0].Status.BackendKind != "sglang" {
		t.Fatalf("backend metrics kind=%#v, want sglang", backends)
	}
}
