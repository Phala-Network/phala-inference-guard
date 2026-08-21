package request

import "strings"

type EndpointKind uint8

const (
	EndpointUnknown EndpointKind = iota
	EndpointChatCompletions
	EndpointCompletions
	EndpointResponses
)

func EndpointForPath(path string, suffixMatch bool) EndpointKind {
	match := func(candidate string) bool {
		return path == candidate || (suffixMatch && strings.HasSuffix(path, candidate))
	}
	switch {
	case match("/v1/chat/completions"):
		return EndpointChatCompletions
	case match("/v1/completions"):
		return EndpointCompletions
	case match("/v1/responses"):
		return EndpointResponses
	default:
		return EndpointUnknown
	}
}
