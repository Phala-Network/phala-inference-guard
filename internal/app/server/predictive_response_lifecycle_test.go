package server

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	runtimepredictive "github.com/Phala-Network/phala-inference-guard/internal/runtime/predictive"
)

func TestV0121PrefillCompletesOnFirstResponseBodyByteNotHeaders(t *testing.T) {
	reservation := &responseLifecycleReservation{}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://upstream.invalid/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new response request: %v", err)
	}
	request = request.WithContext(attachPredictiveReservation(request.Context(), reservation))
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader("data: first\n\nsecond")),
		Request:    request,
	}

	if err := (&proxyServer{}).modifyBackendResponse(response); err != nil {
		t.Fatalf("modify response: %v", err)
	}
	if got := reservation.prefillCalls.Load(); got != 0 {
		t.Fatalf("response headers completed Prefill %d times, want 0", got)
	}

	first := make([]byte, 1)
	if _, err := response.Body.Read(first); err != nil {
		t.Fatalf("read first response byte: %v", err)
	}
	if got := reservation.prefillCalls.Load(); got != 1 {
		t.Fatalf("first response byte completed Prefill %d times, want 1", got)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read remaining response: %v", err)
	}
	if got := reservation.prefillCalls.Load(); got != 1 {
		t.Fatalf("later response reads completed Prefill %d times, want exactly 1", got)
	}
}

type responseLifecycleReservation struct {
	prefillCalls atomic.Uint64
}

func (*responseLifecycleReservation) MarkForwarded() bool { return true }

func (r *responseLifecycleReservation) MarkPrefillComplete() bool {
	r.prefillCalls.Add(1)
	return true
}

func (*responseLifecycleReservation) Terminate(runtimepredictive.TerminalCause) bool { return true }
