# Observability

PIG keeps runtime logs short and exports detailed counters through a protected
Prometheus-style metrics endpoint.

## Logs

PIG logs one concise `pig_status` line at startup and then periodically while it
runs. The interval is controlled by `PIG_STATUS_LOG_INTERVAL_SECONDS`; set it to
`0` to disable periodic status logging.

```text
pig_status v=PIG-v0.8.11 backend={state=green backend=1/1 running=0 waiting=0 ...} pig={limit=50 admit=50 cap=50 queue=0 reject=0 tier_basic=0/49 tier_premium=0/1 ...}
```

The log line has three parts:

- `v`: PIG version.
- `backend`: current backend load snapshot from vLLM or SGLang metrics.
- `pig`: current PIG limits and counters.

The log is intentionally compact. Per-lane counters, queue totals, dynamic
reasons, backend details, and classifier counters are exposed through metrics
instead of being printed on every log interval.

## Metrics Endpoint

Metrics are exposed at:

```text
/pig/metrics
```

The endpoint requires:

```text
Authorization: Bearer $TOKEN
```

When PIG is deployed behind HAProxy, route `/pig/metrics` to the PIG service
and keep the same bearer-token check in HAProxy.

## Metric Groups

Body/output lanes are metrics labels only. They do not create separate
lane-specific QoS caps.

| Group | Metrics | Use |
| --- | --- | --- |
| Runtime | `pig_version_info`, `pig_uptime_seconds` | Process version and uptime. |
| Queue | `pig_queue_*` | Current queue depth, wait time, and timeout pressure. |
| Tier | `pig_tier_*` | Basic/premium in-flight counts, waiting counts, accepted/rejected totals, and premium reserve. |
| Requests | `pig_requests_total`, `pig_inflight`, `pig_request_*`, `pig_response_*` | Request counts, latency, body size, and status classes. |
| PIG Overhead | `pig_decision_duration_seconds`, `pig_proxy_time_to_first_byte_seconds`, `pig_request_semantic_ttft_seconds`, `pig_proxy_total_duration_seconds`, `pig_internal_overhead_seconds` | PIG decision time, first byte, first useful streaming data, upstream/proxy wait, and PIG-only overhead. |
| Rejections | `pig_rejected_total`, `pig_dynamic_rejected_total`, `pig_backend_unavailable_total` | QoS rejects and no-usable-backend events. |
| SSE Keep-Alive | `pig_sse_keepalive_*`, `pig_sse_bridge_*` | Explicitly enabled streaming comment injection, early bridge streams, and bridge error counters. |
| Proxy Errors | `pig_proxy_upstream_errors_total`, `pig_proxy_body_copy_errors_total` | Upstream connection failures and response-copy failures that are not explained by a known client disconnect. |
| Client Disconnects | `pig_client_disconnects_total`, `pig_client_disconnect_upstream_cancellations_total` | Client-side disconnects while waiting in queue, waiting for upstream headers, or copying the upstream response. |
| Dynamic QoS | `pig_dynamic_*` | Load state, learned limits, pressure limits, per-user TPS observations, and TTFT learning. |
| Backend | `pig_backend_*` | Per-backend health, in-flight count, load, KV usage, generation TPS, and TTFT. |
| Classifier | `pig_json_*`, `pig_*output*` | Optional request body and output-token classification. |
| Backend Priority | `pig_backend_priority_*` | Trusted-tier JSON priority injection and rewrite overhead. |

## Operational Checks

For production operation, watch these first:

- `pig_backend_unavailable_total`: should not grow during normal backend health.
- `pig_dynamic_rejected_total`: shows QoS policy rejection pressure.
- `pig_queue_current` and `pig_queue_timeout_total`: show whether requests are
  waiting and timing out.
- `pig_queue_wait_config_seconds` and `pig_queue_wait_effective_cap_seconds`:
  show the configured queue wait and PIG's actual short-wait cap.
- `pig_tier_inflight`, `pig_tier_waiting`, `pig_tier_basic_limit`, and
  `pig_tier_premium_reserved_capacity`: show whether provider traffic is leaving
  room for direct traffic and whether premium requests are waiting. The premium
  reservation is dynamic: for example `premium 0/1` keeps one empty premium
  slot, while `premium 1/2` means one premium request is in flight and one more
  slot remains reserved.
