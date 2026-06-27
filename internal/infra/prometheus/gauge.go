package prometheus

import (
	"strconv"
	"strings"
)

type GaugeAggregation int

const (
	GaugeSum GaugeAggregation = iota
	GaugeMax
)

func ParseGauge(text string, metricName string) (float64, bool) {
	var sum float64
	found := false
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := strings.SplitN(parts[0], "{", 2)[0]
		if name != metricName {
			continue
		}
		value, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}
		sum += value
		found = true
	}
	return sum, found
}

func ParseFirstGauge(text string, metricNames ...string) (float64, bool) {
	for _, metricName := range metricNames {
		value, ok := ParseGauge(text, metricName)
		if ok {
			return value, true
		}
	}
	return 0, false
}

func ParseGaugeSet(text string, metricNames ...string) map[string]float64 {
	aggregation := make(map[string]GaugeAggregation, len(metricNames))
	for _, metricName := range metricNames {
		aggregation[metricName] = GaugeSum
	}
	return ParseGaugeSetWithAggregation(text, aggregation)
}

func ParseGaugeSetWithAggregation(text string, aggregation map[string]GaugeAggregation) map[string]float64 {
	values := make(map[string]float64, len(aggregation))
	found := make(map[string]bool, len(aggregation))
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := strings.SplitN(parts[0], "{", 2)[0]
		mode, ok := aggregation[name]
		if !ok {
			continue
		}
		value, err := strconv.ParseFloat(parts[1], 64)
		if err != nil {
			continue
		}
		if !found[name] {
			values[name] = value
			found[name] = true
			continue
		}
		switch mode {
		case GaugeMax:
			if value > values[name] {
				values[name] = value
			}
		default:
			values[name] += value
		}
	}
	return values
}
