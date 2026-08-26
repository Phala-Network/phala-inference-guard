package request

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type closeTrackingBody struct {
	*bytes.Reader
	closes int
}

func (b *closeTrackingBody) Close() error {
	b.closes++
	return nil
}

type erroringBody struct {
	data   []byte
	read   bool
	closes int
}

type readTrackingBody struct {
	reads  int
	closes int
}

func (b *readTrackingBody) Read([]byte) (int, error) {
	b.reads++
	return 0, io.EOF
}

func (b *readTrackingBody) Close() error {
	b.closes++
	return nil
}

func (b *erroringBody) Read(buffer []byte) (int, error) {
	if b.read {
		return 0, io.EOF
	}
	b.read = true
	return copy(buffer, b.data), io.ErrUnexpectedEOF
}

func (b *erroringBody) Close() error {
	b.closes++
	return nil
}

func TestTPSClassifierPreservesBodyAndReleasesBufferExactlyOnce(t *testing.T) {
	body := []byte(`{"prompt":["one","two"],"n":2,"best_of":3}`)
	original := &closeTrackingBody{Reader: bytes.NewReader(body)}
	classifier := New(Config{MaximumBodyBytes: int64(len(body)), MaximumConcurrent: 1})
	request := httptest.NewRequest(http.MethodPost, "/v1/completions", nil)
	request.Body = original
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Type", "application/json")

	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil || !classification.Supported || classification.BasePromptCount != 2 ||
		classification.DecodeSequences != 6 {
		t.Fatalf("classification=%+v protocol=%+v", classification, protocolError)
	}
	preserved, ok := request.Body.(*preservingReadCloser)
	if !ok || preserved.buffer == nil || len(classifier.bodyPool) != 0 ||
		classifier.ReservedBodyBytes() != int64(len(body)) {
		t.Fatalf("buffer lease body=%T pool=%d reserved=%d", request.Body, len(classifier.bodyPool), classifier.ReservedBodyBytes())
	}
	wantBuffer := preserved.buffer
	forwarded, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Equal(forwarded, body) {
		t.Fatalf("forwarded body=%q error=%v", forwarded, err)
	}
	if request.Body.Close() != nil || request.Body.Close() != nil {
		t.Fatal("idempotent close failed")
	}
	if original.closes != 1 || classifier.ReservedBodyBytes() != 0 || len(classifier.bodyPool) != 1 {
		t.Fatalf("released closes=%d reserved=%d pool=%d", original.closes, classifier.ReservedBodyBytes(), len(classifier.bodyPool))
	}
	reused := classifier.acquireBodyBuffer(len(body))
	if reused != wantBuffer {
		t.Fatal("closed request buffer was not reused")
	}
	classifier.releaseBodyBuffer(reused)
}

