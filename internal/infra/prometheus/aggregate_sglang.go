package prometheus

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func AggregatePDDecodeSamples(samples []telemetry.Sample) (telemetry.Sample, error) {
	if len(samples) < 2 || len(samples) > maximumAggregateMetricsEndpoints {
		return telemetry.Sample{}, fmt.Errorf("PD Decode aggregation requires between 2 and %d endpoints", maximumAggregateMetricsEndpoints)
	}
	first := samples[0]
	modelName := strings.TrimSpace(first.ModelName)
	if !validPDDecodeEndpointSample(first) || modelName == "" {
		return telemetry.Sample{}, fmt.Errorf("PD Decode endpoint metrics are incomplete")
	}

	aggregated := telemetry.Sample{
		BackendKind:        "sglang",
		BackendRole:        "decode",
		ModelName:          modelName,
		ModelNameValid:     true,
		RunningValid:       true,
		WaitingValid:       true,
		DecodePendingValid: true,
		PreemptionsValid:   true,
		GenerationValid:    true,
		RuntimeStartTime:   first.RuntimeStartTime,
	}
	var runtimeEpochInput strings.Builder
	maxInt := int(^uint(0) >> 1)
	for _, sample := range samples {
		if !validPDDecodeEndpointSample(sample) || sample.ModelName != modelName {
			return telemetry.Sample{}, fmt.Errorf("PD Decode endpoint identity or counters are inconsistent")
		}
		if sample.Running > maxInt-aggregated.Running || sample.Waiting > maxInt-aggregated.Waiting ||
			sample.DecodePending > maxInt-aggregated.DecodePending {
			return telemetry.Sample{}, fmt.Errorf("PD Decode request count overflow")
		}
		if sample.Generation > ^uint64(0)-aggregated.Generation ||
			sample.Preemptions > ^uint64(0)-aggregated.Preemptions {
			return telemetry.Sample{}, fmt.Errorf("PD Decode counter overflow")
		}
		if !finitePositive(sample.RuntimeStartTime) {
			return telemetry.Sample{}, fmt.Errorf("PD Decode runtime epoch is invalid")
		}
		aggregated.Running += sample.Running
		aggregated.Waiting += sample.Waiting
		aggregated.DecodePending += sample.DecodePending
		aggregated.Generation += sample.Generation
		aggregated.Preemptions += sample.Preemptions
		runtimeEpochInput.WriteString(strconv.FormatFloat(sample.RuntimeStartTime, 'g', -1, 64))
		runtimeEpochInput.WriteByte(0)
	}
	if aggregated.Running > maxInt-aggregated.Waiting ||
		aggregated.DecodePending > maxInt-aggregated.Running-aggregated.Waiting ||
		!finitePositive(aggregated.RuntimeStartTime) {
		return telemetry.Sample{}, fmt.Errorf("PD Decode aggregate is outside supported bounds")
	}
	aggregated.RuntimeStartTimeValid = true
	// Keep one real endpoint timestamp; the digest below carries the fleet epoch.
	runtimeEpochDigest := sha256.Sum256([]byte(runtimeEpochInput.String()))
	aggregated.RuntimeEpochIdentity = hex.EncodeToString(runtimeEpochDigest[:])
	return aggregated, nil
}

func validPDDecodeEndpointSample(sample telemetry.Sample) bool {
	return sample.BackendKind == "sglang" && sample.BackendRole == "decode" &&
		sample.ModelNameValid && strings.TrimSpace(sample.ModelName) != "" &&
		sample.RunningValid && sample.WaitingValid && sample.DecodePendingValid &&
		sample.PreemptionsValid && sample.GenerationValid &&
		sample.Running >= 0 && sample.Waiting >= 0 && sample.DecodePending >= 0 &&
		sample.RuntimeStartTimeValid && finitePositive(sample.RuntimeStartTime)
}
