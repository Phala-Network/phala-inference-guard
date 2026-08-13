//go:build !race

package request

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
)

// This native-speed acceptance gate covers body read, preservation, strict
// JSON parsing, field extraction, and estimation together. Functional
// classifier tests continue to run under the race detector.
func TestClassifierMaximumBodyLatency(t *testing.T) {
	prefix := []byte(`{"model":"model-agnostic","messages":[{"role":"user","content":"`)
	suffix := []byte(`"}],"max_tokens":256}`)
	body := make([]byte, 0, 4*1024*1024)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("word "), (cap(body)-len(prefix)-len(suffix))/len("word "))...)
	body = append(body, suffix...)
	classifier := New(Config{
		MaximumBodyBytes:  4 * 1024 * 1024,
		MaximumConcurrent: 1,
		OutputTokenFields: []string{"max_tokens"},
		Estimator:         kvadmission.DefaultEstimatorConfig(),
	})

	const runs = 101
	durations := make([]time.Duration, runs)
	for index := range durations {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		started := time.Now()
		classification, protocolError := classifier.ClassifyRequest(request)
		durations[index] = time.Since(started)
		if protocolError != nil || !classification.Cost.Supported {
			t.Fatalf("maximum-body classification failed: protocol=%+v cost=%+v", protocolError, classification.Cost)
		}
		if request.ContentLength != int64(len(body)) {
			t.Fatalf("content length=%d want=%d", request.ContentLength, len(body))
		}
		_ = request.Body.Close()
	}
	sort.Slice(durations, func(left, right int) bool {
		return durations[left] < durations[right]
	})
	p50 := durations[len(durations)/2]
	p99 := durations[(len(durations)*99)/100]
	t.Logf("body_bytes=%d p50=%s p99=%s", len(body), p50, p99)
	if p99 >= 100*time.Millisecond {
		t.Fatalf("maximum-body classifier p99=%s exceeds 100ms", p99)
	}
}
