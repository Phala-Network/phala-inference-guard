package metrics

import (
	"fmt"
	"io"
	"time"

	requesttier "github.com/Phala-Network/phala-inference-guard/internal/domain/request"
	"github.com/Phala-Network/phala-inference-guard/internal/domain/tier"
	"github.com/Phala-Network/phala-inference-guard/internal/observability/histogram"
	"github.com/Phala-Network/phala-inference-guard/internal/support/num"
)

type RuntimeConfig struct {
	Version                    string
	QueueWait                  time.Duration
	QueueWaitEffectiveCap      time.Duration
	QueuePoll                  time.Duration
	DynamicEnabled             bool
	DynamicEnforce             bool
	SemanticTTFTScanLimitBytes int
	BackendCount               int
}

type QueueSnapshot struct {
	Current        int64
	Total          uint64
	Timeout        uint64
	WaitCount      uint64
	WaitSecondsSum float64
}

type StreamSnapshot struct {
	KeepAliveStreams  uint64
	KeepAliveComments uint64
	BridgeStreams     uint64
	BridgeUpstreamErr uint64
	BridgeInvalid     uint64
	BridgeCopyErr     uint64
}

type ErrorSnapshot struct {
	RequestSemanticTTFTLimited uint64
	ProxyUpstreamErr           uint64
	ProxyCopyErr               uint64
	ClientDisconnectQueue      uint64
	ClientDisconnectUpstream   uint64
	ClientDisconnectResponse   uint64
	ClientDisconnectCancel     uint64
}

type DynamicCounterSnapshot struct {
	PollOK             uint64
	PollFailed         uint64
	DynamicRejected    uint64
	BackendUnavailable uint64
}

type RuntimeHistograms struct {
	DecisionDuration    *histogram.DurationHistogram
	ProxyTTFB           *histogram.DurationHistogram
	RequestSemanticTTFT *histogram.DurationHistogram
	ProxyTotal          *histogram.DurationHistogram
	InternalOverhead    *histogram.DurationHistogram
}

type RuntimeInput struct {
	Config        RuntimeConfig
	Uptime        time.Duration
	RejectedTotal uint64
	Queue         QueueSnapshot
	Tier          tier.Snapshot
	Streams       StreamSnapshot
	Errors        ErrorSnapshot
	Dynamic       DynamicCounterSnapshot
	Histograms    RuntimeHistograms
}

func WriteRuntime(w io.Writer, input RuntimeInput) {
	cfg := input.Config
	fmt.Fprintf(w, "pig_version_info{version=%q} 1\n", cfg.Version)
	fmt.Fprintf(w, "pig_uptime_seconds %.0f\n", input.Uptime.Seconds())
	fmt.Fprintf(w, "pig_rejected_total %d\n", input.RejectedTotal)
	fmt.Fprintf(w, "pig_queue_current %d\n", input.Queue.Current)
	fmt.Fprintf(w, "pig_queue_total %d\n", input.Queue.Total)
	fmt.Fprintf(w, "pig_queue_timeout_total %d\n", input.Queue.Timeout)
	fmt.Fprintf(w, "pig_queue_wait_seconds_count %d\n", input.Queue.WaitCount)
	fmt.Fprintf(w, "pig_queue_wait_seconds_sum %.6f\n", input.Queue.WaitSecondsSum)
	fmt.Fprintf(w, "pig_queue_wait_config_seconds %.6f\n", cfg.QueueWait.Seconds())
	fmt.Fprintf(w, "pig_queue_wait_effective_cap_seconds %.6f\n", cfg.QueueWaitEffectiveCap.Seconds())
	fmt.Fprintf(w, "pig_queue_poll_config_seconds %.6f\n", cfg.QueuePoll.Seconds())

	writeTierMetrics(w, input.Tier)
	writeStreamMetrics(w, input.Streams)
	writeErrorMetrics(w, cfg, input.Errors)
	writeDynamicCounterMetrics(w, cfg, input.Dynamic)
	writeRuntimeHistograms(w, input.Histograms)
}

