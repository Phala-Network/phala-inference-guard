package prometheus

import (
	"strconv"
	"strings"
)

type indexedMetricSample struct {
	labels      map[string]string
	labelsValid bool
	value       float64
	valueValid  bool
}

type metricIndex struct {
	samples map[string][]indexedMetricSample
	types   map[string]string
}

func newMetricIndex(text string, wanted map[string]struct{}) metricIndex {
	index := metricIndex{
		samples: make(map[string][]indexedMetricSample, len(wanted)),
		types:   make(map[string]string),
	}
	forEachPrometheusLine(text, func(line string) {
		if strings.HasPrefix(line, "#") {
			fields := strings.Fields(line)
			if len(fields) == 4 && fields[0] == "#" && fields[1] == "TYPE" {
				if _, ok := wanted[fields[2]]; ok {
					index.types[fields[2]] = fields[3]
				}
			}
			return
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return
		}
		head := fields[0]
		name := strings.SplitN(head, "{", 2)[0]
		if _, ok := wanted[name]; !ok {
			return
		}
		value, valueErr := strconv.ParseFloat(fields[1], 64)
		sample := indexedMetricSample{value: value, valueValid: valueErr == nil}
		if open := strings.IndexByte(head, '{'); open >= 0 {
			close := strings.LastIndexByte(head, '}')
			if close > open {
				sample.labels, sample.labelsValid = parseLabelSet(head[open+1 : close])
			}
		} else {
			sample.labels = map[string]string{}
			sample.labelsValid = true
		}
		index.samples[name] = append(index.samples[name], sample)
	})
	return index
}

func forEachPrometheusLine(text string, visit func(string)) {
	for start := 0; start <= len(text); {
		end := strings.IndexByte(text[start:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += start
		}
		line := strings.TrimSpace(text[start:end])
		if line != "" {
			visit(line)
		}
		if end == len(text) {
			return
		}
		start = end + 1
	}
}

func (i metricIndex) has(name string) bool {
	return len(i.samples[name]) > 0 || i.types[name] != ""
}

func (i metricIndex) hasAny(names ...string) bool {
	for _, name := range names {
		if i.has(name) {
			return true
		}
	}
	return false
}

func (i metricIndex) declaredType(name, metricType string) bool {
	return i.types[name] == metricType
}

func (i metricIndex) sum(name string, include func(map[string]string) bool) (float64, bool) {
	var value float64
	found := false
	for _, sample := range i.samples[name] {
		if !sample.valueValid || !sample.labelsValid {
			return 0, false
		}
		if include != nil && !include(sample.labels) {
			continue
		}
		value += sample.value
		found = true
	}
	return value, found
}

func (i metricIndex) maximum(name string, include func(map[string]string) bool) (float64, bool) {
	var value float64
	found := false
	for _, sample := range i.samples[name] {
		if !sample.valueValid || !sample.labelsValid {
			return 0, false
		}
		if include != nil && !include(sample.labels) {
			continue
		}
		if !found || sample.value > value {
			value = sample.value
		}
		found = true
	}
	return value, found
}

func (i metricIndex) uniqueValue(name string) (float64, bool) {
	var value float64
	found := false
	for _, sample := range i.samples[name] {
		if !sample.valueValid || !sample.labelsValid {
			return 0, false
		}
		if !found {
			value = sample.value
			found = true
			continue
		}
		if sample.value != value {
			return 0, false
		}
	}
	return value, found
}

func (i metricIndex) uniqueFloatLabel(name string, labelNames ...string) (float64, bool) {
	var value float64
	found := false
	for _, sample := range i.samples[name] {
		if !sample.labelsValid {
			return 0, false
		}
		raw := ""
		for _, labelName := range labelNames {
			if candidate, ok := sample.labels[labelName]; ok {
				raw = candidate
				break
			}
		}
		if raw == "" {
			return 0, false
		}
		candidate, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, false
		}
		if !found {
			value = candidate
			found = true
			continue
		}
		if candidate != value {
			return 0, false
		}
	}
	return value, found
}

func (i metricIndex) requiredUniqueLabel(metricNames []string, labelName string) (string, bool) {
	value := ""
	for _, metricName := range metricNames {
		samples := i.samples[metricName]
		if len(samples) == 0 {
			return "", false
		}
		for _, sample := range samples {
			if !sample.labelsValid {
				return "", false
			}
			candidate := sample.labels[labelName]
			if candidate == "" || (value != "" && candidate != value) {
				return "", false
			}
			value = candidate
		}
	}
	return value, value != ""
}
