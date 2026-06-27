package prometheus

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func ParseFirstHistogram(text string, metricNames ...string) telemetry.HistogramSample {
	targets := make(map[string]struct{}, len(metricNames))
	builders := make(map[string]*histogramBuilder, len(metricNames))
	for _, metricName := range metricNames {
		targets[metricName] = struct{}{}
		builders[metricName] = &histogramBuilder{buckets: map[float64]uint64{}}
	}
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
		base := ""
		kind := ""
		switch {
		case strings.HasSuffix(name, "_bucket"):
			base = strings.TrimSuffix(name, "_bucket")
			kind = "bucket"
		case strings.HasSuffix(name, "_count"):
			base = strings.TrimSuffix(name, "_count")
			kind = "count"
		case strings.HasSuffix(name, "_sum"):
			base = strings.TrimSuffix(name, "_sum")
			kind = "sum"
		default:
			continue
		}
		if _, ok := targets[base]; !ok {
			continue
		}
		value, err := strconv.ParseFloat(parts[1], 64)
		if err != nil || value < 0 {
			continue
		}
		builder := builders[base]
		switch kind {
		case "bucket":
			upperBound, ok := parseLE(parts[0])
			if !ok {
				continue
			}
			builder.buckets[upperBound] += uint64(value)
		case "count":
			builder.count += uint64(value)
		case "sum":
			builder.sum += value
		}
	}
	for _, metricName := range metricNames {
		sample := builders[metricName].sample()
		if sample.Count > 0 {
			return sample
		}
	}
	return telemetry.HistogramSample{}
}

func parseLE(metricHead string) (float64, bool) {
	index := strings.Index(metricHead, `le="`)
	if index < 0 {
		return 0, false
	}
	rest := metricHead[index+4:]
	end := strings.IndexByte(rest, '"')
	if end < 0 {
		return 0, false
	}
	raw := rest[:end]
	if raw == "+Inf" {
		return math.Inf(1), true
	}
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

type histogramBuilder struct {
	count   uint64
	sum     float64
	buckets map[float64]uint64
}

func (b *histogramBuilder) sample() telemetry.HistogramSample {
	if b == nil {
		return telemetry.HistogramSample{}
	}
	count := b.count
	if count == 0 {
		if infinityCount, ok := b.buckets[math.Inf(1)]; ok {
			count = infinityCount
		}
	}
	buckets := make([]telemetry.HistogramBucketSample, 0, len(b.buckets))
	for upperBound, bucketCount := range b.buckets {
		buckets = append(buckets, telemetry.HistogramBucketSample{UpperBound: upperBound, Count: bucketCount})
	}
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].UpperBound < buckets[j].UpperBound
	})
	return telemetry.HistogramSample{
		Count:   count,
		Sum:     b.sum,
		Buckets: buckets,
	}
}
