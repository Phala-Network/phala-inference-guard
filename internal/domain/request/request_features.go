package request

// RequestFeatures holds bounded enums for generation options a request uses.
// It never retains request content.
type RequestFeatures struct {
	Logprobs       LogprobsBucket
	ToolChoice     ToolChoice
	ToolsPresent   bool
	ResponseFormat ResponseFormat
}

type LogprobsBucket uint8

const (
	LogprobsNone LogprobsBucket = iota
	LogprobsZero
	LogprobsOneToFive
	LogprobsSixToTwenty
	LogprobsOverTwenty
	LogprobsInvalid
	LogprobsBucketCount
)

type ToolChoice uint8

const (
	ToolChoiceUnspecified ToolChoice = iota
	ToolChoiceNone
	ToolChoiceAuto
	ToolChoiceRequired
	ToolChoiceNamed
	ToolChoiceOther
	ToolChoiceCount
)

type ResponseFormat uint8

const (
	ResponseFormatUnspecified ResponseFormat = iota
	ResponseFormatText
	ResponseFormatJSONObject
	ResponseFormatJSONSchema
	ResponseFormatStructuralTag
	ResponseFormatRegex
	ResponseFormatGrammar
	ResponseFormatChoice
	ResponseFormatOther
	ResponseFormatCount
)

const responsesLogprobsInclude = "message.output_text.logprobs"

// requestFeatureFields keeps the last raw value of each relevant root member;
// JSON decoders used by the backends keep the last duplicate key.
type requestFeatureFields struct {
	logprobs       []byte
	topLogprobs    []byte
	include        []byte
	toolChoice     []byte
	tools          []byte
	responseFormat []byte
	text           []byte
	constraint     ResponseFormat
}

func (f *requestFeatureFields) observe(key jsonStringSpan, value []byte) {
	switch {
	case jsonStringSpanEquals(key, "logprobs"):
		f.logprobs = value
	case jsonStringSpanEquals(key, "top_logprobs"):
		f.topLogprobs = value
	case jsonStringSpanEquals(key, "include"):
		f.include = value
	case jsonStringSpanEquals(key, "tool_choice"):
		f.toolChoice = value
	case jsonStringSpanEquals(key, "tools"):
		f.tools = value
	case jsonStringSpanEquals(key, "response_format"):
		f.responseFormat = value
	case jsonStringSpanEquals(key, "text"):
		f.text = value
	default:
		if constraint := constraintFieldFormat(key); constraint != ResponseFormatUnspecified &&
			!bytesEqual(value, "null") {
			f.constraint = constraint
		}
	}
}

func constraintFieldFormat(key jsonStringSpan) ResponseFormat {
	switch {
	case jsonStringSpanEquals(key, "json_schema"), jsonStringSpanEquals(key, "guided_json"):
		return ResponseFormatJSONSchema
	case jsonStringSpanEquals(key, "regex"), jsonStringSpanEquals(key, "guided_regex"):
		return ResponseFormatRegex
	case jsonStringSpanEquals(key, "ebnf"), jsonStringSpanEquals(key, "guided_grammar"):
		return ResponseFormatGrammar
	case jsonStringSpanEquals(key, "guided_choice"):
		return ResponseFormatChoice
	case jsonStringSpanEquals(key, "structural_tag"):
		return ResponseFormatStructuralTag
	case jsonStringSpanEquals(key, "structured_outputs"):
		return ResponseFormatOther
	default:
		return ResponseFormatUnspecified
	}
}

func (f *requestFeatureFields) classify(endpoint EndpointKind) RequestFeatures {
	features := RequestFeatures{
		Logprobs:     f.logprobsBucket(endpoint),
		ToolChoice:   toolChoiceFor(f.toolChoice),
		ToolsPresent: nonEmptyJSONArray(f.tools),
	}
	format := f.responseFormat
	if endpoint == EndpointResponses {
		format = nil
		if text, ok := jsonObjectMember(f.text, "format"); ok {
			format = text
		}
	}
	features.ResponseFormat = responseFormatFor(format)
	if f.constraint != ResponseFormatUnspecified &&
		(features.ResponseFormat == ResponseFormatUnspecified || features.ResponseFormat == ResponseFormatText) {
		features.ResponseFormat = f.constraint
	}
	return features
}