func writeTierMetrics(w io.Writer, tier tier.Snapshot) {
	fmt.Fprintf(w, "pig_tier_inflight{tier=%q} %d\n", requesttier.Basic.String(), tier.BasicInflight)
	fmt.Fprintf(w, "pig_tier_inflight{tier=%q} %d\n", requesttier.Premium.String(), tier.PremiumInflight)
	fmt.Fprintf(w, "pig_tier_waiting{tier=%q} %d\n", requesttier.Basic.String(), tier.BasicWaiting)
	fmt.Fprintf(w, "pig_tier_waiting{tier=%q} %d\n", requesttier.Premium.String(), tier.PremiumWaiting)
	fmt.Fprintf(w, "pig_tier_requests_total{tier=%q,decision=%q} %d\n", requesttier.Basic.String(), "accepted", tier.BasicAccepted)
	fmt.Fprintf(w, "pig_tier_requests_total{tier=%q,decision=%q} %d\n", requesttier.Premium.String(), "accepted", tier.PremiumAccepted)
	fmt.Fprintf(w, "pig_tier_requests_total{tier=%q,decision=%q} %d\n", requesttier.Basic.String(), "rejected", tier.BasicRejected)
	fmt.Fprintf(w, "pig_tier_requests_total{tier=%q,decision=%q} %d\n", requesttier.Premium.String(), "rejected", tier.PremiumRejected)
	fmt.Fprintf(w, "pig_tier_basic_limit %d\n", tier.BasicLimit)
	fmt.Fprintf(w, "pig_tier_premium_reserved_capacity %d\n", tier.PremiumReserved)
}

func writeStreamMetrics(w io.Writer, streams StreamSnapshot) {
	fmt.Fprintf(w, "pig_sse_keepalive_streams_total %d\n", streams.KeepAliveStreams)
	fmt.Fprintf(w, "pig_sse_keepalive_comments_total %d\n", streams.KeepAliveComments)
	fmt.Fprintf(w, "pig_sse_bridge_streams_total %d\n", streams.BridgeStreams)
	fmt.Fprintf(w, "pig_sse_bridge_upstream_errors_total %d\n", streams.BridgeUpstreamErr)
	fmt.Fprintf(w, "pig_sse_bridge_invalid_upstream_total %d\n", streams.BridgeInvalid)
	fmt.Fprintf(w, "pig_sse_bridge_copy_errors_total %d\n", streams.BridgeCopyErr)
}

func writeErrorMetrics(w io.Writer, cfg RuntimeConfig, errors ErrorSnapshot) {
	fmt.Fprintf(w, "pig_request_semantic_ttft_scan_limit_total %d\n", errors.RequestSemanticTTFTLimited)
	fmt.Fprintf(w, "pig_request_semantic_ttft_scan_limit_bytes %d\n", cfg.SemanticTTFTScanLimitBytes)
	fmt.Fprintf(w, "pig_proxy_upstream_errors_total %d\n", errors.ProxyUpstreamErr)
	fmt.Fprintf(w, "pig_proxy_body_copy_errors_total %d\n", errors.ProxyCopyErr)
	fmt.Fprintf(w, "pig_client_disconnects_total{phase=%q} %d\n", "queue", errors.ClientDisconnectQueue)
	fmt.Fprintf(w, "pig_client_disconnects_total{phase=%q} %d\n", "upstream", errors.ClientDisconnectUpstream)
	fmt.Fprintf(w, "pig_client_disconnects_total{phase=%q} %d\n", "response", errors.ClientDisconnectResponse)
	fmt.Fprintf(w, "pig_client_disconnect_upstream_cancellations_total %d\n", errors.ClientDisconnectCancel)
}

func writeDynamicCounterMetrics(w io.Writer, cfg RuntimeConfig, counters DynamicCounterSnapshot) {
	fmt.Fprintf(w, "pig_dynamic_enabled %d\n", num.BoolAsInt(cfg.DynamicEnabled))
	fmt.Fprintf(w, "pig_dynamic_enforce %d\n", num.BoolAsInt(cfg.DynamicEnforce))
	fmt.Fprintf(w, "pig_dynamic_poll_total{result=%q} %d\n", "ok", counters.PollOK)
	fmt.Fprintf(w, "pig_dynamic_poll_total{result=%q} %d\n", "failed", counters.PollFailed)
	fmt.Fprintf(w, "pig_dynamic_rejected_total %d\n", counters.DynamicRejected)
	fmt.Fprintf(w, "pig_backend_unavailable_total %d\n", counters.BackendUnavailable)
	fmt.Fprintf(w, "pig_backend_count %d\n", cfg.BackendCount)
}

func writeRuntimeHistograms(w io.Writer, histograms RuntimeHistograms) {
	histogram.WriteDurationHistogram(w, "pig_decision_duration_seconds", histograms.DecisionDuration)
	histogram.WriteDurationHistogram(w, "pig_proxy_time_to_first_byte_seconds", histograms.ProxyTTFB)
	histogram.WriteDurationHistogram(w, "pig_request_semantic_ttft_seconds", histograms.RequestSemanticTTFT)
	histogram.WriteDurationHistogram(w, "pig_proxy_total_duration_seconds", histograms.ProxyTotal)
	histogram.WriteDurationHistogram(w, "pig_internal_overhead_seconds", histograms.InternalOverhead)
}
