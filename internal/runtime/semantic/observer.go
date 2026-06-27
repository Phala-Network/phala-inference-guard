package semantic

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const ScanLimitBytes = 64 * 1024

type Observer struct {
	started   time.Time
	scanned   int
	found     bool
	limited   bool
	line      []byte
	eventData []string
}

func New(started time.Time) *Observer {
	return &Observer{started: started}
}

func Eligible(response *http.Response, streaming bool) bool {
	if !streaming || response == nil || response.StatusCode != http.StatusOK {
		return false
	}
	return strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream")
}

func (o *Observer) Started() time.Time {
	if o == nil {
		return time.Time{}
	}
	return o.started
}

func (o *Observer) Observe(chunk []byte) (found, limited bool) {
	if o == nil || o.found || o.limited || len(chunk) == 0 {
		return false, false
	}
	remaining := ScanLimitBytes - o.scanned
	if remaining <= 0 {
		o.limited = true
		return false, true
	}
	if len(chunk) > remaining {
		chunk = chunk[:remaining]
		o.limited = true
	}
	o.scanned += len(chunk)
	for _, b := range chunk {
		if b == '\n' {
			if o.processLine() {
				o.found = true
				return true, false
			}
			continue
		}
		o.line = append(o.line, b)
	}
	return false, o.limited
}

func (o *Observer) processLine() bool {
	line := string(o.line)
	o.line = o.line[:0]
	line = strings.TrimSuffix(line, "\r")
	if line == "" {
		return o.finishEvent()
	}
	if strings.HasPrefix(line, ":") {
		return false
	}
	if !strings.HasPrefix(line, "data:") {
		return false
	}
	value := strings.TrimPrefix(line, "data:")
	value = strings.TrimPrefix(value, " ")
	o.eventData = append(o.eventData, value)
	return false
}

func (o *Observer) finishEvent() bool {
	if len(o.eventData) == 0 {
		return false
	}
	data := strings.Join(o.eventData, "\n")
	o.eventData = o.eventData[:0]
	return dataHasSemanticDelta(data)
}

func dataHasSemanticDelta(data string) bool {
	data = strings.TrimSpace(data)
	if data == "" || data == "[DONE]" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(data), &payload); err != nil {
		return false
	}
	if choices, ok := payload["choices"].([]any); ok {
		for _, rawChoice := range choices {
			choice, ok := rawChoice.(map[string]any)
			if !ok {
				continue
			}
			if delta, ok := choice["delta"].(map[string]any); ok && deltaHasSemanticValue(delta) {
				return true
			}
		}
	}
	if delta, ok := payload["delta"].(string); ok && strings.TrimSpace(delta) != "" {
		if eventType, ok := payload["type"].(string); !ok || responseEventType(eventType) {
			return true
		}
	}
	return false
}

func deltaHasSemanticValue(delta map[string]any) bool {
	for _, key := range []string{"reasoning_content", "reasoning", "content"} {
		if usefulSemanticValue(delta[key]) {
			return true
		}
	}
	return usefulSemanticValue(delta["tool_calls"]) || usefulSemanticValue(delta["function_call"])
}

func responseEventType(eventType string) bool {
	eventType = strings.ToLower(eventType)
	return strings.Contains(eventType, "output_text") ||
		strings.Contains(eventType, "reasoning") ||
		strings.Contains(eventType, "tool")
}

func usefulSemanticValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return strings.TrimSpace(typed) != ""
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}