func (f *requestFeatureFields) logprobsBucket(endpoint EndpointKind) LogprobsBucket {
	if endpoint == EndpointCompletions {
		if absentJSONValue(f.logprobs) {
			return LogprobsNone
		}
		count, ok := parseNonnegativeInt64(f.logprobs)
		if !ok {
			return LogprobsInvalid
		}
		return logprobsCountBucket(count)
	}
	top := int64(0)
	if !absentJSONValue(f.topLogprobs) {
		parsed, ok := parseNonnegativeInt64(f.topLogprobs)
		if !ok {
			return LogprobsInvalid
		}
		top = parsed
	}
	requested := false
	if endpoint == EndpointResponses {
		requested = top > 0 || jsonArrayContainsString(f.include, responsesLogprobsInclude)
	} else if !absentJSONValue(f.logprobs) {
		enabled, ok := parseJSONBoolean(f.logprobs)
		if !ok {
			return LogprobsInvalid
		}
		requested = enabled
	}
	if !requested {
		return LogprobsNone
	}
	return logprobsCountBucket(top)
}

func logprobsCountBucket(count int64) LogprobsBucket {
	switch {
	case count == 0:
		return LogprobsZero
	case count <= 5:
		return LogprobsOneToFive
	case count <= 20:
		return LogprobsSixToTwenty
	default:
		return LogprobsOverTwenty
	}
}

func toolChoiceFor(value []byte) ToolChoice {
	if absentJSONValue(value) {
		return ToolChoiceUnspecified
	}
	switch value[0] {
	case '"':
		switch {
		case jsonStringValueEquals(value, "none"):
			return ToolChoiceNone
		case jsonStringValueEquals(value, "auto"):
			return ToolChoiceAuto
		case jsonStringValueEquals(value, "required"):
			return ToolChoiceRequired
		}
	case '{':
		kind, _ := jsonObjectMember(value, "type")
		if jsonStringValueEquals(kind, "function") || jsonStringValueEquals(kind, "custom") {
			return ToolChoiceNamed
		}
	}
	return ToolChoiceOther
}

func responseFormatFor(value []byte) ResponseFormat {
	if absentJSONValue(value) {
		return ResponseFormatUnspecified
	}
	kind, _ := jsonObjectMember(value, "type")
	switch {
	case jsonStringValueEquals(kind, "text"):
		return ResponseFormatText
	case jsonStringValueEquals(kind, "json_object"):
		return ResponseFormatJSONObject
	case jsonStringValueEquals(kind, "json_schema"):
		return ResponseFormatJSONSchema
	case jsonStringValueEquals(kind, "structural_tag"):
		return ResponseFormatStructuralTag
	default:
		return ResponseFormatOther
	}
}

func absentJSONValue(value []byte) bool {
	return len(value) == 0 || bytesEqual(value, "null")
}

func jsonStringValueEquals(value []byte, candidate string) bool {
	parser := jsonShapeParser{body: value}
	span, ok := parser.parseString()
	return ok && parser.index == len(value) && jsonStringSpanEquals(span, candidate)
}

func nonEmptyJSONArray(value []byte) bool {
	parser := jsonShapeParser{body: value}
	if !parser.consume('[') {
		return false
	}
	parser.skipSpace()
	return parser.index < len(value) && value[parser.index] != ']'
}

// jsonObjectMember returns the last value of key in an already scanned object.
func jsonObjectMember(value []byte, key string) ([]byte, bool) {
	parser := jsonShapeParser{body: value}
	if !parser.enter('{') {
		return nil, false
	}
	var member []byte
	found := false
	parser.skipSpace()
	if parser.consume('}') {
		return nil, false
	}
	for {
		name, ok := parser.parseString()
		if !ok {
			return nil, false
		}
		parser.skipSpace()
		if !parser.consume(':') {
			return nil, false
		}
		parser.skipSpace()
		start := parser.index
		if !parser.parseValue() {
			return nil, false
		}
		if jsonStringSpanEquals(name, key) {
			member = value[start:parser.index]
			found = true
		}
		parser.skipSpace()
		if parser.consume('}') {
			return member, found
		}
		if !parser.consume(',') {
			return nil, false
		}
		parser.skipSpace()
	}
}

func jsonArrayContainsString(value []byte, candidate string) bool {
	parser := jsonShapeParser{body: value}
	if !parser.enter('[') {
		return false
	}
	parser.skipSpace()
	if parser.consume(']') {
		return false
	}
	for {
		start := parser.index
		if !parser.parseValue() {
			return false
		}
		if jsonStringValueEquals(value[start:parser.index], candidate) {
			return true
		}
		parser.skipSpace()
		if !parser.consume(',') {
			return false
		}
		parser.skipSpace()
	}
}
