package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"math"
	"mime"
	"strings"
	"time"
)

const (
	maximumCompletionUsageJSONBytes = 4 * 1024 * 1024
	maximumCompletionUsageSSEBytes  = 64 * 1024
	completionUsageLookaheadBytes   = 32 * 1024
)

type CompletionUsage struct {
	PromptTokens     int64
	CompletionTokens int64
	MeanITL          time.Duration
	GenerationTime   time.Duration
	ObservedAt       time.Time
}

type CompletionUsageEvidenceOutcome uint8

const (
	CompletionUsageUnavailable CompletionUsageEvidenceOutcome = iota
	CompletionUsageAvailable
	CompletionUsageMalformed
)

type CompletionUsageEvidence struct {
	Outcome CompletionUsageEvidenceOutcome
	Usage   CompletionUsage
}

type CompletionUsageFormat uint8

const (
	CompletionUsageFormatCompletions CompletionUsageFormat = iota
	CompletionUsageFormatResponses
)

type completionUsageCallback func(CompletionUsage)
type completionUsageTerminalCallback func()
type completionUsageEvidenceCallback func(CompletionUsageEvidence)

type completionUsageBody struct {
	source            io.ReadCloser
	lookahead         *bufio.Reader
	observer          *CompletionUsageObserver
	remaining         int64
	hasExpectedLength bool
	expectedLengthBad bool
}

func ObserveCompletionUsageBody(source io.ReadCloser, streaming bool, callback func(CompletionUsage)) io.ReadCloser {
	return observeCompletionUsageBody(source, streaming, CompletionUsageFormatCompletions, -1, callback, nil, nil)
}

func ObserveCompletionUsageBodyWithTerminal(source io.ReadCloser, streaming bool, callback func(CompletionUsage), onTerminal func()) io.ReadCloser {
	return observeCompletionUsageBody(source, streaming, CompletionUsageFormatCompletions, -1, callback, onTerminal, nil)
}

func ObserveCompletionUsageBodyWithTerminalLength(source io.ReadCloser, streaming bool, contentLength int64, callback func(CompletionUsage), onTerminal func()) io.ReadCloser {
	return observeCompletionUsageBody(source, streaming, CompletionUsageFormatCompletions, contentLength, callback, onTerminal, nil)
}

func ObserveCompletionUsageEvidenceBody(
	source io.ReadCloser,
	streaming bool,
	callback func(CompletionUsageEvidence),
) io.ReadCloser {
	return ObserveCompletionUsageEvidenceBodyForFormat(
		source,
		streaming,
		CompletionUsageFormatCompletions,
		callback,
	)
}

func ObserveCompletionUsageEvidenceBodyForFormat(
	source io.ReadCloser,
	streaming bool,
	format CompletionUsageFormat,
	callback func(CompletionUsageEvidence),
) io.ReadCloser {
	return observeCompletionUsageBody(source, streaming, format, -1, nil, nil, callback)
}

func observeCompletionUsageBody(
	source io.ReadCloser,
	streaming bool,
	format CompletionUsageFormat,
	contentLength int64,
	callback func(CompletionUsage),
	onTerminal func(),
	onEvidence func(CompletionUsageEvidence),
) io.ReadCloser {
	if source == nil || (callback == nil && onTerminal == nil && onEvidence == nil) {
		return source
	}
	body := &completionUsageBody{
		source:    source,
		observer:  newCompletionUsageObserver(streaming, format, callback, onTerminal, onEvidence),
		remaining: -1,
	}
	if !streaming && onTerminal != nil && contentLength >= 0 {
		body.remaining = contentLength
		body.hasExpectedLength = true
	} else if !streaming && onTerminal != nil {
		body.lookahead = bufio.NewReaderSize(source, completionUsageLookaheadBytes)
	}
	return body
}

func NewCompletionUsageObserver(streaming bool, callback func(CompletionUsage)) *CompletionUsageObserver {
	return NewCompletionUsageObserverWithTerminal(streaming, callback, nil)
}

func NewCompletionUsageObserverWithTerminal(streaming bool, callback func(CompletionUsage), onTerminal func()) *CompletionUsageObserver {
	return newCompletionUsageObserver(streaming, CompletionUsageFormatCompletions, callback, onTerminal, nil)
}

