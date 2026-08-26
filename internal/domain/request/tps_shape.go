package request

import "math"

const maximumJSONShapeScanDepth = 128

type TPSRequestShape struct {
	BasePromptCount   int64
	DecodeSequences   int64
	Supported         bool
	UnsupportedReason string
	StreamingPresent  bool
	StreamingKnown    bool
	Streaming         bool
}

type jsonStringSpan struct {
	raw     []byte
	escaped bool
}

type jsonShapeParser struct {
	body               []byte
	index              int
	depth              int
	depthLimitExceeded bool
}

type fanoutFieldState struct {
	seen  bool
	valid bool
	null  bool
	value int64
}

type promptElementKind uint8

const (
	promptElementUnsupported promptElementKind = iota
	promptElementString
	promptElementToken
	promptElementTokenArray
)

func ParseTPSRequestShape(body []byte, endpoint EndpointKind) (TPSRequestShape, bool) {
	parser := jsonShapeParser{body: body}
	parser.skipSpace()
	if endpoint == EndpointUnknown || parser.index >= len(body) || body[parser.index] != '{' {
		return TPSRequestShape{}, false
	}
	shape, ok := parser.parseRootObject(endpoint)
	parser.skipSpace()
	if parser.depthLimitExceeded {
		return TPSRequestShape{UnsupportedReason: "shape_scan_limit"}, false
	}
	return shape, ok && parser.index == len(body)
}