- `pig_tier_requests_total`: separates accepted and rejected request counts for
  `basic` and `premium` traffic.
- `pig_internal_overhead_seconds` and `pig_decision_duration_seconds`: should
  stay near zero compared with request latency. If they rise while
  `pig_queue_current` is `0`, PIG itself is adding measurable work.
- `pig_backend_priority_rewrite_total`, `pig_backend_priority_skipped_total`,
  and `pig_backend_priority_failed_total`: show whether backend priority
  injection is rewriting, skipping, or failing request-body rewrites. A growing
  skipped count usually means non-JSON, chunked, oversized, or busy-slot
  requests. A growing failed count usually means invalid JSON or body read
  errors.
- `pig_backend_priority_body_bytes`, `pig_backend_priority_buffer_bytes`, and
  `pig_backend_priority_stream_buffer_bytes`: show the maximum body size
  eligible for priority rewrite, the maximum size using the in-memory rewrite
  fast path, and the maximum internal streaming scanner buffer size. A full-body
  buffer value of `0` means all eligible rewrites use the streaming path. Known
  request bodies smaller than the stream buffer maximum use 4 KiB-or-larger
  power-of-two buffer buckets.
- `pig_backend_priority_rewrite_duration_seconds_sum` and
  `pig_backend_priority_rewrite_duration_seconds_count`: expose rewrite cost.
  Divide sum by count to estimate average priority injection time. In the
  default configuration this is the cost of the combined streaming JSON body
  pass that both writes trusted backend priority and removes empty
  `messages[].tool_calls` arrays.
- `pig_proxy_time_to_first_byte_seconds` and `pig_proxy_total_duration_seconds`:
  track the backend/proxy portion of latency after PIG has accepted a request.
  First byte is not the same as first semantic SSE data such as
  `reasoning_content` or `content`.
- `pig_request_semantic_ttft_seconds`: tracks accepted `200 text/event-stream`
  responses from PIG request arrival until the first useful SSE `data:` payload
  reaches the client. It counts non-empty `reasoning_content`, `reasoning`,
  `content`, tool-call deltas, and compatible Responses API
  output/reasoning/tool deltas. It ignores headers, comments, empty data, and
  `[DONE]`.
- `pig_dynamic_observed_ttft_source_info`: shows whether dynamic TTFT learning
  is currently using PIG-observed `semantic` TTFT or the `backend` TTFT fallback.
  Once semantic stream samples exist, PIG keeps using semantic TTFT so reasoning
  streams are judged by first useful output rather than by headers or empty
  deltas. Immediately after switching to `semantic`, smoothed TTFT can remain
  `0` until enough semantic samples exist for a reliable learning window.
- `pig_request_semantic_ttft_scan_limit_total`: should stay near `0`. Growth
  means PIG scanned the configured prefix of a stream without seeing useful SSE
  data.
- `pig_sse_keepalive_streams_total` and
  `pig_sse_keepalive_comments_total`: show whether explicitly enabled low-load
  streaming keep-alives are active and how many comments were emitted.
- `pig_sse_bridge_streams_total`: shows accepted streaming requests where PIG
  opened an explicitly enabled early SSE bridge before upstream headers arrived.
- `pig_sse_bridge_upstream_errors_total`,
  `pig_sse_bridge_invalid_upstream_total`, and
  `pig_sse_bridge_copy_errors_total`: should stay at `0`. Growth here means an
  early bridge hid an upstream status or hit a broken stream path and needs
  investigation before broad rollout.
- `pig_client_disconnects_total{phase="queue|upstream|response"}`: separates
  client-side aborts from real proxy/backend errors. Queue-phase growth means a
  client disconnected while waiting for admission. Upstream-phase growth means
  the client disappeared before upstream headers arrived. Response-phase growth
  means the client disconnected while PIG was copying the upstream response.
- `pig_client_disconnect_upstream_cancellations_total`: counts disconnects that
  also canceled an in-flight upstream request or response body. It should rise
  together with the phase-specific disconnect counter when PIG successfully
  stops backend work after a client abort.
- `pig_proxy_upstream_errors_total` and `pig_proxy_body_copy_errors_total`:
  should stay near `0`. Growth here points to connection failures, backend
  resets, or mid-stream copy failures that were not classified as client
  disconnects.
