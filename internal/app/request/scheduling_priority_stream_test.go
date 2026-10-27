package request

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/iotest"
)

func TestStreamingSchedulingPriorityPreservesJSONAndOverridesClient(t *testing.T) {
	for _, body := range []string{
		`{}`,
		" \n{ \t }\r\n",
		`{"priority":99,"messages":[{"role":"user","content":"hi"}]}`,
		`{"messages":[{"content":"escaped \\\" } and nested { []"}],"nested":{"priority":99}}` + " \n",
	} {
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
		request.Body = io.NopCloser(iotest.OneByteReader(strings.NewReader(body)))
		request.ContentLength = int64(len(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Content-Length", "123")
		if !SetSchedulingPriority(request, 1) {
			t.Fatal("unbuffered JSON body was not protected")
		}
		forwarded, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]json.RawMessage
		if err := json.Unmarshal(forwarded, &decoded); err != nil {
			t.Fatalf("rewritten JSON is invalid: %v", err)
		}
		if string(decoded["priority"]) != "1" {
			t.Fatalf("backend priority=%s want 1", decoded["priority"])
		}
		if request.ContentLength != -1 || request.Header.Get("Content-Length") != "" || request.GetBody != nil {
			t.Fatal("streamed rewrite retained an unsafe length or replay body")
		}
	}
}

func TestStreamingSchedulingPriorityCoversScannerFallbacks(t *testing.T) {
	for _, fallback := range []string{"known oversized", "unknown oversized", "saturated"} {
		t.Run(fallback, func(t *testing.T) {
			body := `{` + strings.Repeat(" ", 4*1024*1024) + `"messages":[{"role":"user","content":"hi"}],"priority":99}`
			classifier := New(Config{MaximumBodyBytes: 4 * 1024 * 1024, MaximumConcurrent: 1})
			if fallback == "saturated" {
				body = `{"messages":[],"priority":99}`
				classifier.tokens <- struct{}{}
			}
			request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			if fallback == "unknown oversized" {
				request.ContentLength = -1
			}
			classification, protocolError := classifier.ClassifyRequest(request)
			if protocolError != nil || !classification.SingleSequenceFallback {
				t.Fatalf("fixture did not reach scanner fallback: %+v error=%v", classification, protocolError)
			}
			if !SetSchedulingPriority(request, 0) {
				t.Fatal("fallback JSON body was not protected")
			}
			forwarded, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatal(err)
			}
			var decoded struct {
				Priority int `json:"priority"`
			}
			if err := json.Unmarshal(forwarded, &decoded); err != nil || decoded.Priority != 0 {
				t.Fatalf("fallback backend priority=%d error=%v", decoded.Priority, err)
			}
			if err := request.Body.Close(); err != nil || classifier.ReservedBodyBytes() != 0 {
				t.Fatal("fallback rewrite leaked its scanner body lease")
			}
		})
	}
}

func TestStreamingSchedulingPriorityKeepsInvalidJSONInvalid(t *testing.T) {
	for _, body := range []string{`{"a":}`, `{"a":1,}`, `{"a":"unterminated}`, `{} {}`, `{"a":[1,2}}`} {
		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		SetSchedulingPriority(request, 1)
		forwarded, err := io.ReadAll(request.Body)
		if err != nil || json.Valid(forwarded) {
			t.Fatalf("invalid JSON became valid: read error=%v", err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/unknown", strings.NewReader(`{}`))
	if SetSchedulingPriority(request, 1) {
		t.Fatal("unknown endpoint was rewritten")
	}
}

type priorityReadFailure struct{ err error }

func (r priorityReadFailure) Read([]byte) (int, error) { return 0, r.err }

type priorityTrackingBody struct {
	io.Reader
	closes int
}

func (r *priorityTrackingBody) Close() error { r.closes++; return nil }

func TestStreamingSchedulingPriorityPreservesReadErrorAndClose(t *testing.T) {
	want := errors.New("cancelled request body")
	original := &priorityTrackingBody{Reader: io.MultiReader(bytes.NewBufferString(`{"messages":[`), priorityReadFailure{err: want})}
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	request.Header.Set("Content-Type", "application/json")
	request.Body = original
	if !SetSchedulingPriority(request, 1) {
		t.Fatal("unbuffered JSON body was not protected")
	}
	if _, err := io.ReadAll(request.Body); !errors.Is(err, want) {
		t.Fatalf("read error=%v want cancellation", err)
	}
	if err := request.Body.Close(); err != nil || original.closes != 1 {
		t.Fatal("streamed rewrite did not close the original body")
	}
}
