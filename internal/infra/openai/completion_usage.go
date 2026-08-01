package openai

import (
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
)

type CompletionUsage struct {
	CompletionTokens int64
	MeanITL          time.Duration
	GenerationTime   time.Duration
	ObservedAt       time.Time
}

type completionUsageCallback func(CompletionUsage)

type completionUsageBody struct {
	source   io.ReadCloser
	observer *CompletionUsageObserver
}

func ObserveCompletionUsageBody(source io.ReadCloser, streaming bool, callback func(CompletionUsage)) io.ReadCloser {
	if source == nil || callback == nil {
		return source
	}
	return &completionUsageBody{
		source:   source,
		observer: NewCompletionUsageObserver(streaming, callback),
	}
}

func NewCompletionUsageObserver(streaming bool, callback func(CompletionUsage)) *CompletionUsageObserver {
	if callback == nil {
		return nil
	}
	return &CompletionUsageObserver{streaming: streaming, callback: callback}
}

func (b *completionUsageBody) Read(buffer []byte) (int, error) {
	n, err := b.source.Read(buffer)
	if n > 0 {
		b.observer.Observe(buffer[:n])
	}
	if err == io.EOF {
		b.observer.Finish()
	}
	return n, err
}

func (b *completionUsageBody) Close() error {
	return b.source.Close()
}

type CompletionUsageObserver struct {
	streaming bool
	callback  completionUsageCallback
	finished  bool

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
	if o == nil || o.finished || len(chunk) == 0 {
		return
	}
	if !o.streaming {
		o.observeJSON(chunk)
		return
	}
	for _, value := range chunk {
		if value == '\n' {
			o.processSSELine()
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
	if !o.eventLimited && o.eventHasData {
		data := bytes.TrimSpace(o.eventData)
		if len(data) > 0 && !bytes.Equal(data, []byte("[DONE]")) && bytes.Contains(data, []byte(`"usage"`)) {
			if usage, ok := decodeCompletionUsage(data, true); ok {
				usage.ObservedAt = time.Now()
				if o.candidateValid {
					o.candidateInvalid = true
				} else {
					o.candidate = usage
					o.candidateValid = true
				}
			}
		}
	}
	o.eventData = o.eventData[:0]
	o.eventLimited = false
	o.eventHasData = false
}

func (o *CompletionUsageObserver) Finish() {
	if o == nil || o.finished {
		return
	}
	if o.streaming {
		if len(o.line) > 0 {
			o.processSSELine()
		}
		if o.eventHasData || o.eventLimited {
			o.finishSSEEvent()
		}
		o.finished = true
		if o.candidateValid && !o.candidateInvalid {
			o.emit(o.candidate)
		}
		return
	}
	o.finished = true
	if o.jsonLimited || len(o.jsonBody) == 0 {
		return
	}
	if usage, ok := decodeCompletionUsage(o.jsonBody, false); ok {
		usage.ObservedAt = time.Now()
		o.emit(usage)
	}
}

func (o *CompletionUsageObserver) emit(usage CompletionUsage) {
	if o.callback == nil {
		return
	}
	o.callback(usage)
}

type completionUsageEnvelope struct {
	Choices json.RawMessage       `json:"choices"`
	Usage   *completionUsageValue `json:"usage"`
	Metrics *completionMetrics    `json:"metrics"`
}

type completionUsageValue struct {
	CompletionTokens *int64 `json:"completion_tokens"`
}

type completionMetrics struct {
	MeanITLMilliseconds        *float64 `json:"mean_itl_ms"`
	GenerationTimeMilliseconds *float64 `json:"generation_time_ms"`
}

func decodeCompletionUsage(payload []byte, streaming bool) (CompletionUsage, bool) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	var envelope completionUsageEnvelope
	if err := decoder.Decode(&envelope); err != nil || !jsonDecoderAtEOF(decoder) || envelope.Usage == nil || envelope.Usage.CompletionTokens == nil {
		return CompletionUsage{}, false
	}
	choicesPayload := bytes.TrimSpace(envelope.Choices)
	if len(choicesPayload) < 2 || choicesPayload[0] != '[' || choicesPayload[len(choicesPayload)-1] != ']' {
		return CompletionUsage{}, false
	}
	var choices []json.RawMessage
	if json.Unmarshal(choicesPayload, &choices) != nil || (streaming && len(choices) != 0) || (!streaming && len(choices) != 1) {
		return CompletionUsage{}, false
	}
	usage := CompletionUsage{CompletionTokens: *envelope.Usage.CompletionTokens}
	if usage.CompletionTokens <= 0 {
		return CompletionUsage{}, false
	}
	if envelope.Metrics != nil {
		var ok bool
		if envelope.Metrics.MeanITLMilliseconds != nil {
			usage.MeanITL, ok = millisecondsDuration(*envelope.Metrics.MeanITLMilliseconds)
			if !ok {
				return CompletionUsage{}, false
			}
		}
		if envelope.Metrics.GenerationTimeMilliseconds != nil {
			usage.GenerationTime, ok = millisecondsDuration(*envelope.Metrics.GenerationTimeMilliseconds)
			if !ok {
				return CompletionUsage{}, false
			}
		}
	}
	return usage, true
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
