package request

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainrequest "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

type preservingReadCloser struct {
	io.Reader
	original io.Closer
	buffer   *bytes.Buffer
	owner    *Classifier
	once     sync.Once
	err      error
}

func (r *preservingReadCloser) Close() error {
	if r == nil {
		return nil
	}
	r.once.Do(func() {
		if r.original != nil {
			r.err = r.original.Close()
		}
		if r.owner != nil && r.buffer != nil {
			r.owner.releaseBodyBuffer(r.buffer)
			r.buffer = nil
		}
	})
	return r.err
}

func (c *Classifier) classifyJSONFields(r *http.Request) (Classification, *ProtocolError) {
	unsupported := kvadmission.Cost{UnsupportedReason: "body_not_scannable"}
	classification := Classification{Cost: unsupported}
	if r == nil || c.cfg.MaximumBodyBytes <= 0 {
		return classification, nil
	}
	if r.Body == nil || r.ContentLength > c.cfg.MaximumBodyBytes {
		if r.ContentLength > c.cfg.MaximumBodyBytes {
			unsupported.UnsupportedReason = "body_too_large"
		}
		classification.Cost = unsupported
		return classification, nil
	}
	if !requestContentTypeJSON(r.Header.Get("Content-Type")) {
		unsupported.UnsupportedReason = "unsupported_content_type"
		classification.Cost = unsupported
		return classification, nil
	}
	if !c.acquire() {
		unsupported.UnsupportedReason = "classifier_saturated"
		classification.Cost = unsupported
		return classification, nil
	}
	defer c.release()

	originalBody := r.Body
	originalLength := r.ContentLength
	readStarted := time.Now()
	buffer, err := c.readBoundedRequestBody(originalBody, originalLength, c.cfg.MaximumBodyBytes)
	body := buffer.Bytes()
	classification.Timing.BodyRead = time.Since(readStarted)
	classification.Timing.BodyReadMeasured = true
	if err != nil {
		r.Body = c.preserveBody(io.MultiReader(bytes.NewReader(body), originalBody), originalBody, buffer)
		r.ContentLength = originalLength
		unsupported.UnsupportedReason = "body_read_failed"
		classification.Cost = unsupported
		return classification, nil
	}
	if int64(len(body)) > c.cfg.MaximumBodyBytes {
		r.Body = c.preserveBody(io.MultiReader(bytes.NewReader(body), originalBody), originalBody, buffer)
		r.ContentLength = originalLength
		unsupported.UnsupportedReason = "body_too_large"
		classification.Cost = unsupported
		return classification, nil
	}
	r.Body = c.preserveBody(bytes.NewReader(body), originalBody, buffer)
	r.ContentLength = originalLength

	estimatorStarted := time.Now()
	var protocolError *ProtocolError
	classification.Cost, protocolError = c.classifyBufferedJSON(body)
	classification.Timing.Estimator = time.Since(estimatorStarted)
	classification.Timing.EstimatorMeasured = true
	return classification, protocolError
}

func (c *Classifier) classifyBufferedJSON(body []byte) (kvadmission.Cost, *ProtocolError) {
	fields, valid := domainrequest.ParseJSONFields(body, c.cfg.OutputTokenFields)
	if !valid {
		if !json.Valid(body) {
			return kvadmission.Cost{UnsupportedReason: "invalid_json"}, &ProtocolError{Reason: "invalid_json"}
		}
		return kvadmission.Cost{UnsupportedReason: "unsupported_request_shape"}, nil
	}
	if !fields.ShapeSupported {
		return kvadmission.Cost{UnsupportedReason: "unsupported_request_shape"}, nil
	}
	cost := kvadmission.EstimateValidatedJSONWithShape(
		body,
		fields.OutputTokens,
		fields.HasOutputTokens,
		kvadmission.RequestShape{
			PromptBatchSize:             fields.PromptBatchSize,
			PromptStringBytes:           fields.PromptStringBytes,
			MaximumPromptStringBytes:    fields.MaximumPromptStringBytes,
			ExplicitPromptTokens:        fields.ExplicitPromptTokens,
			MaximumExplicitPromptTokens: fields.MaximumExplicitPromptTokens,
			DecodeSequences:             fields.DecodeSequences,
		},
		c.cfg.Estimator,
	)
	return cost, nil
}

func (c *Classifier) readBoundedRequestBody(body io.Reader, contentLength, maximum int64) (*bytes.Buffer, error) {
	limit := maximum + 1
	capacity := contentLength + bytes.MinRead
	if capacity > limit+bytes.MinRead {
		capacity = limit + bytes.MinRead
	}
	buffer := c.acquireBodyBuffer(int(capacity))
	_, err := buffer.ReadFrom(io.LimitReader(body, limit))
	return buffer, err
}

func (c *Classifier) acquireBodyBuffer(capacity int) *bytes.Buffer {
	if c == nil || c.bodyPool == nil {
		buffer := &bytes.Buffer{}
		buffer.Grow(capacity)
		return buffer
	}
	var buffer *bytes.Buffer
	select {
	case buffer = <-c.bodyPool:
	default:
	}
	if buffer == nil {
		buffer = &bytes.Buffer{}
	}
	buffer.Reset()
	buffer.Grow(capacity)
	return buffer
}

func (c *Classifier) releaseBodyBuffer(buffer *bytes.Buffer) {
	if c == nil || c.bodyPool == nil || buffer == nil {
		return
	}
	buffer.Reset()
	select {
	case c.bodyPool <- buffer:
	default:
	}
}

func (c *Classifier) preserveBody(reader io.Reader, original io.Closer, buffer *bytes.Buffer) io.ReadCloser {
	return &preservingReadCloser{
		Reader:   reader,
		original: original,
		buffer:   buffer,
		owner:    c,
	}
}

func requestContentTypeJSON(value string) bool {
	if strings.TrimSpace(value) == "" {
		return true
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return false
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