func newCompletionUsageObserver(
	streaming bool,
	format CompletionUsageFormat,
	callback func(CompletionUsage),
	onTerminal func(),
	onEvidence func(CompletionUsageEvidence),
) *CompletionUsageObserver {
	if callback == nil && onTerminal == nil && onEvidence == nil {
		return nil
	}
	return &CompletionUsageObserver{
		streaming: streaming, format: format, callback: callback, onTerminal: onTerminal, onEvidence: onEvidence,
	}
}

func (b *completionUsageBody) Read(buffer []byte) (int, error) {
	var n int
	var err error
	if b.lookahead != nil {
		n, err = b.lookahead.Read(buffer)
	} else {
		n, err = b.source.Read(buffer)
	}
	if n > 0 {
		b.observer.Observe(buffer[:n])
	}
	if b.hasExpectedLength {
		b.observeExpectedLength(n, err)
	} else if err == io.EOF {
		b.observer.Finish()
	} else if err == nil && n > 0 && b.lookahead != nil {
		if _, peekErr := b.lookahead.Peek(1); peekErr == io.EOF {
			b.observer.Finish()
		}
	}
	return n, err
}

func (b *completionUsageBody) observeExpectedLength(n int, err error) {
	if b == nil || b.expectedLengthBad || b.observer == nil {
		return
	}
	if int64(n) > b.remaining {
		b.expectedLengthBad = true
		return
	}
	b.remaining -= int64(n)
	if b.remaining == 0 && (err == nil || err == io.EOF) {
		b.observer.Finish()
		return
	}
	if err == io.EOF {
		b.expectedLengthBad = true
	}
}

func (b *completionUsageBody) Close() error {
	return b.source.Close()
}

type CompletionUsageObserver struct {
	streaming        bool
	format           CompletionUsageFormat
	callback         completionUsageCallback
	onTerminal       completionUsageTerminalCallback
	onEvidence       completionUsageEvidenceCallback
	finished         bool
	terminalSeen     bool
	terminalNotified bool
	evidenceNotified bool

	candidate        CompletionUsage
	candidateValid   bool
	candidateInvalid bool

	jsonBody    []byte
	jsonLimited bool

	line         []byte
	eventData    []byte
	eventLimited bool
	eventHasData bool
}

func (o *CompletionUsageObserver) Observe(chunk []byte) {
	if o == nil || o.finished || o.terminalSeen || len(chunk) == 0 {
		return
	}
	if !o.streaming {
		o.observeJSON(chunk)
		return
	}
	for _, value := range chunk {
		if value == '\n' {
			o.processSSELine()
			if o.finished || o.terminalSeen {
				return
			}
			continue
		}
		if len(o.line) < maximumCompletionUsageSSEBytes {
			o.line = append(o.line, value)
		} else {
			o.eventLimited = true
		}
	}
}

func (o *CompletionUsageObserver) observeJSON(chunk []byte) {
	if o.jsonLimited {
		return
	}
	remaining := maximumCompletionUsageJSONBytes - len(o.jsonBody)
	if len(chunk) > remaining {
		o.jsonBody = nil
		o.jsonLimited = true
		return
	}
	o.jsonBody = append(o.jsonBody, chunk...)
}

func (o *CompletionUsageObserver) processSSELine() {
	line := bytes.TrimSuffix(o.line, []byte{'\r'})
	o.line = o.line[:0]
	if len(line) == 0 {
		o.finishSSEEvent()
		return
	}
	if o.eventLimited || line[0] == ':' || !bytes.HasPrefix(line, []byte("data:")) {
		return
	}
	data := bytes.TrimPrefix(line, []byte("data:"))
	data = bytes.TrimPrefix(data, []byte{' '})
	additional := len(data)
	if o.eventHasData {
		additional++
	}
	if len(o.eventData)+additional > maximumCompletionUsageSSEBytes {
		o.eventData = nil
		o.eventLimited = true
		return
	}
	if o.eventHasData {
		o.eventData = append(o.eventData, '\n')
	}
	o.eventData = append(o.eventData, data...)
	o.eventHasData = true
}

func (o *CompletionUsageObserver) finishSSEEvent() {
	terminal := false
	if !o.eventLimited && o.eventHasData {
		data := bytes.TrimSpace(o.eventData)
		terminal = bytes.Equal(data, []byte("[DONE]"))
		if len(data) > 0 && !terminal && bytes.Contains(data, []byte(`"usage"`)) {
			usage, outcome := decodeCompletionUsageEvidence(data, true, o.format)
			switch outcome {
			case CompletionUsageAvailable:
				usage.ObservedAt = time.Now()
				if o.candidateValid {
					o.candidateInvalid = true
				} else {
					o.candidate = usage
					o.candidateValid = true
				}
			case CompletionUsageMalformed:
				o.candidateInvalid = true
			}
		}
	} else if o.eventLimited {
		o.candidateInvalid = true
	}
	o.eventData = o.eventData[:0]
	o.eventLimited = false
	o.eventHasData = false
	if terminal {
		o.terminalSeen = true
		o.notifyTerminal()
	}
}

