package request

type EndpointKind uint8

const (
	EndpointUnknown EndpointKind = iota
	EndpointChatCompletions
	EndpointCompletions
	EndpointResponses
)

func EndpointForPath(path string) EndpointKind {
	switch path {
	case "/v1/chat/completions":
		return EndpointChatCompletions
	case "/v1/completions":
		return EndpointCompletions
	case "/v1/responses":
		return EndpointResponses
	default:
		return EndpointUnknown
	}
}
