package metrics

import (
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/lane"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/output"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type LaneThresholds struct {
	MediumBodyBytes      int64
	LongBodyBytes        int64
	VeryLongBodyBytes    int64
	MediumOutputTokens   int
	LongOutputTokens     int
	VeryLongOutputTokens int
}

type LaneInput struct {
	Snapshots                 []lane.Snapshot
	DurationBucketsSeconds    []float64
	BodyBucketsBytes          []int64
	Thresholds                LaneThresholds
	AdaptiveOutputEnabled     bool
	AdaptiveOutputSamples     int
	EffectiveOutputThresholds output.Thresholds
}

func WriteLanes(w io.Writer, input LaneInput) {
	for _, snapshot := range input.Snapshots {
		writeLaneSnapshot(w, input, snapshot)
	}
	writeLaneThresholds(w, input)
}

func writeLaneSnapshot(w io.Writer, input LaneInput, snapshot lane.Snapshot) {
	fmt.Fprintf(w, "pig_requests_total{lane=%q,decision=%q} %d\n", snapshot.Name, "accepted", snapshot.Accepted)
	fmt.Fprintf(w, "pig_requests_total{lane=%q,decision=%q} %d\n", snapshot.Name, "rejected", snapshot.Rejected)
	fmt.Fprintf(w, "pig_rejected_total{lane=%q} %d\n", snapshot.Name, snapshot.Rejected)
	fmt.Fprintf(w, "pig_inflight{lane=%q} %d\n", snapshot.Name, snapshot.Inflight)
	completed := snapshot.Completed
	fmt.Fprintf(w, "pig_completed_total{lane=%q} %d\n", snapshot.Name, completed)
	fmt.Fprintf(w, "pig_request_duration_seconds_count{lane=%q} %d\n", snapshot.Name, completed)
	fmt.Fprintf(w, "pig_request_duration_seconds_sum{lane=%q} %.6f\n", snapshot.Name, float64(snapshot.DurationNs)/float64(time.Second))
	for index, upper := range input.DurationBucketsSeconds {
		count := uint64(0)
		if index < len(snapshot.DurationBuckets) {
			count = snapshot.DurationBuckets[index]
		}
		fmt.Fprintf(w, "pig_request_duration_seconds_bucket{lane=%q,le=%q} %d\n", snapshot.Name, strconv.FormatFloat(upper, 'f', -1, 64), count)
	}
	fmt.Fprintf(w, "pig_request_duration_seconds_bucket{lane=%q,le=%q} %d\n", snapshot.Name, "+Inf", completed)
	for class := 0; class < len(snapshot.StatusClasses); class++ {
		fmt.Fprintf(w, "pig_response_status_class_total{lane=%q,class=%q} %d\n", snapshot.Name, strconv.Itoa(class)+"xx", snapshot.StatusClasses[class])
	}
	bodyCount := snapshot.BodyCount
	fmt.Fprintf(w, "pig_request_body_bytes_count{lane=%q} %d\n", snapshot.Name, bodyCount)
	fmt.Fprintf(w, "pig_request_body_bytes_sum{lane=%q} %d\n", snapshot.Name, snapshot.BodyBytes)
	for index, upper := range input.BodyBucketsBytes {
		count := uint64(0)
		if index < len(snapshot.BodyBuckets) {
			count = snapshot.BodyBuckets[index]
		}
		fmt.Fprintf(w, "pig_request_body_bytes_bucket{lane=%q,le=%q} %d\n", snapshot.Name, strconv.FormatInt(upper, 10), count)
	}
	fmt.Fprintf(w, "pig_request_body_bytes_bucket{lane=%q,le=%q} %d\n", snapshot.Name, "+Inf", bodyCount)
}

func writeLaneThresholds(w io.Writer, input LaneInput) {
	thresholds := input.Thresholds
	fmt.Fprintf(w, "pig_body_threshold_bytes{lane=%q} %d\n", "medium_body", thresholds.MediumBodyBytes)
	fmt.Fprintf(w, "pig_body_threshold_bytes{lane=%q} %d\n", "long_body", thresholds.LongBodyBytes)
	fmt.Fprintf(w, "pig_body_threshold_bytes{lane=%q} %d\n", "very_long_body", thresholds.VeryLongBodyBytes)
	fmt.Fprintf(w, "pig_output_threshold_tokens{lane=%q} %d\n", "medium_output", thresholds.MediumOutputTokens)
	fmt.Fprintf(w, "pig_output_threshold_tokens{lane=%q} %d\n", "long_output", thresholds.LongOutputTokens)
	fmt.Fprintf(w, "pig_output_threshold_tokens{lane=%q} %d\n", "very_long_output", thresholds.VeryLongOutputTokens)
	effective := input.EffectiveOutputThresholds
	fmt.Fprintf(w, "pig_adaptive_output_enabled %d\n", num.BoolAsInt(input.AdaptiveOutputEnabled))
	fmt.Fprintf(w, "pig_adaptive_output_samples %d\n", input.AdaptiveOutputSamples)
	fmt.Fprintf(w, "pig_effective_output_threshold_tokens{lane=%q} %d\n", "medium_output", effective.Medium)
	fmt.Fprintf(w, "pig_effective_output_threshold_tokens{lane=%q} %d\n", "long_output", effective.Long)
	fmt.Fprintf(w, "pig_effective_output_threshold_tokens{lane=%q} %d\n", "very_long_output", effective.VeryLong)
}
