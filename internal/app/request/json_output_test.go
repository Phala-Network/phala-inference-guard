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

func TestV01215ClassifierIdleBodyRetentionIsBounded(t *testing.T) {
	const (
		maximumBodyBytes       = int64(4 * 1024 * 1024)
		maximumConcurrent      = 64
		maximumRetainedPayload = int64(32 * 1024 * 1024)
	)
	classifier := New(Config{
		MaximumBodyBytes:  maximumBodyBytes,
		MaximumConcurrent: maximumConcurrent,
	})

	retainedPayload := int64(cap(classifier.bodyPool)) * maximumBodyBytes
	if retainedPayload > maximumRetainedPayload {
		t.Fatalf(
			"idle body pool can retain %d bytes across %d buffers, want at most %d bytes",
			retainedPayload,
			cap(classifier.bodyPool),
			maximumRetainedPayload,
		)
	}
	if cap(classifier.tokens) != maximumConcurrent {
		t.Fatalf("scanner concurrency=%d want %d", cap(classifier.tokens), maximumConcurrent)
	}
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

func TestV01215ClassifierReadsUnknownLengthJSONWithinBound(t *testing.T) {
	body := []byte(`{"model":"model-agnostic","prompt":"chunked","max_tokens":8}`)
	classifier := New(Config{
		MaximumBodyBytes:  int64(len(body)),
		MaximumConcurrent: 1,
		OutputTokenFields: []string{"max_tokens"},
		Estimator:         kvadmission.DefaultEstimatorConfig(),
	})
	request := &http.Request{
		Method:        http.MethodPost,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: -1,
	}

	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil || !classification.Cost.Supported {
		t.Fatalf("bounded unknown-length JSON was not classified: protocol=%+v cost=%+v", protocolError, classification.Cost)
	}
	preserved, err := io.ReadAll(request.Body)
	if err != nil || !bytes.Equal(preserved, body) || request.ContentLength != -1 {
		t.Fatalf(
			"unknown-length body was not preserved: error=%v bytes=%d/%d length=%d",
			err,
			len(preserved),
			len(body),
			request.ContentLength,
		)
	}
}

func TestV01215ClassifierSeparatesBatchAggregateFromMaximumSequence(t *testing.T) {
	prompt := strings.Repeat("word ", 3_000)
	body := []byte(`{"model":"model-agnostic","prompt":["` + prompt + `","` + prompt + `"],"n":2,"max_tokens":256}`)
	classifier := New(Config{
		MaximumBodyBytes:  int64(len(body)),
		MaximumConcurrent: 1,
		OutputTokenFields: []string{"max_tokens"},
		Estimator:         kvadmission.DefaultEstimatorConfig(),
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/completions", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")

	classification, protocolError := classifier.ClassifyRequest(request)
	estimate, known := classification.Cost.PredictiveEstimate()
	if protocolError != nil || !known || estimate.DecodeSequences != 4 ||
		estimate.MaximumSequenceInputTokens <= 0 ||
		estimate.MaximumSequenceInputTokens >= estimate.SelectionInputTokens {
		t.Fatalf("batch estimate did not separate aggregate and per-sequence input: protocol=%+v cost=%+v estimate=%+v/%t", protocolError, classification.Cost, estimate, known)
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

func TestV01218EndpointEstimatorIgnoresControlsAndChargesPromptSemantics(t *testing.T) {
	base := classifyEndpointFixture(
		t,
		"/v1/chat/completions",
		`{"messages":[{"role":"user","content":"hello"}],"max_tokens":32}`,
	)
	ignored := strings.Repeat("ignored-control-", 512)
	noisy := classifyEndpointFixture(
		t,
		"/v1/chat/completions",
		`{"model":"`+ignored+`","messages":[{"role":"user","content":"hello"}],`+
			`"user":"`+ignored+`","metadata":{"trace":"`+ignored+`"},`+
			`"response_format":{"type":"json_schema","json_schema":{"schema":{"description":"`+ignored+`"}}},`+
			`"temperature":0.7,"max_tokens":32}`,
	)
	if !base.Cost.Supported || !noisy.Cost.Supported {
		t.Fatalf("chat endpoint estimate unavailable: base=%+v noisy=%+v", base.Cost, noisy.Cost)
	}
	if base.Cost.Estimate != noisy.Cost.Estimate || base.Cost.TextBytes != noisy.Cost.TextBytes ||
		base.Cost.ToolSchemaBytes != noisy.Cost.ToolSchemaBytes {
		t.Fatalf("ignored controls changed chat estimate: base=%+v noisy=%+v", base.Cost, noisy.Cost)
	}

	longMessage := classifyEndpointFixture(
		t,
		"/v1/chat/completions",
		`{"messages":[{"role":"user","name":"caller","content":"`+strings.Repeat("prompt ", 512)+`"},`+
			`{"role":"assistant","content":null,"tool_calls":[{"type":"function","function":{"name":"lookup","arguments":"{\"key\":\"value\"}"}}]}],`+
			`"max_tokens":32}`,
	)
	withTools := classifyEndpointFixture(
		t,
		"/v1/chat/completions",
		`{"messages":[{"role":"user","content":"hello"}],`+
			`"tools":[{"type":"function","function":{"name":"lookup","description":"look up a value","parameters":{"type":"object","properties":{"key":{"type":"string"}}}}}],`+
			`"max_tokens":32}`,
	)
	if !longMessage.Cost.Supported || !withTools.Cost.Supported ||
		longMessage.Cost.Estimate.SelectionInputTokens <= base.Cost.Estimate.SelectionInputTokens ||
		withTools.Cost.Estimate.SelectionInputTokens <= base.Cost.Estimate.SelectionInputTokens {
		t.Fatalf("Prompt semantics were not charged: base=%+v message=%+v tools=%+v", base.Cost, longMessage.Cost, withTools.Cost)
	}
}

func TestV01218EndpointEstimatorIgnoresEndpointForeignFanoutAndOutputControls(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		base  string
		noisy string
	}{
		{
			name:  "chat",
			path:  "/v1/chat/completions",
			base:  `{"messages":[{"role":"user","content":"hello"}],"max_tokens":32}`,
			noisy: `{"messages":[{"role":"user","content":"hello"}],"max_tokens":32,"max_output_tokens":4096,"best_of":8}`,
		},
		{
			name:  "completions",
			path:  "/v1/completions",
			base:  `{"prompt":"hello","max_tokens":32}`,
			noisy: `{"prompt":"hello","max_tokens":32,"max_completion_tokens":4096,"max_output_tokens":4096}`,
		},
		{
			name:  "responses",
			path:  "/v1/responses",
			base:  `{"input":"hello","max_output_tokens":32}`,
			noisy: `{"input":"hello","max_output_tokens":32,"max_tokens":4096,"max_completion_tokens":4096,"n":8}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := classifyEndpointFixture(t, test.path, test.base)
			noisy := classifyEndpointFixture(t, test.path, test.noisy)
			if !base.Cost.Supported || !noisy.Cost.Supported || base.Cost.Estimate != noisy.Cost.Estimate {
				t.Fatalf("endpoint-foreign controls changed estimate: base=%+v noisy=%+v", base.Cost, noisy.Cost)
			}
		})
	}
}

func TestV01218EndpointEstimatorCountsDecodedJSONStringContent(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		literal string
		escaped string
	}{
		{
			name:    "CJK and simple escapes",
			path:    "/v1/chat/completions",
			literal: `{"messages":[{"role":"user","content":"中文\nquote\""}],"max_tokens":32}`,
			escaped: `{"messages":[{"role":"user","content":"\u4e2d\u6587\nquote\""}],"max_tokens":32}`,
		},
		{
			name:    "surrogate pair",
			path:    "/v1/chat/completions",
			literal: `{"messages":[{"role":"user","content":"😀"}],"max_tokens":32}`,
			escaped: `{"messages":[{"role":"user","content":"\ud83d\ude00"}],"max_tokens":32}`,
		},
		{
			name:    "completion CJK and simple escapes",
			path:    "/v1/completions",
			literal: `{"prompt":"中文\nquote\"","max_tokens":32}`,
			escaped: `{"prompt":"\u4e2d\u6587\nquote\"","max_tokens":32}`,
		},
		{
			name:    "completion surrogate pair",
			path:    "/v1/completions",
			literal: `{"prompt":"😀","max_tokens":32}`,
			escaped: `{"prompt":"\ud83d\ude00","max_tokens":32}`,
		},
		{
			name:    "escaped semantic key",
			path:    "/v1/chat/completions",
			literal: `{"messages":[{"role":"user","name":"caller","content":"hello"}],"max_tokens":32}`,
			escaped: `{"messages":[{"role":"user","\u006eame":"caller","content":"hello"}],"max_tokens":32}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			literal := classifyEndpointFixture(t, test.path, test.literal)
			escaped := classifyEndpointFixture(t, test.path, test.escaped)
			if !literal.Cost.Supported || !escaped.Cost.Supported ||
				literal.Cost.TextBytes != escaped.Cost.TextBytes ||
				literal.Cost.Estimate != escaped.Cost.Estimate {
				t.Fatalf("JSON encoding changed semantic estimate: literal=%+v escaped=%+v", literal.Cost, escaped.Cost)
			}
		})
	}
}

func TestV01218EndpointEstimatorCountsTypedMultimodalPartsOnce(t *testing.T) {
	short := classifyEndpointFixture(
		t,
		"/v1/chat/completions",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"https://example.invalid/image.png"}}]}],"max_tokens":32}`,
	)
	large := classifyEndpointFixture(
		t,
		"/v1/chat/completions",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"describe"},{"type":"image_url","image_url":{"url":"data:image/png;base64,`+strings.Repeat("A", 16*1024)+`"}}]}],"max_tokens":32}`,
	)
	if !short.Cost.Supported || !large.Cost.Supported || short.Cost.ModalityCount != 1 || large.Cost.ModalityCount != 1 {
		t.Fatalf("typed modality classification: short=%+v large=%+v", short.Cost, large.Cost)
	}
	if short.Cost.Estimate != large.Cost.Estimate {
		t.Fatalf("transport URL bytes changed multimodal KV estimate: short=%+v large=%+v", short.Cost.Estimate, large.Cost.Estimate)
	}
}