func (p *jsonShapeParser) parseRootObject(endpoint EndpointKind) (TPSRequestShape, bool) {
	shape := TPSRequestShape{BasePromptCount: 1, DecodeSequences: 1, Supported: true}
	n := fanoutFieldState{valid: true, value: 1}
	bestOf := fanoutFieldState{valid: true, value: 1}
	promptSeen := false
	if !p.enter('{') {
		return TPSRequestShape{}, false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume('}') {
		return finalizeTPSRequestShape(shape, endpoint, n, bestOf), true
	}
	for {
		key, ok := p.parseString()
		if !ok {
			return TPSRequestShape{}, false
		}
		p.skipSpace()
		if !p.consume(':') {
			return TPSRequestShape{}, false
		}
		p.skipSpace()
		valueStart := p.index
		if endpoint == EndpointCompletions && jsonStringSpanEquals(key, "prompt") {
			var basePromptCount int64
			var supported bool
			basePromptCount, supported, ok = p.parseCompletionPrompt()
			if promptSeen || !supported {
				shape.Supported = false
			}
			promptSeen = true
			if supported {
				shape.BasePromptCount = basePromptCount
			}
		} else {
			ok = p.parseValue()
		}
		if !ok {
			return TPSRequestShape{}, false
		}
		value := p.body[valueStart:p.index]

		if endpointUsesDecodeFanout(endpoint) && jsonStringSpanEquals(key, "n") {
			shape.Supported = updatePositiveFanout(&n, value) && shape.Supported
		}
		if endpoint == EndpointCompletions && jsonStringSpanEquals(key, "best_of") {
			shape.Supported = updateOptionalPositiveFanout(&bestOf, value) && shape.Supported
		}
		if jsonStringSpanEquals(key, "stream") {
			streaming, valid := parseJSONBoolean(value)
			switch {
			case !shape.StreamingPresent:
				shape.StreamingPresent = true
				shape.StreamingKnown = valid
				shape.Streaming = streaming
			case !valid || !shape.StreamingKnown || shape.Streaming != streaming:
				shape.StreamingKnown = false
				shape.Streaming = false
			}
		}

		p.skipSpace()
		switch {
		case p.consume('}'):
			return finalizeTPSRequestShape(shape, endpoint, n, bestOf), true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == '}' {
				return TPSRequestShape{}, false
			}
		default:
			return TPSRequestShape{}, false
		}
	}
}

func finalizeTPSRequestShape(
	shape TPSRequestShape,
	endpoint EndpointKind,
	n fanoutFieldState,
	bestOf fanoutFieldState,
) TPSRequestShape {
	candidates := n.value
	if endpoint == EndpointCompletions && bestOf.value > candidates {
		candidates = bestOf.value
	}
	if !n.valid || !bestOf.valid || shape.BasePromptCount <= 0 || candidates <= 0 ||
		shape.BasePromptCount > math.MaxInt64/candidates {
		shape.Supported = false
	}
	if !shape.Supported {
		shape.DecodeSequences = 0
		shape.UnsupportedReason = "unsupported_request_shape"
		return shape
	}
	shape.DecodeSequences = shape.BasePromptCount * candidates
	return shape
}

func endpointUsesDecodeFanout(endpoint EndpointKind) bool {
	return endpoint == EndpointChatCompletions || endpoint == EndpointCompletions
}

func (p *jsonShapeParser) parseCompletionPrompt() (int64, bool, bool) {
	p.skipSpace()
	if p.index >= len(p.body) {
		return 0, false, false
	}
	if p.body[p.index] == '"' {
		_, ok := p.parseString()
		return 1, ok, ok
	}
	if p.body[p.index] != '[' {
		ok := p.parseValue()
		return 1, false, ok
	}
	if !p.enter('[') {
		return 0, false, false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume(']') {
		return 1, true, true
	}
	count := int64(0)
	kind := promptElementUnsupported
	supported := true
	for {
		elementKind, ok := p.parsePromptElement()
		if !ok {
			return 0, false, false
		}
		if count == math.MaxInt64 {
			return 0, false, true
		}
		count++
		if kind == promptElementUnsupported {
			kind = elementKind
		} else if kind != elementKind {
			supported = false
		}
		if elementKind == promptElementUnsupported {
			supported = false
		}
		p.skipSpace()
		switch {
		case p.consume(']'):
			switch kind {
			case promptElementString, promptElementTokenArray:
				return count, supported, true
			case promptElementToken:
				return 1, supported, true
			default:
				return 1, false, true
			}
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == ']' {
				return 0, false, false
			}
		default:
			return 0, false, false
		}
	}
}

func (p *jsonShapeParser) parsePromptElement() (promptElementKind, bool) {
	p.skipSpace()
	if p.index >= len(p.body) {
		return promptElementUnsupported, false
	}
	switch p.body[p.index] {
	case '"':
		_, ok := p.parseString()
		return promptElementString, ok
	case '[':
		supported, ok := p.parseTokenArray()
		if !supported {
			return promptElementUnsupported, ok
		}
		return promptElementTokenArray, ok
	default:
		start := p.index
		if !p.parseValue() {
			return promptElementUnsupported, false
		}
		if _, valid := parseNonnegativeInt64(p.body[start:p.index]); valid {
			return promptElementToken, true
		}
		return promptElementUnsupported, true
	}
}

func (p *jsonShapeParser) parseTokenArray() (bool, bool) {
	if !p.enter('[') {
		return false, false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume(']') {
		return true, true
	}
	supported := true
	for {
		start := p.index
		if !p.parseValue() {
			return false, false
		}
		if _, valid := parseNonnegativeInt64(p.body[start:p.index]); !valid {
			supported = false
		}
		p.skipSpace()
		switch {
		case p.consume(']'):
			return supported, true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == ']' {
				return false, false
			}
		default:
			return false, false
		}
	}
}

func updatePositiveFanout(state *fanoutFieldState, value []byte) bool {
	parsed, valid := parsePositiveInt64JSON(value)
	if state == nil || !valid {
		if state != nil {
			state.valid = false
		}
		return false
	}
	if state.seen && state.value != parsed {
		state.valid = false
		return false
	}
	state.seen = true
	state.valid = true
	state.value = parsed
	return true
}

func updateOptionalPositiveFanout(state *fanoutFieldState, value []byte) bool {
	if state == nil {
		return false
	}
	if bytesEqual(value, "null") {
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
	return updatePositiveFanout(state, value)
}

func parsePositiveInt64JSON(value []byte) (int64, bool) {
	if len(value) == 0 {
		return 0, false
	}
	if value[0] != '"' {
		result, valid := parseNonnegativeInt64(value)
		return result, valid && result > 0
	}
	if len(value) < 2 || value[len(value)-1] != '"' {
		return 0, false
	}
	raw := value[1 : len(value)-1]
	result := int64(0)
	digits := false
	trailingSpace := false
	for index := 0; index < len(raw); {
		current, next, ok := nextEscapedJSONStringASCII(raw, index)
		if !ok {
			return 0, false
		}
		index = next
		if isASCIIStringSpace(current) {
			trailingSpace = digits
			continue
		}
		if trailingSpace || !isJSONDigit(current) {
			return 0, false
		}
		digits = true
		digit := int64(current - '0')
		if result > (math.MaxInt64-digit)/10 {
			return 0, false
		}
		result = result*10 + digit
	}
	return result, digits && result > 0
}

func parseNonnegativeInt64(value []byte) (int64, bool) {
	if len(value) == 0 {
		return 0, false
	}
	result := int64(0)
	for _, current := range value {
		if !isJSONDigit(current) {
			return 0, false
		}
		digit := int64(current - '0')
		if result > (math.MaxInt64-digit)/10 {
			return 0, false
		}
		result = result*10 + digit
	}
	return result, true
}

func parseJSONBoolean(value []byte) (bool, bool) {
	if bytesEqual(value, "true") {
		return true, true
	}
	if bytesEqual(value, "false") {
		return false, true
	}
	return false, false
}

func (p *jsonShapeParser) parseValue() bool {
	p.skipSpace()
	if p.index >= len(p.body) {
		return false
	}
	switch p.body[p.index] {
	case '{':
		return p.parseObject()
	case '[':
		return p.parseArray()
	case '"':
		_, ok := p.parseString()
		return ok
	case 't':
		return p.consumeLiteral("true")
	case 'f':
		return p.consumeLiteral("false")
	case 'n':
		return p.consumeLiteral("null")
	default:
		return p.parseNumber()
	}
}

func (p *jsonShapeParser) parseObject() bool {
	if !p.enter('{') {
		return false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume('}') {
		return true
	}
	for {
		if _, ok := p.parseString(); !ok {
			return false
		}
		p.skipSpace()
		if !p.consume(':') || !p.parseValue() {
			return false
		}
		p.skipSpace()
		switch {
		case p.consume('}'):
			return true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == '}' {
				return false
			}
		default:
			return false
		}
	}
}

func (p *jsonShapeParser) parseArray() bool {
	if !p.enter('[') {
		return false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume(']') {
		return true
	}
	for {
		if !p.parseValue() {
			return false
		}
		p.skipSpace()
		switch {
		case p.consume(']'):
			return true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == ']' {
				return false
			}
		default:
			return false
		}
	}
}

func (p *jsonShapeParser) parseString() (jsonStringSpan, bool) {
	if p.index >= len(p.body) || p.body[p.index] != '"' {
		return jsonStringSpan{}, false
	}
	p.index++
	rawStart := p.index
	escaped := false
	for p.index < len(p.body) {
		value := p.body[p.index]
		switch {
		case value == '"':
			span := jsonStringSpan{raw: p.body[rawStart:p.index], escaped: escaped}
			p.index++
			return span, true
		case value == '\\':
			escaped = true
			p.index++
			if p.index >= len(p.body) {
				return jsonStringSpan{}, false
			}
			escape := p.body[p.index]
			if escape == 'u' {
				if p.index+4 >= len(p.body) {
					return jsonStringSpan{}, false
				}
				for offset := 1; offset <= 4; offset++ {
					if !isJSONHex(p.body[p.index+offset]) {
						return jsonStringSpan{}, false
					}
				}
				p.index += 5
				continue
			}
			if !isJSONSimpleEscape(escape) {
				return jsonStringSpan{}, false
			}
			p.index++
		case value < 0x20:
			return jsonStringSpan{}, false
		default:
			p.index++
		}
	}
	return jsonStringSpan{}, false
}

func (p *jsonShapeParser) parseNumber() bool {
	start := p.index
	if p.consume('-') && p.index >= len(p.body) {
		return false
	}
	if p.consume('0') {
		if p.index < len(p.body) && isJSONDigit(p.body[p.index]) {
			return false
		}
	} else {
		if p.index >= len(p.body) || p.body[p.index] < '1' || p.body[p.index] > '9' {
			return false
		}
		for p.index < len(p.body) && isJSONDigit(p.body[p.index]) {
			p.index++
		}
	}
	if p.consume('.') {
		if p.index >= len(p.body) || !isJSONDigit(p.body[p.index]) {
			return false
		}
		for p.index < len(p.body) && isJSONDigit(p.body[p.index]) {
			p.index++
		}
	}
	if p.index < len(p.body) && (p.body[p.index] == 'e' || p.body[p.index] == 'E') {
		p.index++
		if p.index < len(p.body) && (p.body[p.index] == '+' || p.body[p.index] == '-') {
			p.index++
		}
		if p.index >= len(p.body) || !isJSONDigit(p.body[p.index]) {
			return false
		}
		for p.index < len(p.body) && isJSONDigit(p.body[p.index]) {
			p.index++
		}
	}
	return p.index > start
}

func (p *jsonShapeParser) enter(opening byte) bool {
	if p.depth >= maximumJSONShapeScanDepth {
		p.depthLimitExceeded = true
		return false
	}
	if !p.consume(opening) {
		return false
	}
	p.depth++
	return true
}

func (p *jsonShapeParser) leave() {
	if p.depth > 0 {
		p.depth--
	}
}

func (p *jsonShapeParser) skipSpace() {
	for p.index < len(p.body) && isJSONSpace(p.body[p.index]) {
		p.index++
	}
}

func (p *jsonShapeParser) consume(value byte) bool {
	if p.index >= len(p.body) || p.body[p.index] != value {
		return false
	}
	p.index++
	return true
}

func (p *jsonShapeParser) consumeLiteral(value string) bool {
	if len(p.body)-p.index < len(value) {
		return false
	}
	for offset := range len(value) {
		if p.body[p.index+offset] != value[offset] {
			return false
		}
	}
	p.index += len(value)
	return true
}

func jsonStringSpanEquals(value jsonStringSpan, candidate string) bool {
	if value.escaped {
		return escapedJSONStringEqualsASCII(value.raw, candidate)
	}
	if len(value.raw) != len(candidate) {
		return false
	}
	for index := range value.raw {
		if value.raw[index] != candidate[index] {
			return false
		}
	}
	return true
}

func escapedJSONStringEqualsASCII(raw []byte, candidate string) bool {
	candidateIndex := 0
	for index := 0; index < len(raw); {
		value, next, ok := nextEscapedJSONStringASCII(raw, index)
		if !ok || candidateIndex >= len(candidate) || candidate[candidateIndex] != value {
			return false
		}
		index = next
		candidateIndex++
	}
	return candidateIndex == len(candidate)
}

func nextEscapedJSONStringASCII(raw []byte, index int) (byte, int, bool) {
	if index < 0 || index >= len(raw) {
		return 0, index, false
	}
	value := raw[index]
	index++
	if value != '\\' {
		return value, index, value < 0x80
	}
	if index >= len(raw) {
		return 0, index, false
	}
	escape := raw[index]
	index++
	switch escape {
	case '"', '\\', '/':
		return escape, index, true
	case 'b':
		return '\b', index, true
	case 'f':
		return '\f', index, true
	case 'n':
		return '\n', index, true
	case 'r':
		return '\r', index, true
	case 't':
		return '\t', index, true
	case 'u':
		if len(raw)-index < 4 {
			return 0, index, false
		}
		codeUnit := uint16(0)
		for offset := 0; offset < 4; offset++ {
			hex, ok := jsonHexValue(raw[index+offset])
			if !ok {
				return 0, index, false
			}
			codeUnit = codeUnit<<4 | uint16(hex)
		}
		if codeUnit > 0x7f {
			return 0, index, false
		}
		return byte(codeUnit), index + 4, true
	default:
		return 0, index, false
	}
}

func bytesEqual(value []byte, candidate string) bool {
	if len(value) != len(candidate) {
		return false
	}
	for index := range value {
		if value[index] != candidate[index] {
			return false
		}
	}
	return true
}

func isJSONSpace(value byte) bool {
	return value == ' ' || value == '\n' || value == '\r' || value == '\t'
}

func isASCIIStringSpace(value byte) bool {
	return isJSONSpace(value) || value == '\v' || value == '\f'
}

func isJSONDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isJSONHex(value byte) bool {
	return isJSONDigit(value) || value >= 'a' && value <= 'f' || value >= 'A' && value <= 'F'
}

func jsonHexValue(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func isJSONSimpleEscape(value byte) bool {
	switch value {
	case '"', '\\', '/', 'b', 'f', 'n', 'r', 't':
		return true
	default:
		return false
	}
}
