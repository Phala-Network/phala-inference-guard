package request

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	domainrequest "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

type preservingReadCloser struct {
	io.Reader
	io.Closer
}

func (c *Classifier) classifyJSONFields(r *http.Request) (Classification, *ProtocolError) {
	unsupported := kvadmission.Cost{UnsupportedReason: "body_not_scannable"}
	classification := Classification{Cost: unsupported}
	if r == nil || c.cfg.MaximumBodyBytes <= 0 {
		return classification, nil
	}
	if r.Body == nil || r.ContentLength < 0 || r.ContentLength > c.cfg.MaximumBodyBytes {
		if r.ContentLength < 0 {
			unsupported.UnsupportedReason = "unknown_body_length"
		} else if r.ContentLength > c.cfg.MaximumBodyBytes {
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
	body, err := readBoundedRequestBody(originalBody, originalLength, c.cfg.MaximumBodyBytes)
	classification.Timing.BodyRead = time.Since(readStarted)
	classification.Timing.BodyReadMeasured = true
	if err != nil {
		r.Body = preservingReadCloser{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		r.ContentLength = originalLength
		unsupported.UnsupportedReason = "body_read_failed"
		classification.Cost = unsupported
		return classification, nil
	}
	if int64(len(body)) > c.cfg.MaximumBodyBytes {
		r.Body = preservingReadCloser{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		r.ContentLength = originalLength
		unsupported.UnsupportedReason = "body_too_large"
		classification.Cost = unsupported
		return classification, nil
	}
	r.Body = preservingReadCloser{Reader: bytes.NewReader(body), Closer: originalBody}
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
		cost := kvadmission.EstimateJSON(body, 0, false, c.cfg.Estimator)
		if cost.Supported {
			return cost, nil
		}
		return kvadmission.Cost{UnsupportedReason: "unsupported_request_shape"}, nil
	}
	cost := kvadmission.EstimateJSON(body, fields.OutputTokens, fields.HasOutputTokens, c.cfg.Estimator)
	return cost, nil
}

func readBoundedRequestBody(body io.Reader, contentLength, maximum int64) ([]byte, error) {
	limit := maximum + 1
	capacity := contentLength + bytes.MinRead
	if capacity > limit+bytes.MinRead {
		capacity = limit + bytes.MinRead
	}
	var buffer bytes.Buffer
	buffer.Grow(int(capacity))
	_, err := buffer.ReadFrom(io.LimitReader(body, limit))
	return buffer.Bytes(), err
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
