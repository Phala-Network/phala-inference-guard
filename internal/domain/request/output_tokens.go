package request

import (
	"math"
	"strconv"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/lexical"
)

const maximumJSONFieldScanDepth = 128

type JSONFields struct {
	OutputTokens                   int
	HasOutputTokens                bool
	PromptBatchSize                int64
	PromptStringBytes              int64
	MaximumPromptStringBytes       int64
	PromptApproximateTokens        int64
	MaximumPromptApproximateTokens int64
	ExplicitPromptTokens           int64
	MaximumExplicitPromptTokens    int64
	DecodeSequences                int64
	ShapeSupported                 bool
	StreamingPresent               bool
	StreamingKnown                 bool
	Streaming                      bool
}

type promptElementKind uint8

const (
	promptElementUnsupported promptElementKind = iota
	promptElementString
	promptElementToken
	promptElementTokenArray
)

type promptValueShape struct {
	batchSize                int64
	stringBytes              int64
	maximumStringBytes       int64
	approximateTokens        int64
	maximumApproximateTokens int64
	explicitTokens           int64
	maximumExplicitTokens    int64
	conservative             bool
	supported                bool
}

type promptElementShape struct {
	kind              promptElementKind
	explicitTokens    int64
	stringBytes       int64
	approximateTokens int64
	conservative      bool
}

type jsonStringSpan struct {
	raw     []byte
	quoted  []byte
	escaped bool
}

type jsonFieldParser struct {
	body  []byte
	index int
	depth int
}

func ParseJSONFields(body []byte, fields []string) (JSONFields, bool) {
	parser := jsonFieldParser{body: body}
	parser.skipSpace()
	if parser.index >= len(body) || body[parser.index] != '{' {
		return JSONFields{}, false
	}
	result, ok := parser.parseRootObject(fields)
	parser.skipSpace()
	return result, ok && parser.index == len(body)
}