func TestTPSClassifierRejectsInvalidJSONBeforeAdmission(t *testing.T) {
	classifier := New(Config{MaximumBodyBytes: 1024, MaximumConcurrent: 1})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[}`))
	request.Header.Set("Content-Type", "application/json")
	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError == nil || protocolError.Reason != "invalid_json" || classification.Supported ||
		classification.UnsupportedReason != "invalid_json" {
		t.Fatalf("classification=%+v protocol=%+v", classification, protocolError)
	}
	assertRequestBody(t, request, `{"messages":[}`)
}

func TestTPSClassifierSeparatesUnsupportedShapesFromProtocolErrors(t *testing.T) {
	classifier := New(Config{MaximumBodyBytes: 1024, MaximumConcurrent: 1})
	tests := []struct {
		name     string
		path     string
		body     string
		header   string
		reason   string
		fallback bool
	}{
		{name: "fanout conflict", path: "/v1/completions", body: `{"prompt":"hello","n":2,"n":3}`, reason: "unsupported_request_shape"},
		{name: "unknown endpoint", path: "/generate", body: `{"prompt":"hello"}`, reason: "unsupported_endpoint"},
		{name: "content type", path: "/v1/chat/completions", body: `not-json`, header: "text/plain", reason: "unsupported_content_type", fallback: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			if test.header != "" {
				request.Header.Set("Content-Type", test.header)
			}
			classification, protocolError := classifier.ClassifyRequest(request)
			if protocolError != nil || classification.Supported ||
				classification.SingleSequenceFallback != test.fallback ||
				classification.UnsupportedReason != test.reason {
				t.Fatalf("classification=%+v protocol=%+v", classification, protocolError)
			}
			assertRequestBody(t, request, test.body)
		})
	}
}

func TestTPSClassifierDoesNotReadOrReserveIneligibleBodies(t *testing.T) {
	classifier := New(Config{MaximumBodyBytes: 16, MaximumConcurrent: 1})
	tests := []struct {
		name          string
		path          string
		contentLength int64
		reason        string
		fallback      bool
	}{
		{name: "unknown endpoint", path: "/generate", contentLength: 8, reason: "unsupported_endpoint"},
		{name: "known oversized body", path: "/v1/chat/completions", contentLength: 17, reason: "body_too_large", fallback: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			body := &readTrackingBody{}
			request := httptest.NewRequest(http.MethodPost, test.path, nil)
			request.Body = body
			request.ContentLength = test.contentLength
			classification, protocolError := classifier.ClassifyRequest(request)
			if protocolError != nil || classification.Supported ||
				classification.SingleSequenceFallback != test.fallback ||
				classification.UnsupportedReason != test.reason {
				t.Fatalf("classification=%+v protocol=%+v", classification, protocolError)
			}
			if body.reads != 0 || classifier.Inflight() != 0 || classifier.ReservedBodyBytes() != 0 ||
				classifier.Rejected() != 0 {
				t.Fatalf("reads=%d inflight=%d reserved=%d rejected=%d", body.reads,
					classifier.Inflight(), classifier.ReservedBodyBytes(), classifier.Rejected())
			}
			if err := request.Body.Close(); err != nil || body.closes != 1 {
				t.Fatalf("close error=%v closes=%d", err, body.closes)
			}
		})
	}
}

func TestTPSClassifierTreatsDepthLimitedValidJSONAsUnsupportedShape(t *testing.T) {
	const nestedLevels = 256
	nested := strings.Repeat("[", nestedLevels) + "0" + strings.Repeat("]", nestedLevels)
	body := `{"messages":` + nested + `}`
	classifier := New(Config{MaximumBodyBytes: int64(len(body)), MaximumConcurrent: 1})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil || classification.Supported ||
		!classification.SingleSequenceFallback || classification.UnsupportedReason != "shape_scan_limit" {
		t.Fatalf("classification=%+v protocol=%+v", classification, protocolError)
	}
	assertRequestBody(t, request, body)
}

func TestTPSClassifierBoundsOutstandingUnknownLengthBodiesAtomically(t *testing.T) {
	const body = `{"prompt":"hello"}`
	const requestCount = 64
	classifier := New(Config{MaximumBodyBytes: 4 * 1024 * 1024, MaximumConcurrent: requestCount})
	requests := make([]*http.Request, requestCount)
	results := make(chan Classification, requestCount)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for index := range requests {
		request := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(body))
		request.ContentLength = -1
		requests[index] = request
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			classification, protocolError := classifier.ClassifyRequest(request)
			if protocolError != nil {
				t.Errorf("protocol error: %+v", protocolError)
			}
			results <- classification
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	supported := 0
	saturated := 0
	for classification := range results {
		switch {
		case classification.Supported:
			supported++
		case classification.UnsupportedReason == "classifier_saturated" && classification.SingleSequenceFallback:
			saturated++
		default:
			t.Fatalf("unexpected classification=%+v", classification)
		}
	}
	if supported != 8 || saturated != requestCount-8 || classifier.Rejected() != requestCount-8 ||
		classifier.ReservedBodyBytes() != 32*1024*1024 {
		t.Fatalf("supported=%d saturated=%d rejected=%d reserved=%d", supported, saturated, classifier.Rejected(), classifier.ReservedBodyBytes())
	}
	for _, request := range requests {
		_ = request.Body.Close()
	}
	if classifier.ReservedBodyBytes() != 0 {
		t.Fatalf("closed requests retained %d bytes", classifier.ReservedBodyBytes())
	}
}

func TestTPSClassifierReadFailurePreservesPartialBodyAndLease(t *testing.T) {
	original := &erroringBody{data: []byte(`{"prompt":"partial`)}
	classifier := New(Config{MaximumBodyBytes: 4 * 1024 * 1024, MaximumConcurrent: 1})
	request := httptest.NewRequest(http.MethodPost, "/v1/completions", nil)
	request.Body = original
	request.ContentLength = -1
	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil || classification.Supported || classification.UnsupportedReason != "body_read_failed" ||
		classifier.ReservedBodyBytes() != 4*1024*1024 {
		t.Fatalf("classification=%+v protocol=%+v reserved=%d", classification, protocolError, classifier.ReservedBodyBytes())
	}
	forwarded, err := io.ReadAll(request.Body)
	if err != nil || string(forwarded) != `{"prompt":"partial` {
		t.Fatalf("partial body=%q error=%v", forwarded, err)
	}
	_ = request.Body.Close()
	_ = request.Body.Close()
	if original.closes != 1 || classifier.ReservedBodyBytes() != 0 {
		t.Fatalf("closes=%d reserved=%d", original.closes, classifier.ReservedBodyBytes())
	}
}

func TestTPSClassifierScansLargeRequestWithoutInputOrOutputEstimation(t *testing.T) {
	content := strings.Repeat("word ", 800_000)
	body := `{"messages":[{"role":"user","content":"` + content + `"}],"n":2,"max_tokens":1000000}`
	classifier := New(Config{MaximumBodyBytes: int64(len(body)), MaximumConcurrent: 1})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil || !classification.Supported || classification.DecodeSequences != 2 ||
		!classification.Timing.BodyReadMeasured || !classification.Timing.ShapeScanMeasured {
		t.Fatalf("classification=%+v protocol=%+v", classification, protocolError)
	}
	assertRequestBody(t, request, body)
}

func assertRequestBody(t *testing.T, request *http.Request, want string) {
	t.Helper()
	got, err := io.ReadAll(request.Body)
	if err != nil || string(got) != want {
		t.Fatalf("body=%q want=%q error=%v", got, want, err)
	}
	_ = request.Body.Close()
}
