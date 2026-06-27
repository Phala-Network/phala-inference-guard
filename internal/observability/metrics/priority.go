package metrics

import (
	"fmt"
	"io"
	"strconv"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type PriorityInput struct {
	Enabled            bool
	BodyBytes          int64
	BufferBytes        int64
	StreamBufferBytes  int
	Limit              int
	Inflight           int64
	Rewritten          uint64
	Skipped            uint64
	Failed             uint64
	DurationCount      uint64
	DurationSeconds    float64
	DurationBuckets    []telemetry.HistogramBucketSample
	DurationMaxSeconds float64
}

func WritePriority(w io.Writer, input PriorityInput) {
	fmt.Fprintf(w, "pig_backend_priority_injection_enabled %d\n", num.BoolAsInt(input.Enabled))
	fmt.Fprintf(w, "pig_backend_priority_body_bytes %d\n", input.BodyBytes)
	fmt.Fprintf(w, "pig_backend_priority_buffer_bytes %d\n", input.BufferBytes)
	fmt.Fprintf(w, "pig_backend_priority_stream_buffer_bytes %d\n", input.StreamBufferBytes)
	fmt.Fprintf(w, "pig_backend_priority_rewrite_limit %d\n", input.Limit)
	fmt.Fprintf(w, "pig_backend_priority_rewrite_inflight %d\n", input.Inflight)
	fmt.Fprintf(w, "pig_backend_priority_rewrite_total %d\n", input.Rewritten)
	fmt.Fprintf(w, "pig_backend_priority_skipped_total %d\n", input.Skipped)
	fmt.Fprintf(w, "pig_backend_priority_failed_total %d\n", input.Failed)
	fmt.Fprintf(w, "pig_backend_priority_rewrite_duration_seconds_count %d\n", input.DurationCount)
	fmt.Fprintf(w, "pig_backend_priority_rewrite_duration_seconds_sum %f\n", input.DurationSeconds)
	for _, bucket := range input.DurationBuckets {
		fmt.Fprintf(w, "pig_backend_priority_rewrite_duration_seconds_bucket{le=%q} %d\n", strconv.FormatFloat(bucket.UpperBound, 'f', -1, 64), bucket.Count)
	}
	fmt.Fprintf(w, "pig_backend_priority_rewrite_duration_seconds_bucket{le=%q} %d\n", "+Inf", input.DurationCount)
	fmt.Fprintf(w, "pig_backend_priority_rewrite_duration_max_seconds %f\n", input.DurationMaxSeconds)
}
