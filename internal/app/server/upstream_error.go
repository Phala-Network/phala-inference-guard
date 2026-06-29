package server

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/upstreamerror"
)

const upstreamErrorClassificationBodyBytes = 64 * 1024

type prefixReadCloser struct {
	io.Reader
	closer io.Closer
}

func (p *prefixReadCloser) Close() error {
	return p.closer.Close()
}

func (s *proxyServer) classifyUpstreamErrorResponse(response *http.Response) {
	if !s.cfg.UpstreamErrorClassificationEnabled || response == nil || response.Body == nil {
		return
	}
	if !upstreamerror.Eligible(response.StatusCode, response.Header.Get("Content-Type")) {
		return
	}
	if response.ContentLength > upstreamErrorClassificationBodyBytes {
		return
	}

	original := response.Body
	body, err := io.ReadAll(io.LimitReader(original, upstreamErrorClassificationBodyBytes+1))
	if err != nil {
		response.Body = &prefixReadCloser{Reader: io.MultiReader(bytes.NewReader(body), original), closer: original}
		return
	}
	if len(body) > upstreamErrorClassificationBodyBytes {
		response.Body = &prefixReadCloser{Reader: io.MultiReader(bytes.NewReader(body), original), closer: original}
		return
	}
	_ = original.Close()

	classification, rewritten := upstreamerror.ClassifyAndRewrite(response.StatusCode, response.Header.Get("Content-Type"), body)
	response.Body = io.NopCloser(bytes.NewReader(rewritten))
	if !classification.Matched {
		return
	}

	response.StatusCode = classification.Status
	response.Status = fmt.Sprintf("%d %s", classification.Status, http.StatusText(classification.Status))
	response.ContentLength = int64(len(rewritten))
	response.Header.Set("Content-Length", strconv.Itoa(len(rewritten)))
}
