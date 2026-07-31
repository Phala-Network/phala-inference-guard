//go:build pig_native && cgo

package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDefaultNativePredictiveFactoryConstructsRealCountOnlyShadow(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "shadow"
	writePredictiveFactoryTestProfile(t, &cfg)
	srv, err := newProxyServer(cfg)
	if err != nil {
		t.Fatalf("newProxyServer with native predictive profile: %v", err)
	}
	defer func() {
		if err := srv.Close(); err != nil {
			t.Errorf("close native predictive server: %v", err)
		}
	}()
	if _, ok := srv.predictiveShadow.(*realPredictiveShadow); !ok {
		t.Fatalf("default predictive shadow = %T, want *realPredictiveShadow", srv.predictiveShadow)
	}
}
