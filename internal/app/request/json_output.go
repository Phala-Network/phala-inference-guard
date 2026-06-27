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

func (c *Classifier) classifyOutputTokens(r *http.Request) (int, bool) {
	if !c.cfg.ClassifyOutputTokens || c.cfg.JSONClassifyBodyBytes <= 0 || len(c.cfg.OutputTokenFields) == 0 {
		return 0, false
	}
	if r.Body == nil || r.ContentLength < 0 || r.ContentLength > c.cfg.JSONClassifyBodyBytes {
		return 0, false
	}
	if !c.acquire() {
		return 0, false
	}
	defer c.release()
	originalBody := r.Body
	originalContentLength := r.ContentLength
	body, err := io.ReadAll(io.LimitReader(r.Body, c.cfg.JSONClassifyBodyBytes+1))
	if err != nil {
		r.Body = readCloser{Reader: io.MultiReader(bytes.NewReader(body), originalBody), Closer: originalBody}
		r.ContentLength = originalContentLength
		return 0, false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.ContentLength = int64(len(body))
	if int64(len(body)) > c.cfg.JSONClassifyBodyBytes {
		return 0, false
	}
	tokens, ok := tokenclassifier.ParseOutputTokens(body, c.cfg.OutputTokenFields)
	return tokens, ok
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
