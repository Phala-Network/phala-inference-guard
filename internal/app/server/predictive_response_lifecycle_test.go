package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestSuccessfulResponseEOFReleasesReservationWithoutRewritingObservation(t *testing.T) {
	const kib = int64(1024)
	adapter, manager := newLargeRequestAwareAdapterTestFixtureWithMode(t, 0, 0, "enforce")
	setRequestAwareAdapterObservation(t, adapter, manager, 0, 0, 0)
	first := adapter.Decide(
		context.Background(), "response-eof-first-wave", requestAwareAdapterInput(4*kib, 512),
	)
	if first.Outcome != predictiveAdmissionOutcomeForward || first.Reservation == nil ||
		!first.Reservation.MarkForwarded() {
		t.Fatalf("first-wave setup=%+v, want forwarded reservation", first)
	}

	response := newResponseLifecycleHTTPResponse(
		t, context.Background(), http.StatusOK,
		io.NopCloser(strings.NewReader("data: decode\n\n")), first.Reservation,
	)
	if err := (&proxyServer{}).modifyBackendResponse(response); err != nil {
		t.Fatalf("modify first-wave response: %v", err)
	}
	firstByte := make([]byte, 1)
	if _, err := response.Body.Read(firstByte); err != nil {
		t.Fatalf("read first-wave first byte: %v", err)
	}
	setRequestAwareAdapterObservation(t, adapter, manager, 4*kib+512, 1, 0)
	if got := manager.Snapshot().Reservations; got != 1 {
		t.Fatalf("absorbed first-wave reservations=%d, want 1 before EOF", got)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read first-wave response to EOF: %v", err)
	}
	if got := manager.Snapshot().Reservations; got != 0 {
		t.Fatalf("clean EOF retained %d reservations before handler defer, want 0", got)
	}
	if first.Reservation.Terminate(runtimepredictive.TerminalCompleted) {
		t.Fatal("handler-defer fallback terminated an already completed reservation twice")
	}

	stale := adapter.Decide(
		context.Background(), "response-eof-stale", requestAwareAdapterInput(80*kib, 0),
	)
	if stale.Outcome != predictiveAdmissionOutcomeRequestReject || stale.Reservation != nil {
		t.Fatalf("decision against stale observation=%+v, want no fabricated completion credit", stale)
	}
	setRequestAwareAdapterObservation(t, adapter, manager, 0, 0, 0)
	replacement := adapter.Decide(
		context.Background(), "response-eof-replacement", requestAwareAdapterInput(80*kib, 0),
	)
	if replacement.Outcome != predictiveAdmissionOutcomeForward || replacement.Reservation == nil {
		t.Fatalf("replacement after next idle observation=%+v, want pre-forward admission", replacement)
	}
	if !replacement.Reservation.Terminate(runtimepredictive.TerminalExpired) || manager.Snapshot().Reservations != 0 {
		t.Fatalf("replacement cleanup leaked reservation: %+v", manager.Snapshot())
	}
}

func TestV0129SuccessfulResponseBytesAndEOFOrdersPrefillBeforeTerminal(t *testing.T) {
	reservation := &responseLifecycleReservation{}
	response := newResponseLifecycleHTTPResponse(
		t, context.Background(), http.StatusOK,
		&responseBytesAndEOFReadCloser{payload: []byte("complete")}, reservation,
	)
	if err := (&proxyServer{}).modifyBackendResponse(response); err != nil {
		t.Fatalf("modify bytes-plus-EOF response: %v", err)
	}
	buffer := make([]byte, 32)
	read, err := response.Body.Read(buffer)
	if read != len("complete") || !errors.Is(err, io.EOF) {
		t.Fatalf("bytes-plus-EOF read=%d/%v, want %d/EOF", read, err, len("complete"))
	}
	if got := strings.Join(reservation.eventsSnapshot(), ","); got != "prefill,terminal:completed" {
		t.Fatalf("bytes-plus-EOF lifecycle=%q, want prefill before terminal", got)
	}
}

func TestV0129SuccessfulResponseEOFIsExactlyOnce(t *testing.T) {
	reservation := &responseLifecycleReservation{}
	response := newResponseLifecycleHTTPResponse(
		t, context.Background(), http.StatusOK,
		io.NopCloser(strings.NewReader("complete")), reservation,
	)
	if err := (&proxyServer{}).modifyBackendResponse(response); err != nil {
		t.Fatalf("modify repeated-EOF response: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read repeated-EOF response: %v", err)
	}
	buffer := make([]byte, 1)
	for range 2 {
		if read, err := response.Body.Read(buffer); read != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("repeated EOF read=%d/%v, want 0/EOF", read, err)
		}
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close repeated-EOF response: %v", err)
	}
	if got := reservation.prefillCalls.Load(); got != 1 {
		t.Fatalf("repeated EOF Prefill calls=%d, want 1", got)
	}
	if got := reservation.terminalCalls.Load(); got != 1 {
		t.Fatalf("repeated EOF terminal calls=%d, want 1", got)
	}
	if causes := reservation.causesSnapshot(); len(causes) != 1 || causes[0] != runtimepredictive.TerminalCompleted {
		t.Fatalf("repeated EOF terminal causes=%v, want [completed]", causes)
	}
}