func TestV01218EndpointEstimatorIgnoresResponsesInputFileTransportBytes(t *testing.T) {
	short := classifyEndpointFixture(
		t,
		"/v1/responses",
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_file","filename":"input.txt","file_data":"QQ=="}]}],"max_output_tokens":32}`,
	)
	large := classifyEndpointFixture(
		t,
		"/v1/responses",
		`{"input":[{"type":"message","role":"user","content":[{"type":"input_file","filename":"input.txt","file_data":"`+strings.Repeat("A", 16*1024)+`"}]}],"max_output_tokens":32}`,
	)
	if !short.Cost.Supported || !large.Cost.Supported ||
		short.Cost.ModalityCount != 1 || large.Cost.ModalityCount != 1 {
		t.Fatalf("Responses input_file classification: short=%+v large=%+v", short.Cost, large.Cost)
	}
	if short.Cost.Estimate != large.Cost.Estimate {
		t.Fatalf("input_file transport bytes changed KV estimate: short=%+v large=%+v", short.Cost.Estimate, large.Cost.Estimate)
	}
}

func TestV01218EndpointEstimatorAccountsCompletionSuffixAndBestOf(t *testing.T) {
	base := classifyEndpointFixture(
		t,
		"/v1/completions",
		`{"prompt":["first","second"],"n":2,"best_of":3,"max_tokens":32}`,
	)
	withSuffix := classifyEndpointFixture(
		t,
		"/v1/completions",
		`{"prompt":["first","second"],"suffix":"`+strings.Repeat("suffix ", 256)+`","n":2,"best_of":3,"max_tokens":32}`,
	)
	if !base.Cost.Supported || !withSuffix.Cost.Supported ||
		base.Cost.Estimate.BasePromptCount != 2 || base.Cost.Estimate.DecodeSequences != 6 ||
		withSuffix.Cost.Estimate.DecodeSequences != 6 ||
		withSuffix.Cost.Estimate.SelectionInputTokens <= base.Cost.Estimate.SelectionInputTokens {
		t.Fatalf("completion shape estimate: base=%+v suffix=%+v", base.Cost, withSuffix.Cost)
	}
	aggregateDelta := withSuffix.Cost.Estimate.SelectionInputTokens - base.Cost.Estimate.SelectionInputTokens
	maximumDelta := withSuffix.Cost.Estimate.MaximumSequenceInputTokens -
		base.Cost.Estimate.MaximumSequenceInputTokens
	minimumAggregateDelta := 2*maximumDelta - (withSuffix.Cost.Estimate.BasePromptCount - 1)
	if aggregateDelta < minimumAggregateDelta || aggregateDelta > 2*maximumDelta {
		t.Fatalf("completion suffix was not charged once per base Prompt: aggregate_delta=%d maximum_delta=%d",
			aggregateDelta, maximumDelta)
	}
}

