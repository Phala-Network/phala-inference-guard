package request

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func classifiedRequest(t *testing.T, body string) (*http.Request, *Classifier, *closeTrackingBody) {
	t.Helper()
	original := &closeTrackingBody{Reader: bytes.NewReader([]byte(body))}
	classifier := New(Config{MaximumBodyBytes: 1 << 20, MaximumConcurrent: 1})
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Body = original
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Content-Length", "1")
	if _, protocolError := classifier.ClassifyRequest(request); protocolError != nil {
		t.Fatalf("protocol error %+v", protocolError)
	}
	return request, classifier, original
}

func TestSetSchedulingPriorityOverridesClientPriority(t *testing.T) {
	request, classifier, original := classifiedRequest(t,
		`{"priority":99,"messages":[{"role":"user","content":"hi"}],"max_tokens":8}`+"\n")
	if !SetSchedulingPriority(request, 2) {
		t.Fatal("buffered JSON body was not rewritten")
	}
	forwarded, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if request.ContentLength != int64(len(forwarded)) || request.Header.Get("Content-Length") != "" {
		t.Fatalf("content length=%d header=%q body=%d", request.ContentLength, request.Header.Get("Content-Length"), len(forwarded))
	}
	var decoded struct {
		Priority  int `json:"priority"`
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(forwarded, &decoded); err != nil || decoded.Priority != 2 || decoded.MaxTokens != 8 {
		t.Fatalf("forwarded=%s decoded=%+v err=%v", forwarded, decoded, err)
	}
	if request.Body.Close() != nil || original.closes != 1 || classifier.ReservedBodyBytes() != 0 {
		t.Fatalf("rewritten body did not release its lease: closes=%d reserved=%d", original.closes, classifier.ReservedBodyBytes())
	}
}

func TestSetSchedulingPriorityEmptyObject(t *testing.T) {
	request, _, _ := classifiedRequest(t, `{ }`)
	if !SetSchedulingPriority(request, 0) {
		t.Fatal("empty object was not rewritten")
	}
	forwarded, _ := io.ReadAll(request.Body)
	if !json.Valid(forwarded) || !bytes.Contains(forwarded, []byte(`"priority":0`)) {
		t.Fatalf("forwarded=%s", forwarded)
	}
}

func TestSetSchedulingPriorityLeavesUnknownProtocolsAndPartiallyConsumedBodies(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"a":1}`)))
	request.Header.Set("Content-Type", "application/octet-stream")
	if SetSchedulingPriority(request, 1) {
		t.Fatal("a non-JSON body was rewritten")
	}
	partially, _, _ := classifiedRequest(t, `{"messages":[]}`)
	if _, err := partially.Body.Read(make([]byte, 2)); err != nil {
		t.Fatal(err)
	}
	if SetSchedulingPriority(partially, 1) {
		t.Fatal("a partially forwarded body was rewritten")
	}
}
