# Observability

PIG keeps runtime logs short and exports detailed counters through a protected
Prometheus-style metrics endpoint.

## Logs

PIG logs one concise `pig_status` line at startup and then periodically while it
runs. The interval is controlled by `PIG_STATUS_LOG_INTERVAL_SECONDS`; set it to
`0` to disable periodic status logging.

```text
pig_status v=PIG-v0.10.13 backend={state=green backend=1/1 running=1 waiting=0 ...} pig={limit=50 admit=50 cap=50 queue=0 reject=0 tier_basic=1/49 tier_premium=0/1 ...} predictive={mode=enforce attempts=12 fit=4 risk=8 unknown=0 reject=8 last=existing_tps_at_risk/calibrated/6 last_reject=existing_tps_at_risk/calibrated/load/6 reservations=1 virtual_decode=1 pending_prefill=0/0/0 shadow_prefill=0/0/0/empty deferred=0 prefill_learning=7/1/2/1.998/1/1 hard_origin=1/0/0/0/0/0 completion_observer=4/4/4/4 router_bp=1/1/load/existing_tps_at_risk router_lease=1/7/2/5/2026-08-02T12:00:00Z/2026-08-02T12:00:05Z effective=1/1 raw=1/50}
```

The status line has three required parts:

- `v`: PIG version.
- `backend`: current backend load snapshot from vLLM or SGLang metrics.
- `pig`: current PIG limits and counters.
- `predictive`: predictive decisions, live reservations/deferred outcomes,
  completion-observer stages, Router backpressure as
  `active/applied/scope/reason`, lease counters/timestamps, and effective/raw
  `running/global-limit` pairs.
- `kv_shadow`: optional and present only when the separate legacy
  `KV_ADMISSION_MODE=shadow` path is enabled.

The startup configuration line includes
`predictive_admission=off|shadow|enforce`, `dynamic_ttft_protect=false`,
`predictive_ttft_observe=false|true`, `predictive_ttft_protect=false`, and the
bounded Router backpressure hold. Predictive TTFT observation is true only in
`shadow` or `enforce`; it never authorizes a TTFT admission reject. Metrics remain authoritative for the full
predictive decision, learning, reservation, and shadow-only observation state.
An immediate `predictive_router_backpressure event=activated ...` line records
each new bounded activation. Every later load-dependent reject renews the lease
from that latest reject before PIG writes 429. The first in-episode renewal and
then at most one per hold interval emit
`predictive_router_backpressure event=renewed ...`; suppressed renewal logs
still extend the deadline and advance durable counters/metrics, avoiding a
per-request log storm. The first metrics response that actually applies the
Router capacity projection also emits one
`predictive_router_backpressure event=router_capacity_applied ...` line with
the activation number and raw/effective running and global-limit values.
Concurrent or repeated scrapes emit that record at most once per activation;
the record means PIG exported the projection, while live Router status remains
the authority for proving that Router scraped and acted on it.

HTTP response-class and predictive completion metrics record only the final
response. Informational `100`-`199` responses are forwarded but do not advance
the `1xx` class, establish final-response timing, or terminate feedback state;
`101 Switching Protocols` remains a final response.

The log is intentionally compact. Per-lane counters, queue totals, dynamic
reasons, backend details, and classifier counters are exposed through metrics
instead of being printed on every log interval.

## Metrics Endpoint

Metrics are exposed at:

```text
/pig/metrics
/v1/metrics
```

The endpoint requires:

```text
Authorization: Bearer $TOKEN
```

When PIG is deployed behind HAProxy, route `/pig/metrics` to the PIG service
and keep the same bearer-token check in HAProxy.

Downstream gateways can use `GET /v1/upstream-status` for a compact aggregate
capacity signal. It uses the same bearer-token check and returns only one
plain-text integer: `0` green, `1` yellow, `2` red, `3` unknown.

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
| Predictive Admission | `pig_predictive_*` | v0.10 pre-forward mode, decisions, enforced rejects, reservations, bounded learning, shadow-only observations, lifecycle failures, TPS target quality, Router backpressure, and estimator/prediction latency. |
| Legacy KV Shadow | `pig_kv_admission_*`, `pig_kv_shadow_*`, `pig_kv_estimator_*` | Historical v0.9 KV-only shadow decisions and reservations; keep this mode off when using v0.10 predictive admission. |
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
- `pig_predictive_admission_mode_info`, `pig_predictive_admission_enabled`, and
  `pig_predictive_admission_enforce`: prove whether v0.10 predictive admission
  is `off`, `shadow`, or `enforce`. Check these after every restart because
  learning restarts cold.
- `pig_predictive_admission_attempts_total`,
  `pig_predictive_admission_decisions_total{decision="fit|risk|unknown"}`,
  `pig_predictive_admission_enforced_rejects_total`, and
  `pig_predictive_admission_last_decision_info`: show prospective decisions.
  In shadow, risk and unknown must not change the client response. In enforce,
  every non-fit result must be accounted as a pre-forward reject.
