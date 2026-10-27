package request

import (
	"bytes"
	"net/http"
	"strconv"
)

// SetSchedulingPriority writes `"priority": value` as the last member of the
// buffered JSON request body, so it overrides any priority the client sent:
// the backend's JSON decoder keeps the last of duplicate keys. It reports
// whether the body was rewritten; a body the classifier did not buffer whole
// is left unchanged.
func SetSchedulingPriority(r *http.Request, value int) bool {
	if r == nil {
		return false
	}
	body, ok := r.Body.(*preservingReadCloser)
	if !ok || body.buffer == nil {
		return false
	}
	reader, ok := body.Reader.(*bytes.Reader)
	if !ok || reader.Len() != body.buffer.Len() {
		return false
	}
	original := body.buffer.Bytes()
	end := bytes.LastIndexByte(original, '}')
	if end < 0 || len(bytes.TrimSpace(original[end+1:])) != 0 {
		return false
	}
	start := bytes.IndexByte(original, '{')
	if start < 0 || start >= end {
		return false
	}
	member := `"priority":` + strconv.Itoa(value)
	if len(bytes.TrimSpace(original[start+1:end])) != 0 {
		member = "," + member
	}
	rewritten := make([]byte, 0, len(original)+len(member))
	rewritten = append(rewritten, original[:end]...)
	rewritten = append(rewritten, member...)
	rewritten = append(rewritten, original[end:]...)
	body.Reader = bytes.NewReader(rewritten)
	r.ContentLength = int64(len(rewritten))
	r.Header.Del("Content-Length")
	return true
}
