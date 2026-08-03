package httpx

import (
	"bytes"
	"net/http"
	"testing"
	"time"
)

type informationalResponseWriter struct {
	header        http.Header
	finalStatus   int
	informational []int
	body          bytes.Buffer
}

func (w *informationalResponseWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *informationalResponseWriter) WriteHeader(status int) {
	if status >= 100 && status < 200 && status != http.StatusSwitchingProtocols {
		w.informational = append(w.informational, status)
		return
	}
	if w.finalStatus == 0 {
		w.finalStatus = status
	}
}

func (w *informationalResponseWriter) Write(body []byte) (int, error) {
	if w.finalStatus == 0 {
		w.finalStatus = http.StatusOK
	}
	return w.body.Write(body)
}

func TestStatusRecorderIgnoresInformationalResponsesBeforeFinalStatus(t *testing.T) {
	writer := &informationalResponseWriter{}
	recorder := NewStatusRecorder(writer)
	started := time.Now()

	recorder.WriteHeader(http.StatusContinue)
	recorder.WriteHeader(http.StatusEarlyHints)
	if first, ok := recorder.FirstByteSince(started); ok {
		t.Fatalf("informational response recorded first-final-response time %s", first)
	}
	recorder.WriteHeader(http.StatusOK)
	if _, err := recorder.Write([]byte("ok")); err != nil {
		t.Fatalf("write final body: %v", err)
	}

	if got := recorder.StatusOrOK(); got != http.StatusOK {
		t.Fatalf("recorded status = %d, want final 200 after interim 100/103", got)
	}
	if first, ok := recorder.FirstByteSince(started); !ok || first < 0 {
		t.Fatalf("final response first-byte timing = %s/%t, want non-negative observation", first, ok)
	}
	if writer.finalStatus != http.StatusOK {
		t.Fatalf("underlying final status = %d, want 200", writer.finalStatus)
	}
	if len(writer.informational) != 2 || writer.informational[0] != http.StatusContinue || writer.informational[1] != http.StatusEarlyHints {
		t.Fatalf("forwarded informational statuses = %v, want [100 103]", writer.informational)
	}
	if got := writer.body.String(); got != "ok" {
		t.Fatalf("final body = %q, want ok", got)
	}
}

func TestStatusRecorderTreatsSwitchingProtocolsAsFinal(t *testing.T) {
	writer := &informationalResponseWriter{}
	recorder := NewStatusRecorder(writer)

	recorder.WriteHeader(http.StatusSwitchingProtocols)
	recorder.WriteHeader(http.StatusOK)

	if got := recorder.StatusOrOK(); got != http.StatusSwitchingProtocols {
		t.Fatalf("recorded status = %d, want final 101", got)
	}
	if writer.finalStatus != http.StatusSwitchingProtocols {
		t.Fatalf("underlying final status = %d, want 101", writer.finalStatus)
	}
}
