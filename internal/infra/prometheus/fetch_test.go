package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchSampleContextRejectsMetricsBodyLargerThanLimit(t *testing.T) {
	prefix := "vllm:generation_tokens_total 1\n"
	body := prefix + strings.Repeat("# padding padding padding\n", 200_000)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	if _, err := FetchSampleContext(context.Background(), client, server.URL); err == nil {
		t.Fatal("oversized, truncated metrics response was accepted as a complete sample")
	}
}
