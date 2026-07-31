package request

import (
	"bytes"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/kvadmission"
	tokenclassifier "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

type readCloser struct {
	io.Reader
	io.Closer
}

func (c *Classifier) classifyJSONFields(r *http.Request) (tokenclassifier.JSONFields, kvadmission.Cost, []byte, bool) {
	unsupported := kvadmission.Cost{UnsupportedReason: "body_not_scannable"}
	if c.cfg.JSONClassifyBodyBytes <= 0 {
		return tokenclassifier.JSONFields{}, unsupported, nil, false
	}
	if r.Body == nil || r.ContentLength < 0 || r.ContentLength > c.cfg.JSONClassifyBodyBytes {
		if r.ContentLength < 0 {
			unsupported.UnsupportedReason = "unknown_body_length"
		} else if r.ContentLength > c.cfg.JSONClassifyBodyBytes {
			unsupported.UnsupportedReason = "body_too_large"
		}
		return tokenclassifier.JSONFields{}, unsupported, nil, false
	}
	if !c.acquire() {
		unsupported.UnsupportedReason = "classifier_saturated"
		return tokenclassifier.JSONFields{}, unsupported, nil, false
	}
	defer c.release()
	originalBody := r.Body
	originalContentLength := r.ContentLength
	body, err := io.ReadAll(io.LimitReader(r.Body, c.cfg.JSONClassifyBodyBytes+1))
	if err != nil {
		r.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		r.ContentLength = originalContentLength
		unsupported.UnsupportedReason = "body_read_failed"
		return tokenclassifier.JSONFields{}, unsupported, nil, false
	}
	if int64(len(body)) > c.cfg.JSONClassifyBodyBytes {
		r.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		r.ContentLength = originalContentLength
		unsupported.UnsupportedReason = "body_too_large"
		return tokenclassifier.JSONFields{}, unsupported, nil, false
	}
	r.Body = readCloser{Reader: bytes.NewReader(body), Closer: originalBody}
	r.ContentLength = originalContentLength
	fields := c.cfg.OutputTokenFields
	if c.cfg.PredictiveAdmissionMode == "shadow" && len(fields) == 0 {
		fields = []string{"max_tokens", "max_completion_tokens", "max_output_tokens"}
	}
	if !c.cfg.ClassifyOutputTokens && c.cfg.KVAdmissionMode != "shadow" && c.cfg.PredictiveAdmissionMode != "shadow" {
		fields = nil
	}
	parsed, ok := tokenclassifier.ParseJSONFields(body, fields)
	cost := unsupported
	if c.cfg.KVAdmissionMode == "shadow" {
		if !ok {
			cost.UnsupportedReason = "invalid_json"
		} else if requestContentTypeJSON(r.Header.Get("Content-Type")) {
			cost = kvadmission.EstimateJSON(body, parsed.OutputTokens, parsed.HasOutputTokens, c.cfg.KVAdmissionEstimator)
		} else {
			cost.UnsupportedReason = "unsupported_content_type"
		}
	}
	var predictiveBody []byte
	if c.cfg.PredictiveAdmissionMode == "shadow" {
		predictiveBody = append([]byte(nil), body...)
	}
	return parsed, cost, predictiveBody, ok
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

func (c *Classifier) acquire() bool {
	if c.tokens == nil {
		return true
	}
	select {
	case c.tokens <- struct{}{}:
		c.inflight.Add(1)
		return true
	default:
		c.rejected.Add(1)
		return false
	}
}

func (c *Classifier) release() {
	if c.tokens == nil {
		return
	}
	select {
	case <-c.tokens:
		c.inflight.Add(-1)
	default:
	}
}
