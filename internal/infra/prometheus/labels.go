package prometheus

import (
	"strconv"
	"strings"
)

func ParseInfoLabelFloat(text, metricName string, labelNames ...string) (float64, bool) {
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, metricName+"{") {
			continue
		}
		open := strings.IndexByte(line, '{')
		close := strings.LastIndexByte(line, '}')
		if open < 0 || close <= open {
			continue
		}
		labels, ok := parseLabelSet(line[open+1 : close])
		if !ok {
			continue
		}
		for _, name := range labelNames {
			raw, found := labels[name]
			if !found {
				continue
			}
			value, err := strconv.ParseFloat(raw, 64)
			if err == nil {
				return value, true
			}
		}
	}
	return 0, false
}

func parseLabelSet(raw string) (map[string]string, bool) {
	labels := make(map[string]string)
	for index := 0; index < len(raw); {
		for index < len(raw) && (raw[index] == ',' || raw[index] == ' ' || raw[index] == '\t') {
			index++
		}
		if index >= len(raw) {
			break
		}
		start := index
		for index < len(raw) && raw[index] != '=' {
			index++
		}
		if index == start || index >= len(raw) {
			return nil, false
		}
		name := strings.TrimSpace(raw[start:index])
		index++
		if index >= len(raw) || raw[index] != '"' {
			return nil, false
		}
		quotedStart := index
		index++
		escaped := false
		for index < len(raw) {
			char := raw[index]
			if char == '"' && !escaped {
				index++
				break
			}
			if char == '\\' && !escaped {
				escaped = true
			} else {
				escaped = false
			}
			index++
		}
		if index > len(raw) || raw[index-1] != '"' {
			return nil, false
		}
		value, err := strconv.Unquote(raw[quotedStart:index])
		if err != nil {
			return nil, false
		}
		labels[name] = value
	}
	return labels, true
}
