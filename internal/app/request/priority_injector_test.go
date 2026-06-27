package request

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	requestclass "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

func TestPriorityInjectorRewritesPremiumToNegativePriority(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{Mode: requestclass.PriorityModeAll})
	req := newPriorityRequest(`{"model":"m"}`)
	req.Header.Set(requestclass.Header, "premium")

	if !injector.Inject(req, requestclass.FromHeader(req)) {
		t.Fatalf("Inject returned false")
	}
	payload := decodeRequestBody(t, req)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	if req.ContentLength != -1 {
		t.Fatalf("ContentLength = %d, want streaming length -1", req.ContentLength)
	}
	if got := req.Header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length header = %q, want empty for streaming body", got)
	}
}

func TestPriorityInjectorRewritesBasicToZeroByDefaultAllMode(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{})
	req := newPriorityRequest(`{"model":"m","priority":-100}`)

	if !injector.Inject(req, requestclass.Basic) {
		t.Fatalf("Inject returned false")
	}
	payload := decodeRequestBody(t, req)
	if payload["priority"].(float64) != 0 {
		t.Fatalf("priority = %v, want 0", payload["priority"])
	}
}

func TestPriorityInjectorCombinesPriorityAndToolCallCleanup(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{
		StripEmptyToolCalls: true,
		CompatBodyBytes:     1024,
		CompatFailOpen:      true,
	})
	req := newPriorityRequest(`{"model":"m","messages":[{"role":"assistant","tool_calls":[]}],"priority":100}`)

	if !injector.Inject(req, requestclass.Premium) {
		t.Fatalf("Inject returned false")
	}
	payload := decodeRequestBody(t, req)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	message := payload["messages"].([]any)[0].(map[string]any)
	if _, ok := message["tool_calls"]; ok {
		t.Fatalf("empty tool_calls was not removed")
	}
	if injector.Stats().Rewritten != 1 {
		t.Fatalf("Rewritten = %d, want 1", injector.Stats().Rewritten)
	}
	if injector.Stats().DurationCount != 1 {
		t.Fatalf("DurationCount = %d, want 1", injector.Stats().DurationCount)
	}
}

func TestPriorityInjectorCompatFailClosedRejectsBeforeProxy(t *testing.T) {
	injector := NewPriorityInjector(PriorityConfig{
		Enabled:             false,
		StripEmptyToolCalls: true,
		CompatBodyBytes:     1024,
		CompatFailOpen:      false,
		Limit:               1,
	})
	req := newPriorityRequest(`{"messages":[`)

	if injector.Inject(req, requestclass.Basic) {
		t.Fatalf("Inject returned true for fail-closed invalid JSON")
	}
}

func TestPriorityInjectorCapsStreamBufferToKnownBodyLength(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{
		StreamBufferBytes: 2 * 1024 * 1024,
	})
	if got, want := injector.streamBufferBytes(1024), 4*1024; got != want {
		t.Fatalf("streamBufferBytes tiny known body = %d, want %d", got, want)
	}
	if got, want := injector.streamBufferBytes(64*1024), 64*1024; got != want {
		t.Fatalf("streamBufferBytes small known body = %d, want %d", got, want)
	}
	if got, want := injector.streamBufferBytes(65*1024), 128*1024; got != want {
		t.Fatalf("streamBufferBytes bucketed known body = %d, want %d", got, want)
	}
	if got, want := injector.streamBufferBytes(8*1024*1024), 2*1024*1024; got != want {
		t.Fatalf("streamBufferBytes large known body = %d, want %d", got, want)
	}
	if got, want := injector.streamBufferBytes(-1), 2*1024*1024; got != want {
		t.Fatalf("streamBufferBytes unknown body = %d, want %d", got, want)
	}
}

func TestPriorityInjectorBufferedRewritePreservesContentLength(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{
		Mode:        requestclass.PriorityModeAll,
		BufferBytes: 1024,
	})
	req := newPriorityRequest(`{"model":"m"}`)

	if !injector.Inject(req, requestclass.Premium) {
		t.Fatalf("Inject returned false")
	}
	body := readRequestBody(t, req)
	if req.ContentLength != int64(len(body)) {
		t.Fatalf("ContentLength = %d, want %d", req.ContentLength, len(body))
	}
	if got, want := req.Header.Get("Content-Length"), "29"; got != want {
		t.Fatalf("Content-Length header = %q, want %q", got, want)
	}
	payload := decodeBody(t, body)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
}

func TestPriorityInjectorAcceptsMixedCaseJSONContentType(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{Mode: requestclass.PriorityModeAll})
	req := newPriorityRequest(`{"model":"m"}`)
	req.Header.Set("Content-Type", "Application/JSON; charset=utf-8")

	if !injector.Inject(req, requestclass.Premium) {
		t.Fatalf("Inject returned false")
	}
	payload := decodeRequestBody(t, req)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
	if injector.Stats().Rewritten != 1 {
		t.Fatalf("Rewritten = %d, want 1", injector.Stats().Rewritten)
	}
}

