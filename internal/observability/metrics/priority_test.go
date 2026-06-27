package metrics

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Phala-Network/phala-inference-guard/internal/runtime/telemetry"
)

func TestWritePriorityExposesRewriteCounters(t *testing.T) {
	var out bytes.Buffer
	WritePriority(&out, PriorityInput{
		Enabled:           true,
		BodyBytes:         33554432,
		BufferBytes:       33554432,
		StreamBufferBytes: 1048576,
		Limit:             64,
		Inflight:          2,
		Rewritten:         10,
		Skipped:           3,
		Failed:            1,
		DurationCount:     10,
		DurationSeconds:   0.25,
		DurationBuckets: []telemetry.HistogramBucketSample{
			{UpperBound: 0.001, Count: 4},
			{UpperBound: 0.005, Count: 7},
		},
		DurationMaxSeconds: 0.12,
	})

	got := out.String()
	for _, want := range []string{
		"pig_backend_priority_injection_enabled 1",
		"pig_backend_priority_body_bytes 33554432",
		"pig_backend_priority_buffer_bytes 33554432",
		"pig_backend_priority_stream_buffer_bytes 1048576",
		"pig_backend_priority_rewrite_limit 64",
		"pig_backend_priority_rewrite_inflight 2",
		"pig_backend_priority_rewrite_total 10",
		"pig_backend_priority_skipped_total 3",
		"pig_backend_priority_failed_total 1",
		"pig_backend_priority_rewrite_duration_seconds_count 10",
		"pig_backend_priority_rewrite_duration_seconds_sum 0.250000",
		`pig_backend_priority_rewrite_duration_seconds_bucket{le="0.001"} 4`,
		`pig_backend_priority_rewrite_duration_seconds_bucket{le="0.005"} 7`,
		`pig_backend_priority_rewrite_duration_seconds_bucket{le="+Inf"} 10`,
		"pig_backend_priority_rewrite_duration_max_seconds 0.120000",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output missing %q\noutput:\n%s", want, got)
		}
	}
}