func TestV0129SuccessfulEmptyResponseEOFDoesNotCreatePrefill(t *testing.T) {
	reservation := &responseLifecycleReservation{}
	response := newResponseLifecycleHTTPResponse(
		t, context.Background(), http.StatusNoContent,
		io.NopCloser(strings.NewReader("")), reservation,
	)
	if err := (&proxyServer{}).modifyBackendResponse(response); err != nil {
		t.Fatalf("modify empty response: %v", err)
	}
	if _, err := io.ReadAll(response.Body); err != nil {
		t.Fatalf("read empty response: %v", err)
	}
	if got := reservation.prefillCalls.Load(); got != 0 {
		t.Fatalf("empty EOF Prefill calls=%d, want 0", got)
	}
	if got := reservation.terminalCalls.Load(); got != 1 {
		t.Fatalf("empty EOF terminal calls=%d, want 1", got)
	}
}

func TestV0129UnsafeResponseTerminalSignalsStayDeferred(t *testing.T) {
	readFailure := errors.New("upstream body read failure")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	timedOut, timeoutCancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer timeoutCancel()

	tests := []struct {
		name   string
		ctx    context.Context
		status int
		body   io.ReadCloser
		read   bool
	}{
		{name: "close before EOF", ctx: context.Background(), status: http.StatusOK, body: io.NopCloser(strings.NewReader("unread"))},
		{name: "upstream body read failure", ctx: context.Background(), status: http.StatusOK, body: &responseReadErrorCloser{err: readFailure}, read: true},
		{name: "non-2xx EOF", ctx: context.Background(), status: http.StatusBadGateway, body: io.NopCloser(strings.NewReader("upstream error")), read: true},
		{name: "client cancellation before EOF", ctx: cancelled, status: http.StatusOK, body: io.NopCloser(strings.NewReader("cancelled")), read: true},
		{name: "timeout before EOF", ctx: timedOut, status: http.StatusOK, body: io.NopCloser(strings.NewReader("timed out")), read: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reservation := &responseLifecycleReservation{}
			response := newResponseLifecycleHTTPResponse(t, test.ctx, test.status, test.body, reservation)
			if err := (&proxyServer{}).modifyBackendResponse(response); err != nil {
				t.Fatalf("modify unsafe response: %v", err)
			}
			if test.read {
				_, _ = io.ReadAll(response.Body)
			} else if err := response.Body.Close(); err != nil {
				t.Fatalf("close unsafe response: %v", err)
			}
			if got := reservation.terminalCalls.Load(); got != 0 {
				t.Fatalf("unsafe response completed early %d times, want deferred fallback", got)
			}
		})
	}
}

func newResponseLifecycleHTTPResponse(
	t *testing.T,
	ctx context.Context,
	status int,
	body io.ReadCloser,
	reservation predictiveShadowReservation,
) *http.Response {
	t.Helper()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://upstream.invalid/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("new lifecycle request: %v", err)
	}
	request = request.WithContext(attachPredictiveReservation(request.Context(), reservation))
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       body,
		Request:    request,
	}
}

type responseLifecycleReservation struct {
	prefillCalls  atomic.Uint64
	terminalCalls atomic.Uint64
	mu            sync.Mutex
	events        []string
	causes        []runtimepredictive.TerminalCause
}

func (*responseLifecycleReservation) MarkForwarded() bool { return true }

func (r *responseLifecycleReservation) MarkPrefillComplete() bool {
	r.prefillCalls.Add(1)
	r.mu.Lock()
	r.events = append(r.events, "prefill")
	r.mu.Unlock()
	return true
}

func (r *responseLifecycleReservation) Terminate(cause runtimepredictive.TerminalCause) bool {
	r.terminalCalls.Add(1)
	r.mu.Lock()
	r.events = append(r.events, "terminal:"+string(cause))
	r.causes = append(r.causes, cause)
	r.mu.Unlock()
	return true
}

func (r *responseLifecycleReservation) eventsSnapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func (r *responseLifecycleReservation) causesSnapshot() []runtimepredictive.TerminalCause {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]runtimepredictive.TerminalCause(nil), r.causes...)
}

type responseBytesAndEOFReadCloser struct {
	payload []byte
	read    bool
}

func (r *responseBytesAndEOFReadCloser) Read(buffer []byte) (int, error) {
	if r.read {
		return 0, io.EOF
	}
	r.read = true
	return copy(buffer, r.payload), io.EOF
}

func (*responseBytesAndEOFReadCloser) Close() error { return nil }

type responseReadErrorCloser struct {
	err error
}

func (r *responseReadErrorCloser) Read([]byte) (int, error) { return 0, r.err }

func (*responseReadErrorCloser) Close() error { return nil }