- `pig_predictive_admission_intake_open`,
  `pig_predictive_admission_reservations`, and
  `pig_predictive_admission_retired_reservations`: show current eligibility and
  resource accounting. Active reservations must return to zero after idle;
  retired entries are a bounded reconciliation queue, not live KV demand.
- `pig_predictive_router_backpressure_active`,
  `pig_predictive_router_backpressure_applied`,
  `pig_predictive_router_backpressure_state_info`, expiry/hold timestamps, and
  activation/extension counters show whether a load-dependent predictive
  protection is currently being projected to Router.
  `pig_predictive_router_backpressure_latest_load_reject_at_seconds` and
  `pig_predictive_router_backpressure_renewal_logs_total` /
  `pig_predictive_router_backpressure_renewal_logs_suppressed_total` prove that
  sustained 429 protection is renewing even when log output is rate-limited.
  During an active load-scope lease, `active=1,applied=1` remains mandatory
  even when raw and predictive running briefly reach zero. In that instant PIG
  publishes minimum effective running and limit values of one. Exact expiry
  removes the sentinel and restores raw capacity immediately; a persistent
  `active=1,applied=0`, sticky post-expiry clamp, or idle rejection loop is a
  defect rather than an escape hatch.
- `pig_predictive_admission_virtual_decode_sequences` is the predictive
  coordinator's current virtual upper decode count. It includes the latest
  predictive backend observation plus still-unabsorbed reservations.
  `pig_predictive_router_backpressure_predictive_running` records the value
  used by the active projection. This prevents a separate dynamic poll lag from
  hiding a protection that has already rejected a request. When the episode is
  inactive, the backpressure reason/source are normalized to `none/unknown`;
  cumulative activation/extension counters and the last episode timestamps
  remain available for history.
- `pig_dynamic_observed_running_raw` / `pig_dynamic_observed_running` and
  `pig_dynamic_global_limit_raw` / `pig_dynamic_global_limit` compare backend
  truth with the effective Router-consumed capacity. During an applied window,
  effective running divided by effective global limit must be at least 100%.
  `pig_dynamic_admission_limit` continues to report the actual local QoS gate;
  it is not repurposed as a Router signal. If the raw global limit is zero
  during an applied window, the effective limit equals positive effective
  running as a Router-only 100% fullness sentinel; raw and local limits remain
  zero.
- `pig_predictive_shadow_observations` and
  `pig_predictive_shadow_observations_total{result="created|terminated|qualified|censored|dropped"}`:
  show bounded, payload-free observation records for shadow predicted-risk
  requests. They do not contribute virtual KV or concurrency and must converge
  to zero after terminal completion or shutdown. Growth in `dropped` means the
  configured observation cap was reached; it must not change shadow forwarding.
- `pig_predictive_admission_shadow_pending_prefills`,
  `pig_predictive_admission_shadow_pending_prefill_tokens`,
  `pig_predictive_admission_shadow_pending_prefill_attribution_valid`, and
  `pig_predictive_admission_shadow_pending_prefill_attribution_state_info` show
  the current anonymous prefill pressure retained only for shadow learning.
  The state label is fixed to `empty`, `single`, `aggregate`, or
  `incompatible`. A compatible multi-request window must report
  `aggregate` with valid `1`; different Manager bases, changed sequences,
  malformed increments, or overflow must report `incompatible` with valid `0`.
  These values do not create reservations and never authorize current-request
  admission or Router capacity changes.
- `pig_predictive_deferred_outcomes` and
  `pig_predictive_deferred_outcomes_total{result="released|terminated|qualified|censored|dropped"}`:
  show bounded numeric outcomes whose upstream GPU/KV/TPS accounting has
  already been released while the downstream handler is still finishing. They
  never contribute live resource demand and must return to zero after idle.
  `dropped` loses only a learning opportunity; `qualified` must increase only
  after a successful final handler result, while disconnects and write errors
  increase `censored` without learned headroom.
- `pig_predictive_input_size_samples_total`,
  `pig_predictive_input_size_invalidations_total`,
  `pig_predictive_input_size_samples_stored`,
  `pig_predictive_input_size_estimates_total{source="cold|learned"}`, and the
  `pig_predictive_input_size_last_*` metrics show whether qualified
  `usage.prompt_tokens` feedback is improving later approximate-size estimates.
  Zero or sparse samples must continue producing cold estimates rather than
  closing intake.
- `pig_predictive_learning_samples_total`,
  `pig_predictive_learning_invalidations_total`,
  `pig_predictive_learning_cells`, and
  `pig_predictive_learning_global_samples`: show bounded TPS/TTFT/TPOT residual
  learning. TTFT is diagnosis-only and cannot reject; TPS and TPOT remain
  admission inputs. Invalidation after backend identity or capacity drift must
  discard incompatible learning before intake recovers on a new coherent epoch.
- `pig_predictive_learning_hard_adverse_total{dimension="existing_tps|new_tps|tpot"}`:
  separates qualified hard-red evidence by protected dimension. It must not
  increase for censored local wall-clock outcomes. A corroborated joining
  request under real concurrent work may tighten the next pre-forward decision;
  a standalone idle adverse request must not create an idle self-lock.