- Client disconnects are recorded internally as status `499` for lane/status
  metrics. PIG does not send a synthetic `499` response to clients; the client
  has already closed the connection.
- `pig_dynamic_observed_single_user_tokens_per_second`: should generally stay
  at or above the target workload floor.
- `pig_dynamic_observed_ttft_smoothed_p95_seconds`: should converge toward the
  default `1s` target when there is enough traffic to learn from.
- `pig_dynamic_observed_ttft_smoothed_p99_seconds` and
  `pig_dynamic_ttft_p99_high_count`: show whether tail first-token latency is
  high enough to slow or reduce the learned TTFT cap.
- `pig_dynamic_ttft_learned_limit`, `pig_dynamic_ttft_target_limit`, and
  `pig_dynamic_ttft_limit`: show the learned TTFT cap, the next upward probe
  target, and the currently applied TTFT cap. In healthy recovery, the target
  should rise only in small steps and only after enough representative load is
  observed; it should not jump straight back to the global cap.
- `pig_dynamic_ttft_learning_reason_info{state="...",reason="...",target_reason="..."}`:
  shows why the TTFT learner chose its state and where the learned/target cap
  came from, for example high p95 latency, high p99 latency, insufficient TTFT
  signal, a healthy recovery probe, or a previous learned limit.
- `pig_dynamic_observed_kv_cache_usage` and
  `pig_dynamic_observed_preemptions`: indicate memory or scheduler pressure.
- `pig_dynamic_capacity_learned_limit` and
  `pig_dynamic_capacity_target_limit`: show whether the learner is converging up
  or down.
- `pig_dynamic_capacity_projected_limit` and
  `pig_dynamic_capacity_learning_reason_info{state="...",reason="...",target_reason="..."}`
  show the throughput learner's immediate projected cap, why its current state
  was chosen, and where the target limit came from. Reasons are bounded internal
  values such as `pig_below_target`, `severe_pressure`,
  `healthy_window_satisfied`, `ttft_not_healthy`, and
  `low_confidence_bound`.
- `pig_dynamic_capacity_estimate_info`,
  `pig_dynamic_capacity_raw_limit`, `pig_dynamic_capacity_safe_limit`, and
  `pig_dynamic_capacity_low_confidence_limit`: show the independent throughput
  capacity estimator before the learner consumes it.
- `pig_dynamic_capacity_representative_load` and
  `pig_dynamic_representative_user_tps_load`: show whether the current sample is
  considered representative enough for capacity estimation and user-visible TPS
  learning. These are useful when a high low-load TPS sample is intentionally
  held instead of raising the cap.
- `pig_dynamic_single_user_tps_capacity_ratio` and
  `pig_dynamic_single_user_tps_capacity_ratio_max`: show the configured safety
  ratio used by the clean throughput estimator and the max ratio used by
  backend-routing capacity scoring.
- `pig_dynamic_hard_global_limit`, `pig_dynamic_state_limit`,
  `pig_dynamic_throughput_limit`, `pig_dynamic_ttft_limit`,
  `pig_dynamic_pressure_limit`, `pig_dynamic_prefill_limit`, and
  `pig_dynamic_availability_limit`: show each clean-design cap component before
  PIG publishes the final `pig_dynamic_global_limit`.
- `pig_dynamic_pressure_limit_info{reason="...",target_reason="..."}`: shows
  why the pressure guard chose its current cap, for example preemption, backend
  waiting, KV pressure, retained learned pressure memory, or healthy KV
  headroom. If a backend waiting or unavailable override closes current intake,
  this reason follows that override while preserving the long-term learned
  capacity separately. Retained `learned_cap` can still win the final cap, but
  it does not mark `scheduler_pressure_capacity` yellow unless it is actively
  binding current demand or an active pressure signal is present.
- `pig_dynamic_prefill_limit_info{reason="...",target_reason="..."}`: shows
  why the prefill guard chose its current cap, for example backend waiting,
  running at an observed decode cap, the prefill-protected threshold, a
  prefill floor, or a throughput learned-cap floor. If a backend waiting or
  unavailable override closes current intake, this reason follows that override.
- `pig_dynamic_final_limit_info{reason="..."}`: shows which clean-design layer
  currently wins the final `min()` composition, for example `throughput`,
  `ttft`, `pressure`, `prefill`, `backend_waiting`, or
  `backend_unavailable`.
