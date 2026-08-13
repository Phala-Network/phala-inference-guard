package server

import (
	"io"
	"sync"
)

type admissionResponseBodyReadCloser struct {
	io.ReadCloser
	firstOnce    sync.Once
	completeOnce sync.Once
	onFirst      func()
	onComplete   func()
}

func (r *admissionResponseBodyReadCloser) Read(buffer []byte) (int, error) {
	read, err := r.ReadCloser.Read(buffer)
	if read > 0 && r.onFirst != nil {
		r.firstOnce.Do(r.onFirst)
	}
	if err == io.EOF && r.onComplete != nil {
		r.completeOnce.Do(r.onComplete)
	}
	return read, err
}

func observeAdmissionResponseBody(body io.ReadCloser, onFirst, onComplete func()) io.ReadCloser {
	if body == nil || (onFirst == nil && onComplete == nil) {
		return body
	}
	return &admissionResponseBodyReadCloser{
		ReadCloser: body,
		onFirst:    onFirst,
		onComplete: onComplete,
	}
}
