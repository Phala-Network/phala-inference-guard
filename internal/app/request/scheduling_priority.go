package request

import (
	"bytes"
	"io"
	"net/http"
	"strconv"

	domainrequest "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
)

// SetSchedulingPriority writes `"priority": value` as the last member of the
// buffered JSON request body, so it overrides any priority the client sent:
// the backend's JSON decoder keeps the last of duplicate keys. It reports
// whether the body was protected. JSON bodies the classifier could not buffer
// use a bounded streaming rewrite and an unknown Content-Length.
func SetSchedulingPriority(r *http.Request, value int) bool {
	if r == nil || r.Body == nil || r.URL == nil ||
		domainrequest.EndpointForPath(r.URL.Path) == domainrequest.EndpointUnknown ||
		!requestContentTypeJSON(r.Header.Get("Content-Type")) {
		return false
	}
	body, ok := r.Body.(*preservingReadCloser)
	if !ok || body.buffer == nil {
		return setStreamingSchedulingPriority(r, value)
	}
	reader, ok := body.Reader.(*bytes.Reader)
	if !ok {
		return setStreamingSchedulingPriority(r, value)
	}
	if reader.Len() != body.buffer.Len() {
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
	r.GetBody = nil
	return true
}

func setStreamingSchedulingPriority(r *http.Request, value int) bool {
	member := []byte(`"priority":` + strconv.Itoa(value))
	r.Body = &schedulingPriorityBody{
		source:  r.Body,
		member:  member,
		pending: make([]byte, 0, 4096+len(member)+1),
	}
	r.ContentLength = -1
	r.Header.Del("Content-Length")
	r.GetBody = nil
	return true
}

// schedulingPriorityBody inserts a final root-object member while keeping at
// most one 4 KiB input chunk and its output. It does not repair invalid JSON;
// the backend remains responsible for validating streamed request shapes.
type schedulingPriorityBody struct {
	source     io.ReadCloser
	member     []byte
	input      [4096]byte
	pending    []byte
	offset     int
	readErr    error
	started    bool
	rootObject bool
	depth      int
	inString   bool
	escaped    bool
	hasMember  bool
	injected   bool
}

func (b *schedulingPriorityBody) Read(dst []byte) (int, error) {
	if len(dst) == 0 {
		return 0, nil
	}
	if b.offset == len(b.pending) {
		if b.readErr != nil {
			return 0, b.readErr
		}
		n, err := b.source.Read(b.input[:])
		b.readErr = err
		b.pending = b.pending[:0]
		b.offset = 0
		for _, value := range b.input[:n] {
			b.transform(value)
			b.pending = append(b.pending, value)
		}
		if n == 0 {
			return 0, err
		}
	}
	n := copy(dst, b.pending[b.offset:])
	b.offset += n
	return n, nil
}

func (b *schedulingPriorityBody) transform(value byte) {
	if !b.started {
		if jsonWhitespace(value) {
			return
		}
		b.started = true
		b.rootObject = value == '{'
		b.depth = 1
		return
	}
	if !b.rootObject || b.injected {
		return
	}
	if b.inString {
		if b.escaped {
			b.escaped = false
		} else if value == '\\' {
			b.escaped = true
		} else if value == '"' {
			b.inString = false
		}
		return
	}
	switch value {
	case '"':
		b.inString = true
	case '{', '[':
		b.depth++
	case '}', ']':
		if value == '}' && b.depth == 1 {
			if b.hasMember {
				b.pending = append(b.pending, ',')
			}
			b.pending = append(b.pending, b.member...)
			b.injected = true
		}
		b.depth--
	}
	if b.depth == 1 && !jsonWhitespace(value) {
		b.hasMember = true
	}
}

func (b *schedulingPriorityBody) Close() error { return b.source.Close() }

func jsonWhitespace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}