func (o *CompletionUsageObserver) Finish() {
	if o == nil || o.finished {
		return
	}
	if o.streaming {
		if !o.terminalSeen && len(o.line) > 0 {
			o.processSSELine()
			if o.finished || o.terminalSeen {
				o.finishStreaming()
				return
			}
		}
		if !o.terminalSeen && (o.eventHasData || o.eventLimited) {
			o.finishSSEEvent()
			if o.finished || o.terminalSeen {
				o.finishStreaming()
				return
			}
		}
		o.finishStreaming()
		return
	}
	o.finished = true
	defer o.notifyTerminal()
	if o.jsonLimited {
		o.emitEvidence(CompletionUsageEvidence{Outcome: CompletionUsageMalformed})
		return
	}
	if len(o.jsonBody) == 0 {
		o.emitEvidence(CompletionUsageEvidence{Outcome: CompletionUsageUnavailable})
		return
	}
	usage, outcome := decodeCompletionUsageEvidence(o.jsonBody, false, o.format)
	if outcome == CompletionUsageAvailable {
		usage.ObservedAt = time.Now()
		o.emit(usage)
	}
	o.emitEvidence(CompletionUsageEvidence{Outcome: outcome, Usage: usage})
}

func (o *CompletionUsageObserver) TerminalSeen() bool {
	return o != nil && o.terminalSeen
}

func (o *CompletionUsageObserver) finishStreaming() {
	if o == nil || o.finished {
		return
	}
	o.finished = true
	if o.candidateValid && !o.candidateInvalid {
		o.emit(o.candidate)
		o.emitEvidence(CompletionUsageEvidence{Outcome: CompletionUsageAvailable, Usage: o.candidate})
	} else if o.candidateInvalid {
		o.emitEvidence(CompletionUsageEvidence{Outcome: CompletionUsageMalformed})
	} else {
		o.emitEvidence(CompletionUsageEvidence{Outcome: CompletionUsageUnavailable})
	}
	o.notifyTerminal()
}

func (o *CompletionUsageObserver) emit(usage CompletionUsage) {
	if o.callback == nil {
		return
	}
	o.callback(usage)
}

func (o *CompletionUsageObserver) notifyTerminal() {
	if o == nil || o.terminalNotified {
		return
	}
	o.terminalNotified = true
	if o.onTerminal != nil {
		o.onTerminal()
	}
}

func (o *CompletionUsageObserver) emitEvidence(evidence CompletionUsageEvidence) {
	if o == nil || o.evidenceNotified {
		return
	}
	o.evidenceNotified = true
	if o.onEvidence != nil {
		o.onEvidence(evidence)
	}
}

type completionUsageEnvelope struct {
	Choices json.RawMessage       `json:"choices"`
	Usage   *completionUsageValue `json:"usage"`
	Metrics *completionMetrics    `json:"metrics"`
}

type completionUsageValue struct {
	PromptTokens     *int64 `json:"prompt_tokens"`
	CompletionTokens *int64 `json:"completion_tokens"`
}

type completionMetrics struct {
	MeanITLMilliseconds        *float64 `json:"mean_itl_ms"`
	GenerationTimeMilliseconds *float64 `json:"generation_time_ms"`
}

type responsesUsageEnvelope struct {
	Usage *responsesUsageValue `json:"usage"`
}

type responsesUsageEvent struct {
	Type     string                  `json:"type"`
	Response *responsesUsageEnvelope `json:"response"`
}

type responsesUsageValue struct {
	InputTokens  *int64 `json:"input_tokens"`
	OutputTokens *int64 `json:"output_tokens"`
}

func decodeCompletionUsage(payload []byte, streaming bool) (CompletionUsage, bool) {
	usage, outcome := decodeCompletionUsageEvidence(payload, streaming, CompletionUsageFormatCompletions)
	return usage, outcome == CompletionUsageAvailable
}

func decodeCompletionUsageEvidence(
	payload []byte,
	streaming bool,
	format CompletionUsageFormat,
) (CompletionUsage, CompletionUsageEvidenceOutcome) {
	if format == CompletionUsageFormatResponses {
		return decodeResponsesUsageEvidence(payload, streaming)
	}
	return decodeCompletionsUsageEvidence(payload, streaming)
}

