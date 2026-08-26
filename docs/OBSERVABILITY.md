# PIG Observability

Metrics, logs, Router projection, and the admin API format immutable controller
snapshots. Reading them never reruns policy or changes admission.

## Local endpoints

- `/pig/metrics` returns PIG metrics.
- `/v1/metrics` returns PIG metrics and a bounded copy of native upstream
  metrics.
- `/v1/upstream-status` returns Router-facing admission status.
- `GET/PATCH /admin/v1/predictive-policy` reads or atomically updates the
  TPS reference, window concurrency, and running limit.

These routes are handled locally and preserve their configured authentication
semantics. They never enter the public inference proxy.

`/v1/upstream-status` returns `0` for open, `1` for load protection where the
canonical inspect demand still fits, `2` for closed enforce intake, and `3` for
shadow-unavailable or unknown state.

## Decisions

```text
pig_predictive_admission_attempts_total
pig_predictive_admission_decisions_total{decision="fit|risk|unknown"}
pig_predictive_admission_outcomes_total{outcome}
pig_predictive_admission_protections_total{reason,scope}
pig_predictive_admission_enforced_rejects_total
pig_rejected_total
```

The bounded reasons are `open`, `controller_unavailable`,
`observation_missing`, `observation_invalid`, `observation_stale`,
`invalid_request`, `tps_reference`, `running_limit`, `window_concurrency`,
`runtime_identity_drift`, `resource_exhausted`, `counter_overflow`, and
`closed`.

`enforced_rejects_total` counts only enforce-mode pre-forward predictive 429s.
`pig_rejected_total` is the broader local 429 counter. Shadow protection is
observable but does not emit predictive 429 or Router backpressure.

Malformed JSON increments
`pig_client_protocol_errors_total{reason="invalid_json"}` without an admission
attempt. A rejected route increments only `pig_route_not_allowed_total` and
does not read its body, reserve, or call the backend.

## Request shape

```text
pig_predictive_scanner_inflight
pig_predictive_scanner_reserved_body_bytes
pig_predictive_scanner_saturated_total
pig_predictive_classifier_outcomes_total{outcome}
pig_predictive_request_streaming_total{state}
pig_predictive_request_decode_fanout_total{bucket}
pig_predictive_admission_decode_fanout_total{bucket,outcome}
```

The last-decision metric identifies `request` versus one-sequence `fallback`
demand without exposing body, user, model, or token labels:

```text
pig_predictive_tps_last_decision_info{
  action,reason,pressure_source,result,subreason,demand_source
}
pig_predictive_tps_request_decode_sequences
```

## TPS health

```text
pig_predictive_tps_reference
pig_predictive_tps_window_ready
pig_predictive_tps_window_qualified_samples
pig_predictive_tps_window_qualified_sequence_samples
pig_predictive_tps_window_qualified_sequence_seconds
pig_predictive_tps_window_aggregate
pig_predictive_tps_window_mean_active
pig_predictive_tps_latest_interval_qualified
pig_predictive_tps_latest_interval_aggregate
pig_predictive_tps_latest_interval_mean_active
pig_predictive_tps_latest_interval_sequence_seconds
pig_predictive_tps_generation_delta
pig_predictive_tps_preemption_delta
pig_predictive_tps_observed_running
pig_predictive_tps_observed_waiting
pig_predictive_tps_unobserved_sequences
```

`window_aggregate` is output tokens per wall second. `window_mean_active` is
output tokens per active Decode sequence-second and is compared with the
reference. The latest qualified interval supports immediate recovery and
prevents one low interval from closing an otherwise healthy rolling window.
An unqualified latest interval means no reliable current Decode evidence; it is
not fabricated as zero TPS.

The fixed decision matrix is:

```text
pig_predictive_tps_decisions_total{result,subreason}
```

Subreasons are `disabled`, `invalid_state`, `waiting`, `preemption`, `warming`,
`no_current_evidence`, `healthy_window`, `recovered_current`, and
`below_reference`. Denominator selection is auditable through:

```text
pig_predictive_tps_denominator_selections_total{source}
pig_predictive_tps_denominator_sequence_seconds_total{source}
```

Sources distinguish endpoint running, local forwarded exposure, local response
exposure, fallback liability, ties, and no usable denominator.

## Running and window bounds

```text
pig_predictive_running_limit
pig_predictive_running_limit_info{source}
pig_predictive_window_concurrency_limit
pig_predictive_admission_last_projected_running
pig_predictive_admission_last_projected_window_sequences
pig_predictive_capacity_projected_running
pig_predictive_capacity_projected_window_sequences
```

Running-limit source is one of `unknown`, `environment`,
`sglang_server_info`, or `admin`. The `admission_last` pair belongs to the last
real request decision. The `capacity` pair belongs to the canonical next
one-sequence Router inspection. Effective limit gauges always come from the
current policy revision, so an admin update cannot be confused with an older
last decision.

The metrics-window concurrency histogram is:

```text
pig_predictive_window_concurrency_observed_bucket{le}
pig_predictive_window_concurrency_observed_count
pig_predictive_window_concurrency_observed_sum
```

It samples the unreconciled Decode sequence count once per successful
observation, before reconciliation. Finite cumulative buckets are every integer
from `0` through `16`, then `20,24,28,32,36,40,44,48,52,56,60,63`. The
high-concurrency band at or above `64` is the cumulative delta
`+Inf - le="63"`; there are no finite 64+ buckets. Bucket iteration occurs on
the observer path, not the request admission hot path.

## Lifecycle and timing

```text
pig_predictive_admission_reservations
pig_predictive_admission_virtual_decode_sequences
pig_predictive_admission_sequence_liabilities
pig_predictive_admission_residual_debts
pig_predictive_admission_failures_total{phase}
pig_predictive_admission_prediction_duration_seconds
pig_predictive_admission_body_read_duration_seconds
pig_predictive_admission_shape_scan_duration_seconds
pig_predictive_admission_pre_forward_duration_seconds
```

Failure phases are `close`, `decide`, `forward`, `first_byte`, and `terminal`.
A transient liability until the next observation watermark is expected;
sustained residual debt or failure deltas warrant investigation.

## Goodput and backend view

```text
pig_predictive_successful_completion_tokens_total
pig_predictive_response_usage_outcomes_total{outcome}
pig_predictive_response_completion_tokens_total{bucket}
```

The token counter includes exact successful final usage only. Missing,
malformed, censored, non-2xx, timed-out, or disconnected responses add no
tokens. This is observability and never feeds admission.

`pig_backend_*` exposes proxy lifecycle, backend kind, current running/waiting,
and observer freshness. Native `/v1/metrics` data may include KV/cache/Prefill
telemetry, but PIG does not consume it for admission.

Router compatibility remains available through
`pig_predictive_router_backpressure_*` and the compatibility
`pig_dynamic_observed_*`/`pig_dynamic_global_limit*` gauges. Request-scoped
invalid shapes do not close canonical capacity.

## Logs

At `info`, protected decisions record only mode, enforcement, reason/scope, TPS
result/subreason, backend running/waiting, rolling mean/reference, projected
running/window demand, configured bounds/source, policy revision, and
suppression. Periodic controller status reports the same current policy and
capacity view. `debug` adds bounded window and lifecycle numbers.

Logs never include request bodies, prompts, prefixes, tokens, credentials,
endpoint URLs, user identifiers, or unbounded labels.

The old TPS-derived sequence-limit, current/post-admit, and QoS-budget metrics
are retired and must not be queried by current dashboards.
