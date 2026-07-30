package request

import (
	"bytes"
	"io"
	"net/http"

	tokenclassifier "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

type readCloser struct {
	io.Reader
	io.Closer
}

func (c *Classifier) classifyJSONFields(r *http.Request) (tokenclassifier.JSONFields, bool) {
	if c.cfg.JSONClassifyBodyBytes <= 0 {
		return tokenclassifier.JSONFields{}, false
	}
	if r.Body == nil || r.ContentLength < 0 || r.ContentLength > c.cfg.JSONClassifyBodyBytes {
		return tokenclassifier.JSONFields{}, false
	}
	if !c.acquire() {
		return tokenclassifier.JSONFields{}, false
	}
	defer c.release()
	originalBody := r.Body
	originalContentLength := r.ContentLength
	body, err := io.ReadAll(io.LimitReader(r.Body, c.cfg.JSONClassifyBodyBytes+1))
	if err != nil {
		r.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		r.ContentLength = originalContentLength
		return tokenclassifier.JSONFields{}, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	if int64(len(body)) > c.cfg.JSONClassifyBodyBytes {
		return tokenclassifier.JSONFields{}, false
	}
	fields := c.cfg.OutputTokenFields
	if !c.cfg.ClassifyOutputTokens {
		fields = nil
	}
	return tokenclassifier.ParseJSONFields(body, fields)
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
