package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCombinedMetricsFailsBeforeWritingPartialSuccess(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		status  int
	}{
		{"upstream_status", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, "private-backend-diagnostic")
		}, http.StatusBadGateway},
		{"header_timeout", func(w http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}, http.StatusGatewayTimeout},
		{"body_timeout", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, "partial_engine_metric 1\n")
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}, http.StatusGatewayTimeout},
		{"truncated_body", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			_, _ = io.WriteString(w, "partial_engine_metric 1\n")
		}, http.StatusBadGateway},
		{"oversize", func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, strings.Repeat("x", maximumBackendMetricsBytes+1))
		}, http.StatusBadGateway},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := httptest.NewServer(tt.handler)
			defer backend.Close()
			srv := &proxyServer{cfg: config{Token: "secret", PredictiveMetricsURL: backend.URL + "/metrics?private-token=secret", PredictiveMetricsRequestTimeout: time.Second}}
			if strings.Contains(tt.name, "timeout") {
				srv.cfg.PredictiveMetricsRequestTimeout = 25 * time.Millisecond
			}
			request := httptest.NewRequest(http.MethodGet, "/v1/metrics", nil)
			request.Header.Set("Authorization", "Bearer secret")
			response := httptest.NewRecorder()
			srv.combinedMetrics(response, request)
			if response.Code != tt.status {
				t.Fatalf("status=%d want=%d", response.Code, tt.status)
			}
			if got := response.Body.String(); got != "backend metrics unavailable\n" {
				t.Fatalf("unsanitized or partial response: %q", got)
			}
		})
	}
}

func TestCombinedMetricsAuthenticationPrecedesBackendFetch(t *testing.T) {
	// An invalid URL would fail fetch; unauthorized callers must still get 401.
	srv := &proxyServer{cfg: config{Token: "secret", PredictiveMetricsURL: ":bad"}}
	response := httptest.NewRecorder()
	srv.combinedMetrics(response, httptest.NewRequest(http.MethodGet, "/v1/metrics", nil))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d", response.Code)
	}
}

func TestBackendMetricsRequestContextAndSizeBoundary(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", maximumBackendMetricsBytes))
	}))
	defer backend.Close()
	srv := &proxyServer{cfg: config{PredictiveMetricsURL: backend.URL, PredictiveMetricsRequestTimeout: time.Second}}
	body, status := srv.fetchBackendMetrics(context.Background())
	if status != http.StatusOK || len(body) != maximumBackendMetricsBytes {
		t.Fatalf("exact limit: status=%d bytes=%d", status, len(body))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	body, status = srv.fetchBackendMetrics(ctx)
	if status != http.StatusBadGateway || body != nil {
		t.Fatalf("cancel ignored: status=%d", status)
	}
	ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	_, status = srv.fetchBackendMetrics(ctx)
	if status != http.StatusGatewayTimeout {
		t.Fatalf("deadline ignored: status=%d", status)
	}
	srv.cfg.PredictiveMetricsURL = ":invalid-private-url"
	_, status = srv.fetchBackendMetrics(context.Background())
	if status != http.StatusBadGateway {
		t.Fatalf("invalid URL status=%d", status)
	}
}
