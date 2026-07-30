package request

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type JSONFields struct {
	OutputTokens    int
	HasOutputTokens bool
	Stream          bool
	HasStream       bool
}

func ParseJSONFields(body []byte, fields []string) (JSONFields, bool) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	first, err := decoder.Token()
	if err != nil {
		return JSONFields{}, false
	}
	if delim, ok := first.(json.Delim); !ok || delim != '{' {
		return JSONFields{}, false
	}
	result := JSONFields{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return JSONFields{}, false
		}
		key, ok := keyToken.(string)
		if !ok {
			return JSONFields{}, false
		}
		if stringInList(key, fields) {
			value, ok, err := decodeOutputTokenValue(decoder)
			if err != nil {
				return JSONFields{}, false
			}
			if ok && !result.HasOutputTokens {
				result.OutputTokens = value
				result.HasOutputTokens = true
			}
			continue
		}
		if key == "stream" {
			value, ok, err := decodeJSONBool(decoder)
			if err != nil {
				return JSONFields{}, false
			}
			if ok {
				result.Stream = value
				result.HasStream = true
			}
			continue
		}
		if err := skipJSONValue(decoder); err != nil {
			return JSONFields{}, false
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return JSONFields{}, false
	}
	if delim, ok := closing.(json.Delim); !ok || delim != '}' {
		return JSONFields{}, false
	}
	if _, err := decoder.Token(); err != io.EOF {
		return JSONFields{}, false
	}
	return result, true
}

func ParseOutputTokens(body []byte, fields []string) (int, bool) {
	result, ok := ParseJSONFields(body, fields)
	if !ok {
		return 0, false
	}
	return result.OutputTokens, result.HasOutputTokens
}

func stringInList(value string, values []string) bool {
	for _, candidate := range values {
		if value == candidate {
			return true
		}
	}
	return false
}

func decodeOutputTokenValue(decoder *json.Decoder) (int, bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return 0, false, err
	}
	switch value := token.(type) {
	case json.Number:
		tokens, err := strconv.Atoi(value.String())
		if err == nil && tokens >= 0 {
			return tokens, true, nil
		}
	case string:
		tokens, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && tokens >= 0 {
			return tokens, true, nil
		}
	case float64:
		tokens := int(value)
		if value == float64(tokens) && tokens >= 0 {
			return tokens, true, nil
		}
	case json.Delim:
		return 0, false, skipDelimitedJSONValue(decoder, value)
	}
	return 0, false, nil
}

func decodeJSONBool(decoder *json.Decoder) (bool, bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, false, err
	}
	if value, ok := token.(bool); ok {
		return value, true, nil
	}
	if delim, ok := token.(json.Delim); ok {
		if err := skipDelimitedJSONValue(decoder, delim); err != nil {
			return false, false, err
		}
	}
	return false, false, nil
}

func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); ok {
		return skipDelimitedJSONValue(decoder, delim)
	}
	return nil
}

func skipDelimitedJSONValue(decoder *json.Decoder, start json.Delim) error {
	switch start {
	case '{':
		for decoder.More() {
			if _, err := decoder.Token(); err != nil {
				return err
			}
			if err := skipJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := closing.(json.Delim); !ok || delim != '}' {
			return fmt.Errorf("invalid object closing token")
		}
	case '[':
		for decoder.More() {
			if err := skipJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if delim, ok := closing.(json.Delim); !ok || delim != ']' {
			return fmt.Errorf("invalid array closing token")
		}
	default:
		return nil
	}
	return nil
}