- `pig_predictive_learning_hard_adverse_origin_total{dimension="existing_tps|new_tps|tpot",origin="exploratory|non_exploratory"}`:
  exposes six fixed series identifying whether each hard outcome came from a
  one-step frontier probe or an ordinary forecast. For every dimension, the two
  origin counters must sum exactly to the corresponding hard-adverse total.
- `pig_predictive_existing_prefill_last_user_tps`,
  `pig_predictive_existing_prefill_last_user_tps_valid`, and
  `pig_predictive_existing_prefill_last_exploratory`: expose the last accepted
  anonymous stable backend prefill-interference value and its original
  admission origin. They carry no request, user, model, or prompt labels.
- `pig_predictive_tps_outcomes_total{result="backend_qualified|local_corroborated|local_censored|missing|rejected"}`:
  distinguishes structurally qualified backend timing, local direction checks
  corroborated by a stable overlapping vLLM generation window, censored local
  wall-clock outcomes, missing targets, and structurally rejected targets. Do
  not treat all terminal requests as valid TPS training data.
- `pig_predictive_qualified_user_tps_count`, `_sum`, and `_bucket`, together
  with `pig_predictive_qualified_tpot_seconds_count`, `_sum`, and `_bucket`,
  expose bounded aggregate distributions for qualified training values without
  request, user, model, or payload labels. For corroborated local outcomes the
  values come from the backend generation window, not downstream wall time.
- `pig_predictive_completion_observer_events_total{event="attached|claimed|usage|terminal"}`:
  locates a missing completion-feedback stage without request identifiers or
  payload labels. For ordinary successful predictive forwards, the four
  cumulative stages should progress together; a widening gap identifies
  attachment, response eligibility/claim, usage parsing, or terminal cleanup
  before input-size and scheduler outcome counters are interpreted.
- `pig_predictive_admission_prediction_duration_seconds` and
  `pig_predictive_admission_estimator_duration_seconds`: separate scheduler/
  reservation latency from the bounded JSON estimator. During the live gate,
  compare p95/p99 with the plan thresholds by request-size cohort; an aggregate
  histogram alone cannot prove the 16 KiB and 64 KiB targets. These two
  histograms use 10 us through 100 ms buckets, including exact 0.25 ms and 1 ms
  boundaries; general TTFT and total-duration histograms retain their wider
  service-latency buckets.
- `pig_predictive_admission_failures_total{phase="close|decide|forward|semantic|completion|resource_release|terminal"}`:
  should remain unchanged in a healthy canary. Any increase needs matching
  incremental logs and lifecycle/accounting verification before broader use.
- `pig_backend_kv_capacity_tokens`, `pig_backend_kv_active_tokens`,
  `pig_backend_kv_available_tokens`, `pig_backend_kv_evictable_tokens`, and
  `pig_backend_kv_token_metrics_valid`: show the backend-specific token model.
  vLLM capacity comes from `kv_cache_size_tokens`; SGLang TP-rank duplicates are
  deduplicated and evictable tokens are excluded from active pressure.
- `pig_kv_admission_mode_info` and `pig_kv_admission_shadow_enabled` describe
  only the historical `KV_ADMISSION_MODE` path. They do not prove the v0.10
  predictive mode and should remain `off`/`0` in the v0.10 deployment.
- `pig_kv_shadow_decisions_total{decision="..."}` separates `fit`,
  `over_budget`, `emergency_red`, `backend_waiting`,
  `preemption_cooldown`, `stale_metrics`, `capacity_unknown`, and
  `unsupported_request`. Unknown/stale/unsupported must not be read as fit.
- `pig_kv_shadow_reservations`,
  `pig_kv_shadow_unabsorbed_reservation_tokens`, and per-backend
  `pig_kv_shadow_backend_unabsorbed_tokens` show blind-window protection between
  metrics samples. They must return to zero after completion or expiry.
- `pig_kv_shadow_backend_resets_total` and
  `pig_kv_shadow_reservations_expired_total` expose backend counter/capacity
  resets and timeout reconciliation. Unexpected growth should be investigated
  before any enforcement design.
- `pig_kv_estimator_duration_seconds` and
  `pig_kv_shadow_decision_duration_seconds` show the bounded byte-scan cost and
  atomic decision/reservation cost. They are separate from the existing full
  PIG decision histogram.
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
- `pig_dynamic_prefill_protected_running`,
  `pig_dynamic_prefill_transition_active`, and
  `pig_dynamic_prefill_settling_active`: distinguish locally tracked prefill
  requests from decode-running load. During a transition, capacity learning
  should report `state="prefill_freeze"`; low generation TPS must not reduce
  `pig_dynamic_capacity_learned_limit`. A semantic SSE delta removes the
  corresponding request from protection, and settling is bounded to one metrics
  window. Backend waiting may still make the current global limit `0` without
  changing the retained learned limit.
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