func TestPriorityInjectorSkipsNonJSONContentType(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{Mode: requestclass.PriorityModeAll})
	req := newPriorityRequest(`{"model":"m"}`)
	req.Header.Set("Content-Type", "text/plain")

	if !injector.Inject(req, requestclass.Premium) {
		t.Fatalf("Inject returned false for fail-open non-json")
	}
	if got := readRequestBody(t, req); got != `{"model":"m"}` {
		t.Fatalf("body = %s, want original body", got)
	}
	if injector.Stats().Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", injector.Stats().Skipped)
	}
}

func TestPriorityInjectorBufferedFailOpenPreservesBody(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{
		Mode:        requestclass.PriorityModeAll,
		BufferBytes: 1024,
	})
	req := newPriorityRequest(`{"model":"m"`)

	if !injector.Inject(req, requestclass.Premium) {
		t.Fatalf("Inject returned false for fail-open invalid JSON")
	}
	if got := readRequestBody(t, req); got != `{"model":"m"` {
		t.Fatalf("body = %s, want original invalid JSON", got)
	}
	if injector.Stats().Failed != 1 {
		t.Fatalf("Failed = %d, want 1", injector.Stats().Failed)
	}
	if injector.Stats().DurationCount != 1 {
		t.Fatalf("DurationCount = %d, want 1", injector.Stats().DurationCount)
	}
}

func TestPriorityInjectorStreamingRewriteErrorReleasesSlot(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{
		Mode:  requestclass.PriorityModeAll,
		Limit: 1,
	})
	req := newPriorityRequest(`[]`)

	if !injector.Inject(req, requestclass.Premium) {
		t.Fatalf("Inject returned false for streaming fail-open non-object JSON")
	}
	if _, err := io.ReadAll(req.Body); err == nil {
		t.Fatalf("ReadAll returned nil error for non-object streaming JSON")
	}
	stats := injector.Stats()
	if stats.Inflight != 0 {
		t.Fatalf("Inflight = %d, want 0 after streaming read error", stats.Inflight)
	}
	if stats.DurationCount != 1 {
		t.Fatalf("DurationCount = %d, want 1", stats.DurationCount)
	}
}

func TestPriorityInjectorOverwritesBasicSpoofedPriorityInAllMode(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{Mode: requestclass.PriorityModeAll})
	req := newPriorityRequest(`{"model":"m","priority":-100}`)

	if !injector.Inject(req, requestclass.Basic) {
		t.Fatalf("Inject returned false")
	}
	payload := decodeRequestBody(t, req)
	if payload["priority"].(float64) != 0 {
		t.Fatalf("priority = %v, want 0", payload["priority"])
	}
}

func TestPriorityInjectorRewritesLargePremiumBody(t *testing.T) {
	prompt := strings.Repeat("x", 3*1024*1024)
	body := `{"model":"m","messages":[{"role":"user","content":"` + prompt + `"}]}`
	injector := newTestPriorityInjector(PriorityConfig{
		Mode:      requestclass.PriorityModePremiumOnly,
		BodyBytes: 32 * 1024 * 1024,
	})
	req := newPriorityRequest(body)

	if !injector.Inject(req, requestclass.Premium) {
		t.Fatalf("Inject returned false")
	}
	payload := decodeRequestBody(t, req)
	if payload["priority"].(float64) != -100 {
		t.Fatalf("priority = %v, want -100", payload["priority"])
	}
}

func TestPriorityInjectorRewritesExtraBodyPriority(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{Mode: requestclass.PriorityModeAll})
	req := newPriorityRequest(`{"model":"m","extra_body":{"priority":100,"foo":1}}`)

	if !injector.Inject(req, requestclass.Premium) {
		t.Fatalf("Inject returned false")
	}
	payload := decodeRequestBody(t, req)
	extraBody := payload["extra_body"].(map[string]any)
	if extraBody["priority"].(float64) != -100 {
		t.Fatalf("extra_body.priority = %v, want -100", extraBody["priority"])
	}
}

func TestPriorityInjectorAppendLastStrategyUsesDuplicatePriority(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{
		Mode:     requestclass.PriorityModeAll,
		Strategy: requestclass.PriorityRewriteStrategyAppendLast,
	})
	req := newPriorityRequest(`{"priority":100,"model":"m"}`)

	if !injector.Inject(req, requestclass.Premium) {
		t.Fatalf("Inject returned false")
	}
	body := readRequestBody(t, req)
	if got, want := body, `{"priority":100,"model":"m","priority":-100}`; got != want {
		t.Fatalf("body = %s, want %s", got, want)
	}
}

func TestPriorityInjectorSkipsBasicInPremiumOnlyMode(t *testing.T) {
	body := `{"model":"m","priority":-100}`
	injector := newTestPriorityInjector(PriorityConfig{Mode: requestclass.PriorityModePremiumOnly})
	req := newPriorityRequest(body)

	if !injector.Inject(req, requestclass.Basic) {
		t.Fatalf("Inject returned false")
	}
	if got := readRequestBody(t, req); got != body {
		t.Fatalf("body = %s, want %s", got, body)
	}
	stats := injector.Stats()
	if stats.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", stats.Skipped)
	}
}

