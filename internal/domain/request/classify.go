package request

import (
	"net/http"
	"strings"

	"github.com/Phala-Network/phala-inference-guard/internal/domain/lane"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/output"
)

type PathConfig struct {
	Paths       []string
	SuffixMatch bool
}

type BodyThresholds struct {
	Medium   int64
	Long     int64
	VeryLong int64
}

type BodyLanes struct {
	Default  *lane.Lane
	Medium   *lane.Lane
	Long     *lane.Lane
	VeryLong *lane.Lane
	Unknown  *lane.Lane
}

type OutputLanes struct {
	Default  *lane.Lane
	Medium   *lane.Lane
	Long     *lane.Lane
	VeryLong *lane.Lane
}

func AdmittedPath(r *http.Request, cfg PathConfig) bool {
	if r.Method != http.MethodPost {
		return false
	}
	for _, path := range cfg.Paths {
		if r.URL.Path == path || (cfg.SuffixMatch && strings.HasSuffix(r.URL.Path, path)) {
			return true
		}
	}
	return false
}

func HasChunkedTransferEncoding(r *http.Request) bool {
	for _, encoding := range r.TransferEncoding {
		if strings.EqualFold(encoding, "chunked") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(r.Header.Get("Transfer-Encoding")), "chunked")
}

func WantsEventStream(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

func BodyLane(r *http.Request, lanes BodyLanes, thresholds BodyThresholds) *lane.Lane {
	if HasChunkedTransferEncoding(r) || r.ContentLength < 0 {
		return lanes.Unknown
	}
	if r.ContentLength >= thresholds.VeryLong {
		return lanes.VeryLong
	}
	if r.ContentLength >= thresholds.Long {
		return lanes.Long
	}
	if r.ContentLength >= thresholds.Medium {
		return lanes.Medium
	}
	return lanes.Default
}

func OutputLane(tokens int, lanes OutputLanes, thresholds output.Thresholds) *lane.Lane {
	if tokens >= thresholds.VeryLong {
		return lanes.VeryLong
	}
	if tokens >= thresholds.Long {
		return lanes.Long
	}
	if tokens >= thresholds.Medium {
		return lanes.Medium
	}
	return lanes.Default
}

func MoreRestrictiveLane(a, b *lane.Lane) *lane.Lane {
	if laneSeverity(b) > laneSeverity(a) {
		return b
	}
	return a
}

func SafeForEarlySSEBridge(r *http.Request, veryLongBodyBytes int64, veryLongOutputTokens, outputTokens int, hasOutputTokens bool) bool {
	if r == nil {
		return false
	}
	if HasChunkedTransferEncoding(r) || r.ContentLength < 0 {
		return false
	}
	if r.ContentLength >= veryLongBodyBytes {
		return false
	}
	return !hasOutputTokens || outputTokens < veryLongOutputTokens
}

func laneSeverity(ln *lane.Lane) int {
	if ln == nil {
		return 0
	}
	switch ln.Name() {
	case "unknown_body":
		return 4
	case "very_long_body", "very_long_output":
		return 3
	case "long_body", "long_output":
		return 2
	case "medium_body", "medium_output":
		return 1
	default:
		return 0
	}
}
