package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func BenchmarkRequestAwareHTTPPreForwardProtection(b *testing.B) {
	adapter, _ := newRequestAwareAdapterTestFixture(b, 5_000, 1)
	srv := newRequestAwareHTTPTestServer(b, "http://127.0.0.1", adapter, "enforce")
	b.Cleanup(func() {
		if err := srv.Close(); err != nil {
			b.Errorf("close benchmark server: %v", err)
		}
	})
	body := `{"model":"model-agnostic","messages":[{"role":"user","content":"` +
		strings.Repeat("a", 3_600) + `"}],"max_tokens":8}`

	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for iteration := 0; iteration < b.N; iteration++ {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer secret")
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, request)
		if response.Code != http.StatusTooManyRequests {
			b.Fatalf("pre-forward protection status=%d body=%q, want 429", response.Code, response.Body.String())
		}
	}
}