func (p *jsonFieldParser) parseRootObject(fields []string) (JSONFields, bool) {
	result := JSONFields{PromptBatchSize: 1, DecodeSequences: 1, ShapeSupported: true}
	decodeCandidates := int64(1)
	if !p.enter('{') {
		return result, false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume('}') {
		return result, true
	}
	for {
		key, ok := p.parseString()
		if !ok {
			return JSONFields{}, false
		}
		p.skipSpace()
		if !p.consume(':') {
			return JSONFields{}, false
		}
		p.skipSpace()
		valueStart := p.index
		var promptShape promptValueShape
		isPrompt := jsonStringSpanEquals(key, "prompt")
		if isPrompt {
			promptShape, ok = p.parsePromptValue()
		} else {
			ok = p.parseValue()
		}
		if !ok {
			return JSONFields{}, false
		}
		value := p.body[valueStart:p.index]

		if jsonStringSpanInList(key, fields) {
			if outputTokens, valid := parseJSONOutputTokens(value); valid &&
				(!result.HasOutputTokens || outputTokens > result.OutputTokens) {
				result.OutputTokens = outputTokens
				result.HasOutputTokens = true
			}
		}
		if isPrompt {
			result.PromptBatchSize = promptShape.batchSize
			result.PromptStringBytes = promptShape.stringBytes
			result.MaximumPromptStringBytes = promptShape.maximumStringBytes
			result.PromptApproximateTokens = promptShape.approximateTokens
			result.MaximumPromptApproximateTokens = promptShape.maximumApproximateTokens
			result.ExplicitPromptTokens = promptShape.explicitTokens
			result.MaximumExplicitPromptTokens = promptShape.maximumExplicitTokens
			result.ShapeSupported = result.ShapeSupported && promptShape.supported
		}
		if jsonStringSpanEquals(key, "n") {
			candidate, valid := parseJSONOutputTokens(value)
			if !valid || candidate <= 0 {
				result.ShapeSupported = false
			} else if int64(candidate) > decodeCandidates {
				decodeCandidates = int64(candidate)
			}
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
			return finalizeJSONFields(result, decodeCandidates), true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == '}' {
				return JSONFields{}, false
			}
		default:
			return JSONFields{}, false
		}
	}
}

func finalizeJSONFields(result JSONFields, decodeCandidates int64) JSONFields {
	if !result.ShapeSupported || result.PromptBatchSize <= 0 || decodeCandidates <= 0 ||
		result.PromptBatchSize > math.MaxInt64/decodeCandidates {
		return JSONFields{
			OutputTokens:     result.OutputTokens,
			HasOutputTokens:  result.HasOutputTokens,
			PromptBatchSize:  1,
			ShapeSupported:   false,
			DecodeSequences:  0,
			StreamingPresent: result.StreamingPresent,
			StreamingKnown:   result.StreamingKnown,
			Streaming:        result.Streaming,
		}
	}
	result.DecodeSequences = result.PromptBatchSize * decodeCandidates
	return result
}

func parseJSONBoolean(value []byte) (bool, bool) {
	if len(value) == len("true") &&
		value[0] == 't' && value[1] == 'r' && value[2] == 'u' && value[3] == 'e' {
		return true, true
	}
	if len(value) == len("false") &&
		value[0] == 'f' && value[1] == 'a' && value[2] == 'l' && value[3] == 's' && value[4] == 'e' {
		return false, true
	}
	return false, false
}

func (p *jsonFieldParser) parseValue() bool {
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

func (p *jsonFieldParser) parseObject() bool {
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

func (p *jsonFieldParser) parseArray() bool {
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

func (p *jsonFieldParser) parsePromptValue() (promptValueShape, bool) {
	p.skipSpace()
	if p.index >= len(p.body) {
		return promptValueShape{}, false
	}
	if p.body[p.index] == '"' {
		value, ok := p.parseString()
		length, approximateTokens, conservative, approximateKnown := promptJSONStringEstimate(value)
		return promptValueShape{
			batchSize: 1, stringBytes: length, maximumStringBytes: length,
			approximateTokens: approximateTokens, maximumApproximateTokens: approximateTokens,
			conservative: conservative, supported: ok && approximateKnown,
		}, ok && approximateKnown
	}
	if p.body[p.index] != '[' {
		return promptValueShape{batchSize: 1}, p.parseValue()
	}
	if !p.enter('[') {
		return promptValueShape{}, false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume(']') {
		return promptValueShape{batchSize: 1}, true
	}

	shape := promptValueShape{supported: true}
	var expected promptElementKind
	var elements int64
	for {
		element, ok := p.parsePromptElement()
		if !ok {
			return promptValueShape{}, false
		}
		if elements == math.MaxInt64 || shape.explicitTokens > math.MaxInt64-element.explicitTokens ||
			shape.stringBytes > math.MaxInt64-element.stringBytes ||
			shape.approximateTokens > math.MaxInt64-element.approximateTokens {
			return promptValueShape{}, false
		}
		elements++
		shape.explicitTokens += element.explicitTokens
		shape.stringBytes += element.stringBytes
		shape.approximateTokens += element.approximateTokens
		shape.conservative = shape.conservative || element.conservative
		if element.explicitTokens > shape.maximumExplicitTokens {
			shape.maximumExplicitTokens = element.explicitTokens
		}
		if element.stringBytes > shape.maximumStringBytes {
			shape.maximumStringBytes = element.stringBytes
		}
		if element.approximateTokens > shape.maximumApproximateTokens {
			shape.maximumApproximateTokens = element.approximateTokens
		}
		if expected == promptElementUnsupported {
			expected = element.kind
		} else if element.kind != expected {
			shape.supported = false
		}
		if element.kind == promptElementUnsupported {
			shape.supported = false
		}
		p.skipSpace()
		switch {
		case p.consume(']'):
			switch expected {
			case promptElementString, promptElementTokenArray:
				shape.batchSize = elements
			case promptElementToken:
				shape.batchSize = 1
				shape.maximumExplicitTokens = shape.explicitTokens
			default:
				shape.batchSize = 1
				shape.supported = false
			}
			return shape, true
		case p.consume(','):
			p.skipSpace()
			if p.index >= len(p.body) || p.body[p.index] == ']' {
				return promptValueShape{}, false
			}
		default:
			return promptValueShape{}, false
		}
	}
}

func (p *jsonFieldParser) parsePromptElement() (promptElementShape, bool) {
	p.skipSpace()
	if p.index >= len(p.body) {
		return promptElementShape{}, false
	}
	switch p.body[p.index] {
	case '"':
		value, ok := p.parseString()
		stringBytes, approximateTokens, conservative, known := promptJSONStringEstimate(value)
		return promptElementShape{
			kind: promptElementString, stringBytes: stringBytes,
			approximateTokens: approximateTokens, conservative: conservative,
		}, ok && known
	case '[':
		tokens, supported, ok := p.parsePromptTokenArray()
		if !supported {
			return promptElementShape{explicitTokens: tokens}, ok
		}
		return promptElementShape{kind: promptElementTokenArray, explicitTokens: tokens}, ok
	default:
		start := p.index
		if !p.parseValue() {
			return promptElementShape{}, false
		}
		if _, valid := parseNonnegativeDecimal(p.body[start:p.index]); valid {
			return promptElementShape{kind: promptElementToken, explicitTokens: 1}, true
		}
		return promptElementShape{}, true
	}
}

func promptJSONStringEstimate(value jsonStringSpan) (stringBytes, tokens int64, conservative, valid bool) {
	if value.escaped {
		tokens, stringBytes, conservative, valid = lexical.EstimateDecodedJSONStringTokensWithRisk(value.raw)
		return stringBytes, tokens, conservative, valid
	}
	tokens, conservative, valid = lexical.EstimateJSONStringTokensWithRisk(value.raw)
	return int64(len(value.raw)), tokens, conservative, valid
}

func (p *jsonFieldParser) parsePromptTokenArray() (int64, bool, bool) {
	if !p.enter('[') {
		return 0, false, false
	}
	defer p.leave()
	p.skipSpace()
	if p.consume(']') {
		return 0, true, true
	}
	tokens := int64(0)
	supported := true
	for {
		start := p.index
		if !p.parseValue() {
			return 0, false, false
		}
		if _, valid := parseNonnegativeDecimal(p.body[start:p.index]); !valid {
			supported = false
		} else if tokens == math.MaxInt64 {
			return 0, false, false
		} else {
			tokens++
		}
		p.skipSpace()
		switch {
		case p.consume(']'):
			return tokens, supported, true
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

func (p *jsonFieldParser) parseString() (jsonStringSpan, bool) {
	if p.index >= len(p.body) || p.body[p.index] != '"' {
		return jsonStringSpan{}, false
	}
	quotedStart := p.index
	p.index++
	rawStart := p.index
	escaped := false
	for p.index < len(p.body) {
		value := p.body[p.index]
		switch {
		case value == '"':
			span := jsonStringSpan{
				raw:     p.body[rawStart:p.index],
				quoted:  p.body[quotedStart : p.index+1],
				escaped: escaped,
			}
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

func (p *jsonFieldParser) parseNumber() bool {
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

func (p *jsonFieldParser) enter(opening byte) bool {
	if p.depth >= maximumJSONFieldScanDepth || !p.consume(opening) {
		return false
	}
	p.depth++
	return true
}

func (p *jsonFieldParser) leave() {
	if p.depth > 0 {
		p.depth--
	}
}

func (p *jsonFieldParser) skipSpace() {
	for p.index < len(p.body) && isJSONSpace(p.body[p.index]) {
		p.index++
	}
}

func (p *jsonFieldParser) consume(value byte) bool {
	if p.index >= len(p.body) || p.body[p.index] != value {
		return false
	}
	p.index++
	return true
}

func (p *jsonFieldParser) consumeLiteral(value string) bool {
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

func jsonStringSpanInList(value jsonStringSpan, candidates []string) bool {
	for _, candidate := range candidates {
		if jsonStringSpanEquals(value, candidate) {
			return true
		}
	}
	return false
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
		value := raw[index]
		index++
		if value == '\\' {
			if index >= len(raw) {
				return false
			}
			escape := raw[index]
			index++
			switch escape {
			case '"', '\\', '/':
				value = escape
			case 'b':
				value = '\b'
			case 'f':
				value = '\f'
			case 'n':
				value = '\n'
			case 'r':
				value = '\r'
			case 't':
				value = '\t'
			case 'u':
				if len(raw)-index < 4 {
					return false
				}
				codeUnit := uint16(0)
				for offset := 0; offset < 4; offset++ {
					hex, ok := jsonHexValue(raw[index+offset])
					if !ok {
						return false
					}
					codeUnit = codeUnit<<4 | uint16(hex)
				}
				index += 4
				if codeUnit > 0x7f {
					return false
				}
				value = byte(codeUnit)
			default:
				return false
			}
		}
		if value >= 0x80 || candidateIndex >= len(candidate) || candidate[candidateIndex] != value {
			return false
		}
		candidateIndex++
	}
	return candidateIndex == len(candidate)
}

func parseJSONOutputTokens(value []byte) (int, bool) {
	if len(value) == 0 {
		return 0, false
	}
	if value[0] != '"' {
		return parseNonnegativeDecimal(value)
	}
	if len(value) < 2 || value[len(value)-1] != '"' {
		return 0, false
	}
	raw := value[1 : len(value)-1]
	for _, current := range raw {
		if current == '\\' || current >= 0x80 {
			decoded, err := strconv.Unquote(string(value))
			if err != nil {
				return 0, false
			}
			tokens, err := strconv.Atoi(strings.TrimSpace(decoded))
			return tokens, err == nil && tokens >= 0
		}
	}
	start, end := 0, len(raw)
	for start < end && isASCIIStringSpace(raw[start]) {
		start++
	}
	for end > start && isASCIIStringSpace(raw[end-1]) {
		end--
	}
	return parseNonnegativeDecimal(raw[start:end])
}

func parseNonnegativeDecimal(value []byte) (int, bool) {
	if len(value) == 0 {
		return 0, false
	}
	maximum := int(^uint(0) >> 1)
	result := 0
	for _, current := range value {
		if !isJSONDigit(current) {
			return 0, false
		}
		digit := int(current - '0')
		if result > (maximum-digit)/10 {
			return 0, false
		}
		result = result*10 + digit
	}
	return result, true
}

func isJSONSpace(value byte) bool {
	return value == ' ' || value == '\n' || value == '\r' || value == '\t'
}

func isASCIIStringSpace(value byte) bool {
	return value == ' ' || value == '\n' || value == '\r' || value == '\t' || value == '\v' || value == '\f'
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
