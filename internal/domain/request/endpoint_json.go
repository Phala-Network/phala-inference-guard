package request

import (
	"bytes"
	"math"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/lexical"
)

type EndpointInputFeatures struct {
	PromptBytes            int64
	TextBytes              int64
	ToolSchemaBytes        int64
	MessageCount           int64
	ToolCount              int64
	ModalityCount          int64
	ApproximateInputTokens int64
	ExplicitPromptTokens   int64
	Conservative           bool
}

type EndpointJSONFields struct {
	OutputTokens      int
	HasOutputTokens   bool
	BasePromptCount   int64
	DecodeSequences   int64
	ShapeSupported    bool
	UnsupportedReason string
	StreamingPresent  bool
	StreamingKnown    bool
	Streaming         bool
	Aggregate         EndpointInputFeatures
	MaximumSequence   EndpointInputFeatures
}

type endpointFieldState struct {
	seen  bool
	valid bool
	null  bool
	value int64
}

func ParseEndpointJSONFields(
	body []byte,
	outputTokenFields []string,
	endpoint EndpointKind,
) (EndpointJSONFields, bool) {
	parser := jsonFieldParser{body: body}
	parser.skipSpace()
	if endpoint == EndpointUnknown || parser.index >= len(body) || body[parser.index] != '{' {
		return EndpointJSONFields{}, false
	}
	result, ok := parser.parseEndpointRootObject(endpoint, outputTokenFields)
	parser.skipSpace()
	return result, ok && parser.index == len(body)
}

