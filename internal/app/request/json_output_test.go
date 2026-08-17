package request

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
)

type closeTrackingBody struct {
	*bytes.Reader
	closes int
}

func (b *closeTrackingBody) Close() error {
	b.closes++
	return nil
}

func TestClassifierRecyclesBodyBufferOnlyAfterIdempotentClose(t *testing.T) {
	body := []byte(`{"prompt":"hello","max_tokens":8}`)
	original := &closeTrackingBody{Reader: bytes.NewReader(body)}
	classifier := New(Config{
		MaximumBodyBytes:  int64(len(body)),
		MaximumConcurrent: 1,
		OutputTokenFields: []string{"max_tokens"},
		Estimator:         kvadmission.DefaultEstimatorConfig(),
	})
	request := &http.Request{
		Method:        http.MethodPost,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          original,
		ContentLength: int64(len(body)),
	}

	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil || !classification.Cost.Supported {
		t.Fatalf("classification failed: protocol=%+v cost=%+v", protocolError, classification.Cost)
	}
	preserved, ok := request.Body.(*preservingReadCloser)
	if !ok || preserved.buffer == nil || len(classifier.bodyPool) != 0 {
		t.Fatalf("body buffer was recycled before request close: body=%T pool=%d", request.Body, len(classifier.bodyPool))
	}
	wantBuffer := preserved.buffer
	forwarded, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Equal(forwarded, body) {
		t.Fatalf("forwarded body=%q error=%v", forwarded, err)
	}
	if err := request.Body.Close(); err != nil {
		t.Fatalf("close preserved body: %v", err)
	}
	if err := request.Body.Close(); err != nil {
		t.Fatalf("close preserved body twice: %v", err)
	}
	if original.closes != 1 || len(classifier.bodyPool) != 1 {
		t.Fatalf("close count/pool=%d/%d want 1/1", original.closes, len(classifier.bodyPool))
	}
	reused := classifier.acquireBodyBuffer(len(body))
	if reused != wantBuffer {
		t.Fatal("closed request buffer was not reused")
	}
	classifier.releaseBodyBuffer(reused)
}

func TestV0121UnsupportedContentTypePrecedesJSONSyntaxClassification(t *testing.T) {
	const body = `not-json-but-owned-by-the-upstream`
	classifier := New(Config{
		MaximumBodyBytes:  int64(len(body) + 1),
		MaximumConcurrent: 1,
		Estimator:         kvadmission.DefaultEstimatorConfig(),
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "text/plain; charset=utf-8")

	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil {
		t.Fatalf("unsupported content type became protocol error: %+v", protocolError)
	}
	if classification.Cost.Supported || classification.Cost.UnsupportedReason != "unsupported_content_type" {
		t.Fatalf("unsupported content-type cost=%+v", classification.Cost)
	}
	preserved, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read preserved request body: %v", err)
	}
	if string(preserved) != body || request.ContentLength != int64(len(body)) {
		t.Fatalf("request body/length changed: got=%q/%d want=%q/%d", preserved, request.ContentLength, body, len(body))
	}
}

func TestV0121ClassifierCoversModelNeutral650KTextWindow(t *testing.T) {
	content := strings.Repeat("word ", 650_000)
	body := []byte(`{"model":"model-agnostic","messages":[{"role":"user","content":"` + content + `"}],"max_tokens":8}`)
	classifier := New(Config{
		MaximumBodyBytes:  4 * 1024 * 1024,
		MaximumConcurrent: 1,
		OutputTokenFields: []string{"max_tokens"},
		Estimator:         kvadmission.DefaultEstimatorConfig(),
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil || !classification.Cost.Supported {
		t.Fatalf("650K text window was not classified: protocol=%+v cost=%+v bytes=%d", protocolError, classification.Cost, len(body))
	}
	hint, known := classification.Cost.ApproximatePrefillTokenHint()
	if !known || hint < 500_000 || hint > 800_000 {
		t.Fatalf("650K text window hint=%d/%t, want a bounded model-neutral estimate", hint, known)
	}
	preserved, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read preserved 650K request body: %v", err)
	}
	if !bytes.Equal(preserved, body) || request.ContentLength != int64(len(body)) {
		t.Fatalf("650K request body/length changed: bytes=%d/%d length=%d/%d", len(preserved), len(body), request.ContentLength, len(body))
	}
}

func TestV0121ClassifierPreservesUndeclaredOversizeBody(t *testing.T) {
	const maximum = 32
	body := []byte(`{"model":"model-agnostic","prompt":"this body is longer than its declared length"}`)
	classifier := New(Config{
		MaximumBodyBytes:  maximum,
		MaximumConcurrent: 1,
		OutputTokenFields: []string{"max_tokens"},
		Estimator:         kvadmission.DefaultEstimatorConfig(),
	})
	request := &http.Request{
		Method:        http.MethodPost,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: maximum,
	}

	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil {
		t.Fatalf("oversize body became protocol error: %+v", protocolError)
	}
	if classification.Cost.Supported || classification.Cost.UnsupportedReason != "body_too_large" {
		t.Fatalf("oversize classification=%+v", classification.Cost)
	}
	preserved, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read preserved oversize request body: %v", err)
	}
	if !bytes.Equal(preserved, body) || request.ContentLength != maximum {
		t.Fatalf("oversize request body/length changed: bytes=%d/%d length=%d/%d", len(preserved), len(body), request.ContentLength, maximum)
	}
}

func TestV0121ClassifierKnownLengthAllocationsAreBounded(t *testing.T) {
	prefix := []byte(`{"model":"model-agnostic","messages":[{"role":"user","content":"`)
	suffix := []byte(`"}],"max_tokens":256}`)
	body := make([]byte, 0, 64*1024)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("word "), (cap(body)-len(prefix)-len(suffix))/len("word "))...)
	body = append(body, suffix...)
	classifier := New(Config{
		MaximumBodyBytes:  int64(len(body)),
		MaximumConcurrent: 1,
		OutputTokenFields: []string{"max_tokens"},
		Estimator:         kvadmission.DefaultEstimatorConfig(),
	})
	request := &http.Request{
		Method: http.MethodPost,
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	allocations := testing.AllocsPerRun(10, func() {
		request.Body = io.NopCloser(bytes.NewReader(body))
		request.ContentLength = int64(len(body))
		classification, protocolError := classifier.ClassifyRequest(request)
		if protocolError != nil || !classification.Cost.Supported {
			t.Fatalf("classification failed: protocol=%+v cost=%+v", protocolError, classification.Cost)
		}
		_ = request.Body.Close()
	})
	if allocations > 12 {
		t.Fatalf("known-length classifier allocations=%.1f, want <=12", allocations)
	}
}

func BenchmarkV0121ClassifyJSON4MiB(b *testing.B) {
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
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	b.ResetTimer()
	for b.Loop() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		classification, protocolError := classifier.ClassifyRequest(request)
		if protocolError != nil || !classification.Cost.Supported {
			b.Fatalf("4 MiB classification failed: protocol=%+v cost=%+v", protocolError, classification.Cost)
		}
		_ = request.Body.Close()
	}
}
