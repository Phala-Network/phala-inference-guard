//go:build !pig_native || !cgo

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDefaultPredictiveFactoryFailsClosedWithoutNativeTokenizer(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	cfg := testProxyConfig(backend.URL)
	cfg.PredictiveAdmissionMode = "shadow"
	writePredictiveFactoryTestProfile(t, &cfg)
	_, err := newProxyServer(cfg)
	if err == nil || !strings.Contains(err.Error(), "native predictive tokenizer is unavailable") {
		t.Fatalf("newProxyServer error = %v, want explicit non-native fail-closed error", err)
	}
}
