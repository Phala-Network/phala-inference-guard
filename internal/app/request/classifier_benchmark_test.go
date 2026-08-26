package request

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func BenchmarkTPSClassifier4MiB(b *testing.B) {
	prefix := []byte(`{"messages":[{"role":"user","content":"`)
	suffix := []byte(`"}],"n":2,"max_tokens":1000000}`)
	body := make([]byte, 0, 4*1024*1024)
	body = append(body, prefix...)
	body = append(body, bytes.Repeat([]byte("word "), (cap(body)-len(prefix)-len(suffix))/len("word "))...)
	body = append(body, suffix...)
	classifier := New(Config{MaximumBodyBytes: int64(len(body)), MaximumConcurrent: 1})
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		classification, protocolError := classifier.ClassifyRequest(request)
		if protocolError != nil || !classification.Supported || classification.DecodeSequences != 2 {
			b.Fatalf("classification=%+v protocol=%+v", classification, protocolError)
		}
		_ = request.Body.Close()
	}
}