func (p *jsonFieldParser) parseEndpointRootObject(
	endpoint EndpointKind,
	outputTokenFields []string,
) (EndpointJSONFields, bool) {
	result := EndpointJSONFields{BasePromptCount: 1, DecodeSequences: 1, ShapeSupported: true}
	decodeCandidates := endpointFieldState{valid: true, value: 1}
	bestOf := endpointFieldState{valid: true, value: 1}
	var promptShape promptValueShape
	promptShape.supported = true
	var suffixFeatures EndpointInputFeatures
	var promptSeen, suffixSeen, messagesSeen, inputSeen, instructionsSeen, toolsSeen bool
	if !p.enter('{') {
		return result, false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume('}') {
		return finalizeEndpointJSONFields(result, endpoint, promptShape, suffixFeatures, decodeCandidates, bestOf), true
	}
	for {
		key, ok := p.parseString()
		if !ok {
			return EndpointJSONFields{}, false
		}
		p.skipSpace()
		if !p.consume(':') {
			return EndpointJSONFields{}, false
		}
		p.skipSpace()
		valueStart := p.index

		switch {
		case endpoint == EndpointChatCompletions && jsonStringSpanEquals(key, "messages"):
			if messagesSeen {
				result.ShapeSupported = false
			}
			messagesSeen = true
			var features EndpointInputFeatures
			features, ok = p.parseSemanticValue()
			result.ShapeSupported = result.ShapeSupported && mergeEndpointFeatures(&result.Aggregate, features)
		case endpoint == EndpointCompletions && jsonStringSpanEquals(key, "prompt"):
			if promptSeen {
				result.ShapeSupported = false
			}
			promptSeen = true
			promptShape, ok = p.parsePromptValue()
			result.ShapeSupported = result.ShapeSupported && promptShape.supported
		case endpoint == EndpointCompletions && jsonStringSpanEquals(key, "suffix"):
			if suffixSeen {
				result.ShapeSupported = false
			}
			suffixSeen = true
			suffixFeatures, ok = p.parseOptionalSemanticString()
		case endpoint == EndpointResponses && jsonStringSpanEquals(key, "instructions"):
			if instructionsSeen {
				result.ShapeSupported = false
			}
			instructionsSeen = true
			var features EndpointInputFeatures
			features, ok = p.parseOptionalSemanticString()
			if ok && (features.TextBytes > 0 || features.PromptBytes > 0) {
				features.MessageCount = 1
			}
			result.ShapeSupported = result.ShapeSupported && mergeEndpointFeatures(&result.Aggregate, features)
		case endpoint == EndpointResponses && jsonStringSpanEquals(key, "input"):
			if inputSeen {
				result.ShapeSupported = false
			}
			inputSeen = true
			var features EndpointInputFeatures
			features, ok = p.parseSemanticValue()
			if ok && features.MessageCount == 0 && (features.TextBytes > 0 || features.PromptBytes > 0) {
				features.MessageCount = 1
			}
			result.ShapeSupported = result.ShapeSupported && mergeEndpointFeatures(&result.Aggregate, features)
		case (endpoint == EndpointChatCompletions || endpoint == EndpointResponses) && jsonStringSpanEquals(key, "tools"):
			if toolsSeen {
				result.ShapeSupported = false
			}
			toolsSeen = true
			var schemaBytes, toolCount int64
			var supported bool
			schemaBytes, toolCount, supported, ok = p.parseToolSchemaValue()
			result.ShapeSupported = result.ShapeSupported && supported &&
				addEndpointFeatureValue(&result.Aggregate.ToolSchemaBytes, schemaBytes) &&
				addEndpointFeatureValue(&result.Aggregate.ToolCount, toolCount)
		case endpoint == EndpointResponses && endpointExternalContextKey(key):
			var external bool
			external, ok = p.parseExternalContextValue()
			if external {
				result.UnsupportedReason = "body_external_context"
				result.ShapeSupported = false
			}
		default:
			ok = p.parseValue()
		}
		if !ok {
			return EndpointJSONFields{}, false
		}
		value := p.body[valueStart:p.index]

		if endpointOutputTokenField(endpoint, key) && jsonStringSpanInList(key, outputTokenFields) {
			if outputTokens, valid := parseJSONOutputTokens(value); valid &&
				(!result.HasOutputTokens || outputTokens > result.OutputTokens) {
				result.OutputTokens = outputTokens
				result.HasOutputTokens = true
			}
		}
		if endpointUsesDecodeFanout(endpoint) && jsonStringSpanEquals(key, "n") {
			result.ShapeSupported = result.ShapeSupported && updatePositiveEndpointField(&decodeCandidates, value)
		}
		if endpoint == EndpointCompletions && jsonStringSpanEquals(key, "best_of") {
			result.ShapeSupported = result.ShapeSupported && updateOptionalPositiveEndpointField(&bestOf, value)
		}
		if jsonStringSpanEquals(key, "stream") {
			streaming, valid := parseJSONBoolean(value)
			switch {
			case !result.StreamingPresent:
				result.StreamingPresent = true
				result.StreamingKnown = valid
				result.Streaming = streaming
			case !valid || !result.StreamingKnown || result.Streaming != streaming:
				result.StreamingKnown = false
				result.Streaming = false
			}
		}

		p.skipSpace()
		switch {
		case p.consume('}'):
			return finalizeEndpointJSONFields(result, endpoint, promptShape, suffixFeatures, decodeCandidates, bestOf), true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == '}' {
				return EndpointJSONFields{}, false
			}
		default:
			return EndpointJSONFields{}, false
		}
	}
}

func finalizeEndpointJSONFields(
	result EndpointJSONFields,
	endpoint EndpointKind,
	promptShape promptValueShape,
	suffix EndpointInputFeatures,
	decodeCandidates endpointFieldState,
	bestOf endpointFieldState,
) EndpointJSONFields {
	if endpoint == EndpointCompletions {
		if promptShape.batchSize <= 0 {
			promptShape.batchSize = 1
		}
		result.BasePromptCount = promptShape.batchSize
		result.Aggregate.TextBytes = promptShape.stringBytes
		result.Aggregate.PromptBytes = promptShape.stringBytes
		result.Aggregate.ApproximateInputTokens = promptShape.approximateTokens
		result.Aggregate.ExplicitPromptTokens = promptShape.explicitTokens
		result.MaximumSequence.TextBytes = promptShape.maximumStringBytes
		result.MaximumSequence.PromptBytes = promptShape.maximumStringBytes
		result.MaximumSequence.ApproximateInputTokens = promptShape.maximumApproximateTokens
		result.MaximumSequence.ExplicitPromptTokens = promptShape.maximumExplicitTokens
		if scaled, ok := scaleEndpointFeatures(suffix, result.BasePromptCount); ok {
			result.ShapeSupported = result.ShapeSupported && mergeEndpointFeatures(&result.Aggregate, scaled)
		} else {
			result.ShapeSupported = false
		}
		result.ShapeSupported = result.ShapeSupported && mergeEndpointFeatures(&result.MaximumSequence, suffix)
	} else {
		result.MaximumSequence = result.Aggregate
	}
	if result.UnsupportedReason == "" && !result.ShapeSupported {
		result.UnsupportedReason = "unsupported_request_shape"
	}
	candidates := decodeCandidates.value
	if endpoint == EndpointCompletions && bestOf.value > candidates {
		candidates = bestOf.value
	}
	if !decodeCandidates.valid || !bestOf.valid || result.BasePromptCount <= 0 || candidates <= 0 ||
		result.BasePromptCount > math.MaxInt64/candidates {
		result.ShapeSupported = false
		if result.UnsupportedReason == "" {
			result.UnsupportedReason = "unsupported_request_shape"
		}
		result.DecodeSequences = 0
		return result
	}
	result.DecodeSequences = result.BasePromptCount * candidates
	return result
}

func (p *jsonFieldParser) parseOptionalSemanticString() (EndpointInputFeatures, bool) {
	p.skipSpace()
	if p.index < len(p.body) && p.body[p.index] == 'n' {
		return EndpointInputFeatures{}, p.consumeLiteral("null")
	}
	if p.index >= len(p.body) || p.body[p.index] != '"' {
		return EndpointInputFeatures{}, p.parseValue()
	}
	return p.parseSemanticValue()
}

func (p *jsonFieldParser) parseSemanticValue() (EndpointInputFeatures, bool) {
	p.skipSpace()
	if p.index >= len(p.body) {
		return EndpointInputFeatures{}, false
	}
	switch p.body[p.index] {
	case '"':
		value, ok := p.parseString()
		if !ok {
			return EndpointInputFeatures{}, false
		}
		return endpointFeaturesForString(value)
	case '{':
		return p.parseSemanticObject()
	case '[':
		return p.parseSemanticArray()
	case 'n':
		return EndpointInputFeatures{}, p.consumeLiteral("null")
	default:
		start := p.index
		if !p.parseValue() {
			return EndpointInputFeatures{}, false
		}
		length := int64(p.index - start)
		return EndpointInputFeatures{
			PromptBytes:            length,
			ApproximateInputTokens: int64((length + 3) / 4),
			Conservative:           true,
		}, true
	}
}

func (p *jsonFieldParser) parseSemanticArray() (EndpointInputFeatures, bool) {
	features := EndpointInputFeatures{PromptBytes: 2}
	if !p.enter('[') {
		return EndpointInputFeatures{}, false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume(']') {
		return features, true
	}
	elements := int64(0)
	for {
		value, ok := p.parseSemanticValue()
		if !ok || !mergeEndpointFeatures(&features, value) {
			return EndpointInputFeatures{}, false
		}
		if elements == math.MaxInt64 {
			return EndpointInputFeatures{}, false
		}
		elements++
		p.skipSpace()
		switch {
		case p.consume(']'):
			if elements > 1 && !addEndpointFeatureValue(&features.PromptBytes, elements-1) {
				return EndpointInputFeatures{}, false
			}
			return features, true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == ']' {
				return EndpointInputFeatures{}, false
			}
		default:
			return EndpointInputFeatures{}, false
		}
	}
}

func (p *jsonFieldParser) parseSemanticObject() (EndpointInputFeatures, bool) {
	features := EndpointInputFeatures{PromptBytes: 2}
	if !p.enter('{') {
		return EndpointInputFeatures{}, false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume('}') {
		return features, true
	}
	fields := int64(0)
	messageObject := false
	modalityObject := false
	for {
		key, ok := p.parseString()
		if !ok {
			return EndpointInputFeatures{}, false
		}
		p.skipSpace()
		if !p.consume(':') {
			return EndpointInputFeatures{}, false
		}
		p.skipSpace()
		switch {
		case jsonStringSpanEquals(key, "role"):
			messageObject = true
			ok = p.parseValue()
		case jsonStringSpanEquals(key, "type"):
			if p.index < len(p.body) && p.body[p.index] == '"' {
				var value jsonStringSpan
				value, ok = p.parseString()
				if ok {
					messageObject = messageObject || jsonStringSpanEquals(value, "message")
					modalityObject = endpointModalityType(value)
				}
			} else {
				ok = p.parseValue()
			}
		case endpointModalityPayloadKey(key):
			modalityObject = true
			ok = p.parseValue()
		case jsonStringSpanEquals(key, "detail"):
			ok = p.parseValue()
		default:
			var value EndpointInputFeatures
			value, ok = p.parseSemanticValue()
			if ok {
				ok = addEndpointFeatureValue(&features.PromptBytes, int64(len(key.quoted)+1)) &&
					mergeEndpointFeatures(&features, value)
			}
		}
		if !ok {
			return EndpointInputFeatures{}, false
		}
		if fields == math.MaxInt64 {
			return EndpointInputFeatures{}, false
		}
		fields++
		p.skipSpace()
		switch {
		case p.consume('}'):
			if fields > 1 && !addEndpointFeatureValue(&features.PromptBytes, fields-1) {
				return EndpointInputFeatures{}, false
			}
			if messageObject {
				features.MessageCount++
			}
			if modalityObject {
				features.ModalityCount++
				features.Conservative = true
			}
			return features, true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == '}' {
				return EndpointInputFeatures{}, false
			}
		default:
			return EndpointInputFeatures{}, false
		}
	}
}

func (p *jsonFieldParser) parseToolSchemaValue() (int64, int64, bool, bool) {
	p.skipSpace()
	start := p.index
	if p.index < len(p.body) && p.body[p.index] == 'n' {
		ok := p.consumeLiteral("null")
		return 0, 0, true, ok
	}
	if !p.enter('[') {
		ok := p.parseValue()
		return int64(p.index - start), 0, false, ok
	}
	defer p.leave()
	p.skipSpace()
	if p.consume(']') {
		return int64(p.index - start), 0, true, true
	}
	count := int64(0)
	for {
		if !p.parseValue() || count == math.MaxInt64 {
			return 0, 0, false, false
		}
		count++
		p.skipSpace()
		switch {
		case p.consume(']'):
			return int64(p.index - start), count, true, true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == ']' {
				return 0, 0, false, false
			}
		default:
			return 0, 0, false, false
		}
	}
}

func (p *jsonFieldParser) parseExternalContextValue() (bool, bool) {
	start := p.index
	if !p.parseValue() {
		return false, false
	}
	value := bytes.TrimSpace(p.body[start:p.index])
	return !bytes.Equal(value, []byte("null")) && !bytes.Equal(value, []byte(`""`)) &&
		!bytes.Equal(value, []byte("[]")), true
}

func endpointFeaturesForString(value jsonStringSpan) (EndpointInputFeatures, bool) {
	decodedBytes := int64(len(value.raw))
	var tokens int64
	var conservative, known bool
	if value.escaped {
		tokens, decodedBytes, conservative, known = lexical.EstimateDecodedJSONStringTokensWithRisk(value.raw)
	} else {
		tokens, conservative, known = lexical.EstimateJSONStringTokensWithRisk(value.raw)
	}
	if !known || decodedBytes > math.MaxInt64-2 {
		return EndpointInputFeatures{}, false
	}
	return EndpointInputFeatures{
		PromptBytes:            decodedBytes + 2,
		TextBytes:              decodedBytes,
		ApproximateInputTokens: tokens,
		Conservative:           conservative,
	}, true
}

func updatePositiveEndpointField(state *endpointFieldState, value []byte) bool {
	parsed, valid := parseJSONOutputTokens(value)
	if state == nil || !valid || parsed <= 0 {
		if state != nil {
			state.valid = false
		}
		return false
	}
	candidate := int64(parsed)
	if state.seen && state.value != candidate {
		state.valid = false
		return false
	}
	state.seen = true
	state.valid = true
	state.value = candidate
	return true
}

func updateOptionalPositiveEndpointField(state *endpointFieldState, value []byte) bool {
	if state == nil {
		return false
	}
	if bytes.Equal(value, []byte("null")) {
		if state.seen && !state.null {
			state.valid = false
			return false
		}
		state.seen = true
		state.valid = true
		state.null = true
		state.value = 1
		return true
	}
	if state.seen && state.null {
		state.valid = false
		return false
	}
	return updatePositiveEndpointField(state, value)
}

func endpointExternalContextKey(key jsonStringSpan) bool {
	return jsonStringSpanEquals(key, "previous_response_id") ||
		jsonStringSpanEquals(key, "previous_input_messages") ||
		jsonStringSpanEquals(key, "prompt") ||
		jsonStringSpanEquals(key, "conversation")
}

func endpointUsesDecodeFanout(endpoint EndpointKind) bool {
	return endpoint == EndpointChatCompletions || endpoint == EndpointCompletions
}

func endpointOutputTokenField(endpoint EndpointKind, key jsonStringSpan) bool {
	switch endpoint {
	case EndpointChatCompletions:
		return jsonStringSpanEquals(key, "max_tokens") ||
			jsonStringSpanEquals(key, "max_completion_tokens")
	case EndpointCompletions:
		return jsonStringSpanEquals(key, "max_tokens")
	case EndpointResponses:
		return jsonStringSpanEquals(key, "max_output_tokens")
	default:
		return false
	}
}

func endpointModalityPayloadKey(key jsonStringSpan) bool {
	return jsonStringSpanEquals(key, "image_url") ||
		jsonStringSpanEquals(key, "input_image") ||
		jsonStringSpanEquals(key, "audio_url") ||
		jsonStringSpanEquals(key, "input_audio") ||
		jsonStringSpanEquals(key, "video_url") ||
		jsonStringSpanEquals(key, "input_video") ||
		jsonStringSpanEquals(key, "file_id")
}

func endpointModalityType(value jsonStringSpan) bool {
	return jsonStringSpanEquals(value, "image_url") ||
		jsonStringSpanEquals(value, "input_image") ||
		jsonStringSpanEquals(value, "input_audio") ||
		jsonStringSpanEquals(value, "input_video") ||
		jsonStringSpanEquals(value, "input_file")
}

func mergeEndpointFeatures(target *EndpointInputFeatures, value EndpointInputFeatures) bool {
	if target == nil ||
		!addEndpointFeatureValue(&target.PromptBytes, value.PromptBytes) ||
		!addEndpointFeatureValue(&target.TextBytes, value.TextBytes) ||
		!addEndpointFeatureValue(&target.ToolSchemaBytes, value.ToolSchemaBytes) ||
		!addEndpointFeatureValue(&target.MessageCount, value.MessageCount) ||
		!addEndpointFeatureValue(&target.ToolCount, value.ToolCount) ||
		!addEndpointFeatureValue(&target.ModalityCount, value.ModalityCount) ||
		!addEndpointFeatureValue(&target.ApproximateInputTokens, value.ApproximateInputTokens) ||
		!addEndpointFeatureValue(&target.ExplicitPromptTokens, value.ExplicitPromptTokens) {
		return false
	}
	target.Conservative = target.Conservative || value.Conservative
	return true
}

func scaleEndpointFeatures(value EndpointInputFeatures, multiplier int64) (EndpointInputFeatures, bool) {
	if multiplier <= 0 {
		return EndpointInputFeatures{}, false
	}
	result := value
	fields := []*int64{
		&result.PromptBytes,
		&result.TextBytes,
		&result.ToolSchemaBytes,
		&result.MessageCount,
		&result.ToolCount,
		&result.ModalityCount,
		&result.ApproximateInputTokens,
		&result.ExplicitPromptTokens,
	}
	for _, field := range fields {
		if *field < 0 || *field > math.MaxInt64/multiplier {
			return EndpointInputFeatures{}, false
		}
		*field *= multiplier
	}
	return result, true
}

func addEndpointFeatureValue(target *int64, value int64) bool {
	if target == nil || value < 0 || *target > math.MaxInt64-value {
		return false
	}
	*target += value
	return true
}