func TestPriorityInjectorOversizedBodyFailOpenAndFailClosed(t *testing.T) {
	req := newPriorityRequest(`{"model":"m"}`)
	req.ContentLength = 12
	injector := newTestPriorityInjector(PriorityConfig{BodyBytes: 4, FailOpen: true})
	if !injector.Inject(req, requestclass.Basic) {
		t.Fatalf("fail-open oversized request returned false")
	}
	if injector.Stats().Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1", injector.Stats().Skipped)
	}

	req = newPriorityRequest(`{"model":"m"}`)
	req.ContentLength = 12
	injector = newTestPriorityInjectorFailClosed(PriorityConfig{BodyBytes: 4})
	if injector.Inject(req, requestclass.Basic) {
		t.Fatalf("fail-closed oversized request returned true")
	}
}

func TestPriorityInjectorStatsExposeDurationBucketsAndMax(t *testing.T) {
	injector := newTestPriorityInjector(PriorityConfig{})

	injector.observeDuration(700 * time.Microsecond)
	injector.observeDuration(3 * time.Millisecond)
	injector.observeDuration(60 * time.Millisecond)

	stats := injector.Stats()
	if stats.DurationCount != 3 {
		t.Fatalf("DurationCount = %d, want 3", stats.DurationCount)
	}
	if stats.DurationMaxSeconds < 0.059999 || stats.DurationMaxSeconds > 0.060001 {
		t.Fatalf("DurationMaxSeconds = %f, want 0.06", stats.DurationMaxSeconds)
	}
	buckets := map[float64]uint64{}
	for _, bucket := range stats.DurationBuckets {
		buckets[bucket.UpperBound] = bucket.Count
	}
	if buckets[0.0005] != 0 {
		t.Fatalf("bucket <=0.0005 = %d, want 0", buckets[0.0005])
	}
	if buckets[0.001] != 1 || buckets[0.005] != 2 || buckets[0.1] != 3 {
		t.Fatalf("bucket counts <=1ms/<=5ms/<=100ms = %d/%d/%d, want 1/2/3", buckets[0.001], buckets[0.005], buckets[0.1])
	}
}

func newTestPriorityInjector(overrides PriorityConfig) *PriorityInjector {
	return NewPriorityInjector(testPriorityConfig(overrides))
}

func newTestPriorityInjectorFailClosed(overrides PriorityConfig) *PriorityInjector {
	cfg := testPriorityConfig(overrides)
	cfg.FailOpen = false
	return NewPriorityInjector(cfg)
}

func testPriorityConfig(overrides PriorityConfig) PriorityConfig {
	cfg := PriorityConfig{
		Enabled:        true,
		Mode:           requestclass.PriorityModeAll,
		Field:          "priority",
		PremiumValue:   -100,
		BasicValue:     0,
		BodyBytes:      1024,
		Limit:          1,
		FailOpen:       true,
		CompatFailOpen: true,
	}
	if overrides.Mode != "" {
		cfg.Mode = overrides.Mode
	}
	if overrides.Strategy != "" {
		cfg.Strategy = overrides.Strategy
	}
	if overrides.Field != "" {
		cfg.Field = overrides.Field
	}
	if overrides.BodyBytes != 0 {
		cfg.BodyBytes = overrides.BodyBytes
	}
	if overrides.BufferBytes != 0 {
		cfg.BufferBytes = overrides.BufferBytes
	}
	if overrides.Limit != 0 {
		cfg.Limit = overrides.Limit
	}
	if overrides.StreamBufferBytes != 0 {
		cfg.StreamBufferBytes = overrides.StreamBufferBytes
	}
	if overrides.PremiumValue != 0 {
		cfg.PremiumValue = overrides.PremiumValue
	}
	if overrides.BasicValue != 0 {
		cfg.BasicValue = overrides.BasicValue
	}
	if overrides.StripEmptyToolCalls {
		cfg.StripEmptyToolCalls = true
	}
	if overrides.CompatBodyBytes != 0 {
		cfg.CompatBodyBytes = overrides.CompatBodyBytes
	}
	if overrides.CompatFailOpen {
		cfg.CompatFailOpen = true
	}
	return cfg
}

func newPriorityRequest(body string) *http.Request {
	req := &http.Request{
		Method:        http.MethodPost,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Header:        make(http.Header),
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

func decodeRequestBody(t *testing.T, req *http.Request) map[string]any {
	t.Helper()
	body := readRequestBody(t, req)
	return decodeBody(t, body)
}

func decodeBody(t *testing.T, body string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		t.Fatalf("json.Unmarshal returned error: %v", err)
	}
	return payload
}

func readRequestBody(t *testing.T, req *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("ReadAll returned error: %v", err)
	}
	return string(body)
}
