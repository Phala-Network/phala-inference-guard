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

	domainrequest "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

type preservingReadCloser struct {
	io.Reader
	original          io.Closer
	buffer            *bytes.Buffer
	owner             *Classifier
	reservedBodyBytes int64
	once              sync.Once
	err               error
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
		if r.owner != nil && r.reservedBodyBytes > 0 {
			_ = r.owner.releaseBodyBytes(r.reservedBodyBytes)
			r.reservedBodyBytes = 0
		}
	})
	return r.err
}

func (c *Classifier) classifyJSONFields(r *http.Request) (Classification, *ProtocolError) {
	classification := Classification{UnsupportedReason: "body_not_scannable"}
	if r == nil || c == nil || c.cfg.MaximumBodyBytes <= 0 {
		return classification, nil
	}
	path := ""
	if r.URL != nil {
		path = r.URL.Path
	}
	endpoint := domainrequest.EndpointForPath(path)
	if endpoint == domainrequest.EndpointUnknown {
		classification.UnsupportedReason = "unsupported_endpoint"
		return classification, nil
	}
	if r.Body == nil || r.ContentLength > c.cfg.MaximumBodyBytes {
		if r.ContentLength > c.cfg.MaximumBodyBytes {
			classification.UnsupportedReason = "body_too_large"
			classification.SingleSequenceFallback = true
		}
		return classification, nil
	}
	if !requestContentTypeJSON(r.Header.Get("Content-Type")) {
		classification.UnsupportedReason = "unsupported_content_type"
		classification.SingleSequenceFallback = true
		return classification, nil
	}
	reservedBodyBytes, acquired := c.acquire(r.ContentLength)
	if !acquired {
		classification.UnsupportedReason = "classifier_saturated"
		classification.SingleSequenceFallback = true
		return classification, nil
	}
	defer c.releaseScanner()
	bodyLeaseTransferred := false
	defer func() {
		if !bodyLeaseTransferred {
			_ = c.releaseBodyBytes(reservedBodyBytes)
		}
	}()

	originalBody := r.Body
	originalLength := r.ContentLength
	readStarted := time.Now()
	buffer, err := c.readBoundedRequestBody(originalBody, originalLength, c.cfg.MaximumBodyBytes)
	body := buffer.Bytes()
	classification.Timing.BodyRead = time.Since(readStarted)
	classification.Timing.BodyReadMeasured = true
	if err != nil {
		r.Body = c.preserveBody(io.MultiReader(bytes.NewReader(body), originalBody), originalBody, buffer, reservedBodyBytes)
		bodyLeaseTransferred = true
		r.ContentLength = originalLength
		classification.UnsupportedReason = "body_read_failed"
		classification.SingleSequenceFallback = true
		return classification, nil
	}
	if int64(len(body)) > c.cfg.MaximumBodyBytes {
		r.Body = c.preserveBody(io.MultiReader(bytes.NewReader(body), originalBody), originalBody, buffer, reservedBodyBytes)
		bodyLeaseTransferred = true
		r.ContentLength = originalLength
		classification.UnsupportedReason = "body_too_large"
		classification.SingleSequenceFallback = true
		return classification, nil
	}
	r.Body = c.preserveBody(bytes.NewReader(body), originalBody, buffer, reservedBodyBytes)
	bodyLeaseTransferred = true
	r.ContentLength = originalLength

	scanStarted := time.Now()
	shape, valid := domainrequest.ParseTPSRequestShape(body, endpoint)
	classification.Timing.ShapeScan = time.Since(scanStarted)
	classification.Timing.ShapeScanMeasured = true
	if !valid {
		classification.UnsupportedReason = shape.UnsupportedReason
		if classification.UnsupportedReason == "shape_scan_limit" {
			classification.SingleSequenceFallback = true
			return classification, nil
		}
		classification.UnsupportedReason = "invalid_json"
		if json.Valid(body) {
			classification.UnsupportedReason = "unsupported_request_shape"
			return classification, nil
		}
		return classification, &ProtocolError{Reason: "invalid_json"}
	}
	classification.JSONFieldsKnown = true
	classification.StreamingPresent = shape.StreamingPresent
	classification.StreamingKnown = shape.StreamingKnown
	classification.Streaming = shape.Streaming
	classification.BasePromptCount = shape.BasePromptCount
	classification.DecodeSequences = shape.DecodeSequences
	classification.Supported = shape.Supported
	classification.UnsupportedReason = shape.UnsupportedReason
	if !classification.Supported && classification.UnsupportedReason == "" {
		classification.UnsupportedReason = "unsupported_request_shape"
	}
	return classification, nil
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

func (c *Classifier) preserveBody(
	reader io.Reader,
	original io.Closer,
	buffer *bytes.Buffer,
	reservedBytes int64,
) io.ReadCloser {
	return &preservingReadCloser{
		Reader:            reader,
		original:          original,
		buffer:            buffer,
		owner:             c,
		reservedBodyBytes: reservedBytes,
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
