package predictive

import "errors"

var ErrUnsupportedRequestClass = errors.New("unsupported tokenizer request class")

type RequestClass string

const (
	RequestClassCompletion RequestClass = "completion"
	RequestClassChat       RequestClass = "chat_completion"
)