func decodeCompletionsUsageEvidence(payload []byte, streaming bool) (CompletionUsage, CompletionUsageEvidenceOutcome) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var envelope completionUsageEnvelope
	if err := decoder.Decode(&envelope); err != nil || !jsonDecoderAtEOF(decoder) {
		return CompletionUsage{}, CompletionUsageMalformed
	}
	if envelope.Usage == nil {
		return CompletionUsage{}, CompletionUsageUnavailable
	}
	if envelope.Usage.CompletionTokens == nil {
		return CompletionUsage{}, CompletionUsageMalformed
	}
	choicesPayload := bytes.TrimSpace(envelope.Choices)
	if len(choicesPayload) < 2 || choicesPayload[0] != '[' || choicesPayload[len(choicesPayload)-1] != ']' {
		return CompletionUsage{}, CompletionUsageMalformed
	}
	var choices []json.RawMessage
	if json.Unmarshal(choicesPayload, &choices) != nil {
		return CompletionUsage{}, CompletionUsageMalformed
	}
	if streaming && len(choices) != 0 {
		return CompletionUsage{}, CompletionUsageUnavailable
	}
	if !streaming && len(choices) != 1 {
		return CompletionUsage{}, CompletionUsageMalformed
	}
	usage := CompletionUsage{CompletionTokens: *envelope.Usage.CompletionTokens}
	if usage.CompletionTokens <= 0 {
		return CompletionUsage{}, CompletionUsageMalformed
	}
	if envelope.Usage.PromptTokens != nil && *envelope.Usage.PromptTokens > 0 {
		usage.PromptTokens = *envelope.Usage.PromptTokens
	}
	if envelope.Metrics != nil {
		var ok bool
		if envelope.Metrics.MeanITLMilliseconds != nil {
			usage.MeanITL, ok = millisecondsDuration(*envelope.Metrics.MeanITLMilliseconds)
			if !ok {
				return CompletionUsage{}, CompletionUsageMalformed
			}
		}
		if envelope.Metrics.GenerationTimeMilliseconds != nil {
			usage.GenerationTime, ok = millisecondsDuration(*envelope.Metrics.GenerationTimeMilliseconds)
			if !ok {
				return CompletionUsage{}, CompletionUsageMalformed
			}
		}
	}
	return usage, CompletionUsageAvailable
}

func decodeResponsesUsageEvidence(payload []byte, streaming bool) (CompletionUsage, CompletionUsageEvidenceOutcome) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var envelope *responsesUsageEnvelope
	if streaming {
		var event responsesUsageEvent
		if err := decoder.Decode(&event); err != nil || !jsonDecoderAtEOF(decoder) {
			return CompletionUsage{}, CompletionUsageMalformed
		}
		if event.Type != "response.completed" {
			return CompletionUsage{}, CompletionUsageUnavailable
		}
		envelope = event.Response
	} else {
		envelope = &responsesUsageEnvelope{}
		if err := decoder.Decode(envelope); err != nil || !jsonDecoderAtEOF(decoder) {
			return CompletionUsage{}, CompletionUsageMalformed
		}
	}
	if envelope == nil || envelope.Usage == nil {
		return CompletionUsage{}, CompletionUsageUnavailable
	}
	if envelope.Usage.OutputTokens == nil || *envelope.Usage.OutputTokens <= 0 {
		return CompletionUsage{}, CompletionUsageMalformed
	}
	usage := CompletionUsage{CompletionTokens: *envelope.Usage.OutputTokens}
	if envelope.Usage.InputTokens != nil && *envelope.Usage.InputTokens > 0 {
		usage.PromptTokens = *envelope.Usage.InputTokens
	}
	return usage, CompletionUsageAvailable
}

func jsonDecoderAtEOF(decoder *json.Decoder) bool {
	var extra any
	return decoder.Decode(&extra) == io.EOF
}

func millisecondsDuration(value float64) (time.Duration, bool) {
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, false
	}
	nanoseconds := value * float64(time.Millisecond)
	if nanoseconds > float64(math.MaxInt64) {
		return 0, false
	}
	return time.Duration(math.Ceil(nanoseconds)), true
}

func CompletionUsageContentTypeEligible(contentType string, streaming bool) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return false
	}
	mediaType = strings.ToLower(mediaType)
	if streaming {
		return mediaType == "text/event-stream"
	}
	return mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
}