func TestV01218EndpointEstimatorPreservesExactTokenIDPromptShape(t *testing.T) {
	classification := classifyEndpointFixture(
		t,
		"/v1/completions",
		`{"prompt":[[1,2,3],[4,5]],"n":2,"max_tokens":32}`,
	)
	estimate := classification.Cost.Estimate
	if !classification.Cost.Supported || classification.Cost.ExplicitPromptTokens != 5 ||
		estimate.SelectionInputTokens != 5 || estimate.MaximumSequenceInputTokens != 3 ||
		estimate.BasePromptCount != 2 || estimate.DecodeSequences != 4 {
		t.Fatalf("token-id Prompt shape lost exactness: cost=%+v", classification.Cost)
	}
}

func TestV01218EndpointEstimatorValidatesCompletionFanoutFields(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		supported bool
		sequences int64
	}{
		{
			name:      "null best of is unspecified",
			body:      `{"prompt":["one","two"],"best_of":null,"max_tokens":32}`,
			supported: true,
			sequences: 2,
		},
		{
			name:      "same best of duplicate",
			body:      `{"prompt":["one","two"],"best_of":2,"best_of":2,"max_tokens":32}`,
			supported: true,
			sequences: 4,
		},
		{
			name: "conflicting n duplicate",
			body: `{"prompt":"hello","n":2,"n":3,"max_tokens":32}`,
		},
		{
			name: "conflicting best of duplicate",
			body: `{"prompt":"hello","best_of":2,"best_of":3,"max_tokens":32}`,
		},
		{
			name: "later n cannot erase duplicate conflict",
			body: `{"prompt":"hello","n":2,"n":3,"n":2,"max_tokens":32}`,
		},
		{
			name: "later best of cannot erase duplicate conflict",
			body: `{"prompt":"hello","best_of":2,"best_of":3,"best_of":2,"max_tokens":32}`,
		},
		{
			name: "unknown best of value",
			body: `{"prompt":"hello","best_of":"many","max_tokens":32}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			classification := classifyEndpointFixture(t, "/v1/completions", test.body)
			if test.supported {
				if !classification.Cost.Supported ||
					classification.Cost.Estimate.DecodeSequences != test.sequences {
					t.Fatalf("valid fan-out field was rejected: %+v", classification.Cost)
				}
				return
			}
			if classification.Cost.Supported ||
				classification.Cost.UnsupportedReason != "unsupported_request_shape" {
				t.Fatalf("invalid fan-out field was accepted: %+v", classification.Cost)
			}
		})
	}
}

func TestV01218EndpointEstimatorHandlesResponsesVisibleAndHiddenContext(t *testing.T) {
	base := classifyEndpointFixture(
		t,
		"/v1/responses",
		`{"instructions":"guide","input":"hello","max_output_tokens":32}`,
	)
	ignored := strings.Repeat("ignored-control-", 512)
	noisy := classifyEndpointFixture(
		t,
		"/v1/responses",
		`{"model":"`+ignored+`","instructions":"guide","input":"hello",`+
			`"metadata":{"trace":"`+ignored+`"},"user":"`+ignored+`",`+
			`"text":{"format":{"type":"json_schema","schema":{"description":"`+ignored+`"}}},`+
			`"temperature":0.7,"max_output_tokens":32}`,
	)
	if !base.Cost.Supported || !noisy.Cost.Supported || base.Cost.Estimate != noisy.Cost.Estimate ||
		base.Cost.TextBytes != noisy.Cost.TextBytes || base.Cost.ToolSchemaBytes != noisy.Cost.ToolSchemaBytes {
		t.Fatalf("Responses controls changed visible-context estimate: base=%+v noisy=%+v", base.Cost, noisy.Cost)
	}

	withTools := classifyEndpointFixture(
		t,
		"/v1/responses",
		`{"instructions":"guide","input":"hello","tools":[{"type":"function","name":"lookup","description":"look up a value","parameters":{"type":"object","properties":{"key":{"type":"string"}}}}],"max_output_tokens":32}`,
	)
	if !withTools.Cost.Supported || withTools.Cost.Estimate.SelectionInputTokens <= base.Cost.Estimate.SelectionInputTokens {
		t.Fatalf("Responses tools were not charged: base=%+v tools=%+v", base.Cost, withTools.Cost)
	}

	hidden := classifyEndpointFixture(
		t,
		"/v1/responses",
		`{"input":"hello","previous_response_id":"resp_hidden","max_output_tokens":32}`,
	)
	if hidden.Cost.Supported || hidden.Cost.UnsupportedReason != "body_external_context" {
		t.Fatalf("body-external Responses context was treated as known: %+v", hidden.Cost)
	}

	nestedReference := classifyEndpointFixture(
		t,
		"/v1/responses",
		`{"input":[{"type":"item_reference","id":"item_hidden"}],"max_output_tokens":32}`,
	)
	if nestedReference.Cost.Supported || nestedReference.Cost.UnsupportedReason != "body_external_context" {
		t.Fatalf("nested Responses item reference was treated as visible context: %+v", nestedReference.Cost)
	}

	emptyReference := classifyEndpointFixture(
		t,
		"/v1/responses",
		"{\"instructions\":\"guide\",\"input\":\"hello\",\"previous_input_messages\":[ \n\t ],\"max_output_tokens\":32}",
	)
	if !emptyReference.Cost.Supported || emptyReference.Cost.Estimate != base.Cost.Estimate {
		t.Fatalf("empty Responses external-context field changed estimate: base=%+v empty=%+v", base.Cost, emptyReference.Cost)
	}
}

func TestV01218EndpointEstimatorRejectsUnknownConfiguredEndpoint(t *testing.T) {
	classification := classifyEndpointFixture(t, "/custom/generate", `{"prompt":"hello","max_tokens":32}`)
	if classification.Cost.Supported || classification.Cost.UnsupportedReason != "unsupported_endpoint" {
		t.Fatalf("unknown endpoint classification=%+v", classification.Cost)
	}
}

func classifyEndpointFixture(t *testing.T, path, body string) Classification {
	t.Helper()
	classifier := New(Config{
		Paths:             []string{"/v1/chat/completions", "/v1/completions", "/v1/responses", "/custom/generate"},
		MaximumBodyBytes:  int64(len(body) + 1),
		MaximumConcurrent: 1,
		OutputTokenFields: []string{"max_tokens", "max_completion_tokens", "max_output_tokens"},
		Estimator:         kvadmission.DefaultEstimatorConfig(),
	})
	request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	classification, protocolError := classifier.ClassifyRequest(request)
	if protocolError != nil {
		t.Fatalf("classify %s: protocol=%+v cost=%+v", path, protocolError, classification.Cost)
	}
	preserved, err := io.ReadAll(request.Body)
	if err != nil || string(preserved) != body || request.ContentLength != int64(len(body)) {
		t.Fatalf("classify %s changed body: bytes=%d/%d length=%d error=%v", path, len(preserved), len(body), request.ContentLength, err)
	}
	if err := request.Body.Close(); err != nil {
		t.Fatalf("close classified %s body: %v", path, err)
	}
	return classification
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
